# AfterGuard reference

`aftermarket guard` stands in front of an HTTP app that can't protect itself, such as an old admin panel with no login or an internal API with no rate limits, and turns away requests that break your rules. The app stays exactly as it is. The [README](../README.md) explains what the tool is for, and the [threat model](guard-threat-model.md) says what AfterGuard defends against and what it doesn't.

## The one thing to get right

**The app must be reachable only through AfterGuard.** If it also listens on an address clients can reach, every rule can be walked around. Bind the app to `127.0.0.1` on the same machine, or put it on a network only AfterGuard can reach. AfterGuard can't check this for you, so `guard check` reminds you every time.

## Commands

```text
aftermarket guard run   --config guard.yaml --upstream URL [--listen ADDR] [--dry-run] [--log FILE|-]
aftermarket guard check --config guard.yaml
aftermarket guard test  --config guard.yaml --fixture fixture.yaml
aftermarket guard hash  < key.txt
```

- **`run`** listens on `--listen` (`127.0.0.1:8080` by default) and forwards what the rules allow to `--upstream`. It answers `/_aftermarket/healthz` itself; `--health-path` changes or turns off that path.
- **`--dry-run`** forwards everything and logs what each request would have got. Use it to try a config on real traffic before it blocks anything.
- **`--log`** writes the decision log as JSON lines: `-` for stderr (the default), a file name to append to, or empty for none.
- **`check`** validates the config, lists every problem with its line number, lists the rules, and warns about a `default: allow`, rules that can never match, and plain HTTP.
- **`test`** runs fixture requests through the rules and compares each decision with what you expect.
- **`hash`** reads a key or password on standard input and prints its SHA-256, which is what the config holds. Reading it from standard input keeps it out of shell history.

Exit codes: 0 for success, 1 for problems in the config or a failing fixture, 2 for usage errors.

## Config file

```yaml
version: 1                   # optional; must be 1
default: deny                # deny (the default) or allow: what happens when no rule matches

limits:
  max_body: 1MiB             # the largest request body forwarded
  upstream_timeout: 30s      # how long the app may take to start answering

server:
  max_header_bytes: 16KiB
  header_timeout: 10s        # time to send the request head
  body_timeout: 60s          # time to send the body, after the head
  idle_timeout: 2m           # how long an idle keep-alive connection stays open
  write_timeout: 5m          # time to send an answer, after the request has arrived
  max_connections: 1024
  max_connections_per_address: 64

tls:                         # optional; without it AfterGuard serves plain HTTP
  cert: cert.pem
  key: key.pem

paths:
  ignore_case: false         # match paths without regard to case, for apps that do the same
  allow_semicolons: false    # let ; through in paths, for apps that use path parameters

trusted_proxies: [10.0.0.0/8]   # optional; see "Client addresses"

credentials:
  api_key_header: X-Api-Key       # or none
  api_key_query: ""               # a query key that carries an API key, if any
  keep: false                     # forward the credential to the app
  identity_header: X-Aftermarket-Client   # or none
  failed_logins: {rate: 5/m, burst: 5}

clients:
  - id: billing
    api_key_sha256: f04bf1cd...    # from aftermarket guard hash
  - id: admin
    basic_user: admin
    basic_password_sha256: 729d8864...

limit_table: 100000          # how many rate-limit buckets are kept at most
strip_headers: [X-Internal-Role]   # request headers never forwarded, besides the built-in ones

rules:
  - id: admin-office
    when: {path: /admin/**}
    from: [192.168.0.0/16]
    client: [admin]
    allow: true
    limit: {rate: 60/m, burst: 10, per: client}
    max_body: 64KiB
    json: true
    upgrade: [websocket]     # optional; upgrades are refused unless listed

response:
  set_headers: {X-Content-Type-Options: nosniff, X-Frame-Options: DENY}
  remove_headers: [Server, X-Powered-By]
```

Only `rules:` is required. Unknown keys are errors, and paths in the file are relative to its folder.

## Rules

Rules are checked in the order written, and the first one that matches decides. A request no rule matches gets the `default`: with `deny`, a 403.

A rule matches when all its conditions hold:

- **`when:`** the block the other Aftermarket tools use: `method`, `path` (a glob: `*` within one segment, `**` across segments), `query` and `headers`. `status` and `content_type` describe answers, so they aren't allowed here; match a request's `Content-Type` under `headers:`. An allow rule that names a query key or header needs every copy of it to match, so `?role=user&role=admin` can't get in on the first value.
- **`from:`** a list of addresses or ranges such as `192.168.0.0/16` or `::1`. See "Client addresses".

Then the rule acts, with exactly one of:

- **`deny: true`** or **`deny: 404`**: turn the request away with 403, or the status given.
- **`allow: true`**: let it through, after these optional checks, in this order:
  - **`client:`** a list of client ids, or `any` for any known client. A request with no credential gets 401, with a credential AfterGuard doesn't recognise 401, and with a known credential not on the list 403.
  - **`limit:`** a rate limit: `rate` such as `60/m` (per `s`, `m` or `h`), `burst` (the most at once; the rate's count by default), and `per`: `address` (the default), `client` or `rule` (one bucket for everyone). Over the limit gives 429 with `Retry-After`.
  - **`max_body:`** a body larger than this gives 413.
  - **`json: true`** a body, when there is one, must be valid JSON, or 400.
  - **`upgrade:`** the protocols a request may switch to, such as `[websocket]`. A request asking for any other upgrade gets 403, and so does one that no rule matched under `default: allow`. `h2c` is never allowed, since an HTTP/2 tunnel would carry requests past every rule.

A rule with `when:` and `from:` empty matches every request, and `check` warns about any rule after it. It also warns about a rule an earlier one always matches first in the common cases, such as `/admin/users` after `/admin/**`.

## Credentials

AfterGuard knows two kinds: an API key, sent in the `api_key_header` (or the `api_key_query` key), and HTTP Basic, a user name and password. The config holds only SHA-256 hashes, made with `aftermarket guard hash`. Every request compares against every client's hash in constant time, so timing tells nothing about which one is close.

SHA-256 is fast, so a short password could be guessed from a stolen config. Use long random keys and passwords, 32 characters or more; `hash` warns about anything under 16.

**Failed logins** are limited per address: by default, after 5 wrong credentials in a minute, every request from that address that needs a credential gets 429 until the bucket refills, the right credential included. Each attempt takes its place in the count before the credential is compared and gives it back when the credential is right, so a burst of guesses sent at once gets no more comparisons than the limit. Two `Authorization` headers, or two API keys, count as a wrong credential.

**Before forwarding,** AfterGuard removes the credentials it checked: the API key header or query key, and `Authorization` when any client uses Basic. With `keep: true` they go on to the app. When a request was let in by a client rule, AfterGuard adds the `identity_header` (`X-Aftermarket-Client: billing`) so the app knows who called. Any copy of that header from the client is removed first, under every spelling, including `x_aftermarket_client`.

Single sign-on (OAuth, OpenID Connect, SAML) isn't part of AfterGuard yet.

## Client addresses

A request's address is the address of the connection. `X-Forwarded-For` is believed only when the connection comes from a range listed in `trusted_proxies`, and then only its last entry: the hop that proxy added. Everything before it was written by someone AfterGuard can't vouch for.

The app gets `X-Forwarded-For` with that one address, `X-Forwarded-Host` and `X-Forwarded-Proto`, all set by AfterGuard. Copies the client sent, `Forwarded` and `X-Real-Ip` included, are removed.

Some servers and frameworks also let a request rewrite its own path, method or address with a header. These never reach the app: `X-Original-Url`, `X-Rewrite-Url`, `X-Override-Url`, `X-Http-Method-Override`, `X-Http-Method`, `X-Method-Override`, `True-Client-Ip`, `X-Client-Ip`, `Cf-Connecting-Ip`, `X-Cluster-Client-Ip`, `X-Forwarded-Port`, `X-Forwarded-Prefix`, `X-Forwarded-Server`, `X-Forwarded-Scheme`, `X-Forwarded-Ssl`, `X-Url-Scheme` and `Http2-Settings`. `strip_headers:` adds your own.

Header names with an underscore, such as `X_Role`, are dropped before the rules see the request, as nginx does by default. Some frameworks read `X_Role` as `X-Role`, and a rule matching `X-Role` would never see it.

Rate limits count IPv6 addresses by their /64 network, since one machine usually has a whole /64 to send from.

## Requests that could be read two ways

Gateways and apps don't always read a request the same way, and attacks such as request smuggling live in that gap. AfterGuard rejects with 400, and never cleans up, any request with:

- both `Content-Length` and `Transfer-Encoding`, or `Transfer-Encoding` twice
- a header folded over two lines, or lines ending in a bare LF instead of CRLF
- a target that isn't a path, such as `http://host/...` or `*`
- an encoded slash, backslash, dot, NUL or percent sign in the path (`%2f`, `%5c`, `%2e`, `%00`, `%25`)
- a backslash, a control character, an empty segment (`//`), a `.` or `..` segment, or invalid UTF-8 in the path
- a semicolon in the path (unless `allow_semicolons` is on) or in the query string, or a query string that can't be read
- characters outside ASCII in the path, when `ignore_case` is on: apps that ignore case fold some of them onto plain letters (`ſ` onto `s`) in ways AfterGuard can't predict
- a method that isn't in upper case, such as `delete`
- `Transfer-Encoding` on an HTTP/1.0 request
- an upgrade to `h2c`, or an `Http2-Settings` header

Paths are matched decoded, so `/%61dmin` is judged as `/admin`. The app gets the path AfterGuard judged, in its usual encoding, so it never sees bytes the rules didn't.

To find where each request starts, AfterGuard follows every request body on a connection the way Go's server reads it, chunk by chunk. If it ever loses its place, every later request on that connection is rejected; so is a request it has no verdict for, and `OPTIONS *`. After a rejected request, or an exchange with the app that failed, the connection is closed. Once the app answers an upgrade with 101, the connection carries the other protocol and isn't read as HTTP any more.

AfterGuard serves HTTP/1.1 (and 1.0) only, HTTPS included, so every request goes through these checks.

## Answers and headers

AfterGuard's own answers are short, such as `aftermarket guard: forbidden`, and never say which rule decided. If AfterGuard itself fails while judging a request, the answer is 500 and nothing is forwarded. They carry the `set_headers` from `response:`, plus `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`.

`response:` changes every answer from the app: `set_headers` sets headers (replacing what the app sent), and `remove_headers` removes headers such as `Server` and `X-Powered-By` that give away versions. Security headers are only added when you name them, since a wrong `Content-Security-Policy` or `Strict-Transport-Security` can break an app.

## TLS

With `tls:` AfterGuard serves HTTPS, TLS 1.2 or newer, from a certificate and key in PEM files. Renew them by replacing the files: AfterGuard checks them at most every 10 seconds and loads the new pair when both read; a half-copied pair is ignored and the old one stays in use. Without `tls:` it serves plain HTTP, which fits behind a load balancer that handles TLS.

When the app is called over HTTPS, its certificate is checked as usual.

## Limits

- Request heads over `max_header_bytes` get 431. Slow clients get `header_timeout` for the head and `body_timeout` more for the body.
- Bodies over `limits: max_body` get 413, before anything is forwarded when the size is known, and as soon as the limit is passed when it isn't.
- Connections over `max_connections`, or over `max_connections_per_address` from one address, are closed as soon as they open, and logged. An IPv6 /64 counts as one address. Connections from `trusted_proxies` count toward `max_connections` only, since many clients share a proxy's address.
- `write_timeout` bounds how long an answer may take to send, so a client that never reads can't hold a connection. Upgraded connections aren't affected.
- Rate-limit buckets live in a table of `limit_table` entries; per-rule buckets (`per: rule`) are kept apart and never dropped. When the table is full, a bucket that has refilled completely makes room, since dropping it changes nothing. If none near the least recently used end has, a request that needs a new bucket gets 429 instead: a flood of new addresses can't reset anyone's limit or failed-login count. Both are logged as summary lines (`limit_evicted_summary`, `limit_table_full_summary`). Size the table above the number of addresses you expect in a minute or so.

## The decision log

One JSON line per request:

```json
{"time":"2026-10-21T15:04:05.123Z","event":"denied","request":12,"rule":"admin-office","status":401,"method":"GET","path":"/admin/users","address":"203.0.113.7","reason":"no credential"}
```

- `event` is `allowed`, `denied` (a rule or the default), `rejected` (could be read two ways, or too big or too slow), `limited` (a rate limit), `refused` (a connection over a limit) or `upstream_error`.
- In a dry run, lines for requests that went through anyway carry `"dry_run": true`.
- Query strings appear as `query_keys`, the names only. Credentials, cookies and query values are never logged. Paths are, so don't put secrets in them.
- Past 20 lines other than `allowed` in one second, the rest are counted and logged once a second as a summary line per event, such as `{"event":"denied_summary","count":4812}`, so a flood can't fill the disk.

## Fixture file

```yaml
cases:
  - name: the admin pages need a login
    request: {method: GET, path: /admin/users}
    expect: {decision: denied, status: 401, rule: admin-office}

  - name: guessing passwords stops after five tries
    request:
      method: GET
      path: /admin/users
      from: 192.168.1.66
      headers: {Authorization: Basic YWRtaW46Z3Vlc3M=}
    times: 6
    expect: {decision: limited, status: 429}
```

Each case has a `request` (`method`, `path` with any query, `headers`, `from`, the client address, `127.0.0.1` by default, and `body`) and an `expect` with `decision` and optionally `status`, `rule` and `client` (`-` means none). `times` sends the request that many times and checks the last answer. Cases run in order against one set of limits, so a case can count on what earlier ones used up. Use test credentials in fixtures, never real ones.

## The example

[`examples/legacy-admin`](../examples/legacy-admin) is an old admin panel with no login, a debug page that prints a password, and version headers. Its `guard.yaml` opens the status check to everyone, hides the debug page, lets the billing system read reports with an API key, and keeps the admin pages for the admin login from the office network. Its tests show the app before and after.
