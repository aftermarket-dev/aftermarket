# AfterGuard threat model

AfterGuard is a security gateway for HTTP apps that can't protect themselves: an old admin panel, an internal API with no login, a vendor app with no rate limits. It sits in front of the app as a reverse proxy, run as `aftermarket guard`, and turns away requests that break the rules you write. The app stays exactly as it is.

This file says what AfterGuard defends against, what it doesn't, and how each promise will be tested. It was written before any code, as the plan requires, and approved with the 0.6.0 plan on Oct 21 2026.

## What it protects

- **The app behind it.** Its data, its admin functions, and its uptime.
- **Credentials.** The API keys and passwords AfterGuard checks for the app.
- **The logs.** They must be useful without holding secrets.

## Where it sits

```text
internet or internal network
          |
          v
  aftermarket guard     <- the only way in
          |
          v
  the old app           <- reachable from AfterGuard only
```

One assumption carries everything: **the app can only be reached through AfterGuard.** If the app also listens on a public address, every rule can be walked around. AfterGuard can't enforce that from inside, so the docs and `guard check` say it plainly, and the quick start binds the example app to 127.0.0.1.

## Who attacks it

- **Anonymous clients** anywhere on the network, sending any bytes they like.
- **Clients with a valid key** who try to reach more than they should, or more often.
- **Many clients at once**, from many addresses, trying to use up memory, connections or the app's capacity.

Out of scope: someone who can edit AfterGuard's config or its machine, and attacks on the network below HTTP (floods that fill the line before AfterGuard sees a request).

## Threats and answers

### 1. The gateway and the app read a request differently

This is the main risk for any gateway. AfterGuard decides on one reading of the request, and the app acts on another. Request smuggling (both `Content-Length` and `Transfer-Encoding`, two `Content-Length` headers), paths like `/admin/..%2f`, `//admin`, `/ADMIN`, `/admin;x`, `/admin%00` or `\admin`, and headers folded over two lines all exploit that gap.

**Answer: one reading, and nothing ambiguous goes through.**
- A request with conflicting framing, an encoded slash, dot or backslash in the path, a NUL, a non-canonical path (`..`, `//`, a trailing dot segment) or a header with bytes HTTP doesn't allow is rejected with 400. It is never cleaned up and passed on.
- AfterGuard forwards the request it judged, rebuilt from its own parse, so the app never sees bytes the rules didn't.
- Path rules match the decoded, canonical path, case-sensitive by default with an option for apps that ignore case.

### 2. A request reaches the app without the check it needed

**Answer: rules in order, and a default that fails closed.**
- Rules apply in the order written, and the first one that decides wins, as in AfterPatch.
- The default for a request no rule allows is `deny`. A config that sets the default to `allow` is accepted, but `guard check` warns about it.
- If anything inside AfterGuard fails while judging a request, the answer is 500 and the request is not forwarded.

### 3. Someone pretends to be someone else

**Answer: AfterGuard owns identity headers.**
- Credentials: API keys in a header or query key, and HTTP Basic. The config holds SHA-256 hashes of keys and passwords, never the plain values, and comparisons take the same time whatever the input.
- The credential is removed before the request goes on, unless the config says the app needs it.
- AfterGuard can tell the app who called, in a header such as `X-Aftermarket-Client: billing`. Any copy of that header from the client is removed first, so a client can't name itself.
- The client's address is the socket address. `X-Forwarded-For` is believed only from addresses listed as trusted proxies, and only the hop they added.

### 4. Guessing passwords, and too many requests

**Answer: limits that hold under load and can't be used to fill memory.**
- Rate limits per client address, per key, or per rule, as a token bucket (`60/m`, burst 10). Over the limit gives 429 with `Retry-After`.
- Failed logins have their own, stricter limit per address.
- The table of limits has a fixed size. IPv6 addresses are counted per /64, so an attacker with many addresses doesn't get many buckets. When the table is full, the oldest entries go first, and that is logged.
- Timeouts for reading headers and bodies (against slow-sending clients), caps on header size, body size, and connections in total and per address.

### 5. Requests the app was never meant to get

**Answer: plain allow lists, not attack signatures.**
- Rules match on method, path, query keys, headers, content type, client address (CIDR lists) and credential, using the same `when:` block as the other tools.
- A rule can also require a body under a size, or valid JSON.
- No WAF signatures for SQL injection or cross-site scripting. They give a false sense of safety and block real traffic. That is said in the docs.

### 6. The app's answers leak or can be misused by a browser

**Answer: response headers set by config.**
- Security headers can be added: `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Strict-Transport-Security` and `Content-Security-Policy`, each only when configured, since a wrong one can break the app.
- Headers that give away versions (`Server`, `X-Powered-By`) can be removed.
- AfterGuard's own error answers are short and say nothing about the rules.

### 7. The logs become a problem

**Answer: the same rules as the other tools.**
- One JSON line per decision: allowed or denied, which rule, the status, the client address, and the key's name, never the key.
- Credentials, cookies and secret-looking query keys are redacted, as in `record`.
- Denials under a flood are counted and summarised once a second instead of one line each, so an attacker can't fill the disk.

### 8. A rollout breaks the app

**Answer: try it before it blocks anything.**
- `--dry-run` forwards everything and logs what each request would have got.
- `guard check` validates the config, lists every problem, and warns about an `allow` default, rules that can never match, and rules hidden behind earlier ones.
- `guard test` runs fixture requests through the rules and compares the decisions with what you expect.

### 9. AfterGuard itself is the target

- No scripting in rules, no admin API, no plugins. The only endpoint of its own is the health check at `/_aftermarket/healthz`.
- Standard library only, as with the rest of Aftermarket.
- TLS: it can serve HTTPS with certificate and key files. Certificates are renewed outside AfterGuard; the docs show how to reload without dropping connections. When the app is called over HTTPS, its certificate is checked.

## Not in the first version

- Single sign-on (OAuth, OpenID Connect, SAML). It's a big surface, and a first version done badly would be worse than none.
- Attack signatures and bot detection.
- Limits shared between several AfterGuard instances.
- Looking inside WebSocket traffic; an upgrade is allowed or denied as a whole.
- Automatic certificates.
- Anything that fixes the app's own bugs in requests the rules allow.

## The promises and how they'll be tested

1. **Nothing ambiguous gets through.** A collection of smuggling and path tricks is sent over a raw socket. The test app records the exact bytes it receives. Every case must be rejected, or reach the app in exactly the form the rule judged.
2. **A denied request never reaches the app.** Random requests against random rule sets; the test keeps its own verdict for each, and the app's request count must match the allowed ones exactly.
3. **Allowed traffic passes byte for byte**, checked the same way as AfterPatch's random pass-through test.
4. **Limits hold.** Concurrent clients can't get past a limit by more than the burst. A client rotating through a million addresses leaves memory within the configured table size.
5. **A client can't choose its identity or address.** Spoofed identity and forwarding headers never reach the app.
6. **No secret reaches a log.** Keys, passwords and cookies are sent through every path and the log is searched for them.
7. **The legacy example:** an old admin panel open to everyone, then behind AfterGuard: anonymous requests denied, the right key allowed, a brute force slowed to the limit.

## Decisions

- **Single sign-on stays out of 0.6.0.** API keys and HTTP Basic only.
- **TLS from certificate files is optional.** AfterGuard serves plain HTTP by default, and HTTPS when given a certificate and key, so it can stand alone or behind a load balancer.
- **Default deny.** A request no rule allows is refused.

The 0.6.0 plan is in the repository's CLAUDE.md.
