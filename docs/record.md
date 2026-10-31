# AfterTest and AfterDiff reference

`aftermarket record` (AfterTest) saves real traffic as fixture cases. `aftermarket replay` (AfterDiff) sends those cases to two versions of a service and reports where the answers differ. `aftermarket shadow` (AfterDiff's live mode) does the same on live traffic: clients get the current service's answers while copies go to the new one. The [README](../README.md) explains what the tools are for, and [reference.md](reference.md#fixture-file) describes the fixture format that all the tools share.

## record

```text
aftermarket record --upstream URL --out fixture.yaml [--config record.yaml]
                   [--listen ADDR] [--audit-log FILE|-] [--health-path PATH]
aftermarket record check --config record.yaml
```

record is a proxy that changes nothing. Put it between a client and a service, and every request and answer passes through byte for byte, streaming as it arrives. When an exchange has finished, record writes it to `--out` as one fixture case.

- `--out` must not exist yet. record never overwrites or appends to a fixture.
- The file is valid after every case, so you can stop record at any time with Ctrl-C and use what it wrote.
- `--listen` defaults to `127.0.0.1:8080`. `--audit-log` and `--health-path` work as they do for patch.
- `record check` validates a config file and prints what it will record and redact.

### What a recorded case holds

```yaml
cases:
  - name: "1 GET /api/v1/customer"
    request:
      method: "GET"
      path: "/api/v1/customer?id=7"
      headers:
        "Authorization": "REDACTED"
        "User-Agent": "Go-http-client/1.1"
    upstream:
      status: 200
      headers:
        "Content-Type": "application/json"
      body: "{\"customer_id\":7,\"name\":\"Ada Lovelace\"}\n"
    expect:
      response:
        status: 200
        headers:
          "Content-Type": "application/json"
        body: "{\"customer_id\":7,\"name\":\"Ada Lovelace\"}\n"
```

The case name is a counter, the method and the path without its query string. The path keeps the query string as the client sent it. `upstream` is what the service answered, and `expect.response` is the same answer, so the case passes through a proxy that changes nothing.

Bodies are stored as text. A gzip body is stored decompressed, and `Content-Encoding` is left out so the case stays consistent. `Content-Length`, `Date`, `Host`, `Accept-Encoding` and connection headers aren't stored. A header with several values is stored as one value joined with `, `.

### What isn't recorded

These exchanges pass through untouched and are logged as `skipped`, with the reason:

- a request or response body over `max_body`, before or after decompression
- a body that isn't text (not valid UTF-8)
- compression other than gzip, or a gzip body that can't be decoded
- a body labelled JSON that can't be read while JSON fields are listed for redaction, since record can't check it for those values
- a header value or a path that isn't valid UTF-8, which a fixture can't hold exactly
- a body that was cut off, or a request body the upstream didn't read to the end
- an upstream that failed (the client gets the usual `502` or `504`)
- an upgraded connection, such as a WebSocket (`101 Switching Protocols`); the connection itself works as usual
- anything after `max_cases` cases

Traffic that doesn't match `when:` passes through without a log line. The fixture file starts with a comment naming the upstream by scheme, host and path only, so a password or query string in `--upstream` never reaches it.

### Config file

The config is optional. Without one, record saves all traffic and redacts credentials.

```yaml
version: 1                 # optional; must be 1

when:                      # optional; the same conditions as a patch rule
  method: GET
  path: /api/**
  status: 2xx

redact:                    # optional
  headers: [X-Request-Token]
  query: [api_key]
  json: [password, customer.email, data.*.email]

limits:                    # optional
  max_body: 1MiB
  upstream_timeout: 30s

max_cases: 1000            # optional; no limit by default
```

`when:` takes `method`, `path`, `query`, `headers`, `status` and `content_type`, exactly as in a [patch rule](reference.md). The request conditions decide whether record keeps a copy of the request; the response conditions are checked when the answer arrives.

### Redaction

Redaction changes only what goes into the fixture file. The client and the upstream always see the real values.

- **Credential headers are always redacted:** `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie` and `X-Api-Key`, in requests and in answers.
- **Credential query keys are always redacted:** `access_token`, `api_key`, `apikey`, `client_secret`, `password`, `sig`, `signature` and `token`.
- `redact.headers` adds more header names.
- `redact.query` adds more query keys. Their values become `REDACTED`, and the rest of the query string keeps its bytes: `?api_key=s3cret&page=2` is stored as `?api_key=REDACTED&page=2`. Both `&` and `;` count as separators, and encoded keys such as `api%5Fkey` are matched too. The same keys are redacted in the query string of a `Location` header.
- `redact.json` names JSON fields by dot path. Every request and response body that is valid JSON is checked, whatever its `Content-Type` says. `*` stands for any key or list element, so `data.*.email` covers every email in a `data` list. A number selects one list element. A path starts at the top of the body, so `password` doesn't reach into a list; write `*.password` for that. A redacted value becomes the string `"REDACTED"`, whatever it was. A body where nothing matched is stored exactly as sent.

Values in a URL path, such as `/users/ada@example.com`, and in bodies that aren't JSON, such as a form post, are not redacted. Keep those out with `when:`.

The audit log never contains values, as with the other tools.

## replay

```text
aftermarket replay --fixture fixture.yaml --a URL [--b URL] [--include-writes]
                   [--ignore PATH]... [--header NAME]... [--set-header "Name: value"]...
                   [--timeout 30s] [--report FILE] [--json FILE]
```

replay sends each case's request to `--a` and `--b` in turn, in file order, and compares the two answers. Without `--b`, it compares the answers from `--a` with the recorded ones in `expect.response`.

- **Only GET and HEAD are sent by default**, so a replay can't create, change or delete anything. Cases with other methods are listed as skipped. `--include-writes` sends them too; use it on test services you can reset.
- The request goes out with the recorded method, path, query string, headers and body. `Host`, `Content-Length`, `Accept-Encoding` and connection headers are left to the HTTP client.
- `--set-header "Authorization: Bearer ..."` sets a header on every request. Use it to put real credentials back in place of `REDACTED`. It can be given more than once.
- Redirects aren't followed; the redirect itself is compared.
- Each answer must arrive within `--timeout` (30 seconds by default) and be no larger than 16 MiB.

### How answers are compared

- **Status codes** must be the same.
- **Headers:** only `Content-Type` by default. `--header` replaces that list and can be given more than once. Against a recording, a header that wasn't recorded isn't compared.
- **JSON bodies** (both sides valid JSON) are compared by meaning. Key order and spacing don't count, and neither does the way a number is written: `1`, `1.0` and `1e0` are the same. Numbers are compared exactly, however many digits they have. Differences are listed by dot path, such as `customer.email: "a@x" vs "b@x"`, `name: only in A`, or `data: 3 items vs 2 items`. At most 20 are listed per case.
- `--ignore` leaves a dot path out of the comparison, with `*` for any key or element: `--ignore data.*.updated_at`. It can be given more than once. When you compare one service with a recording, redacted fields show up as differences (`"REDACTED" vs "the real value"`), so ignore those paths too.
- **Other bodies** are compared byte for byte, after gzip is undone. The report gives both lengths and where they first differ.

### Report

The Markdown report goes to stdout, or to `--report FILE` with only the summary line on stdout. `--json FILE` writes the same report as JSON. Every case has an outcome: `same`, `different`, `skipped` or `error` (a side couldn't be reached or answered too late).

replay exits 0 when every case it sent got the same answer from both sides, 1 when any differed or failed, and 2 for a usage error. A replay that compared nothing, because every case was skipped, also exits 1, since it proves nothing. A fixture file that can't be read exits 1, as with `patch test`. The report names services by scheme, host and path only. That makes it fit in CI.

## shadow

```text
aftermarket shadow --primary URL --candidate URL [--config record.yaml] [--listen ADDR]
                   [--report FILE] [--json FILE] [--sample F] [--include-writes]
                   [--queue N] [--workers N] [--candidate-timeout D]
                   [--ignore PATH]... [--header NAME]...
                   [--audit-log FILE|-] [--health-path PATH]
```

shadow is replay on live traffic. It is a proxy in front of the current service, the primary. Every request is served from the primary, and the client gets the primary's answer byte for byte, the same way record passes traffic through. Once that answer has gone out, a copy of the request is sent to the candidate, the new version, and the two answers are compared the way replay compares them. The differences go into a report that is rewritten as it grows.

The candidate can't slow down or break the primary path:

- **The copy is made after the primary's answer is complete**, never while the client waits. Handing it over is a send into a queue that never waits.
- **The queue is bounded** (`--queue`, 64 by default). When it is full, the copy is dropped and counted.
- **Workers send the copies** (`--workers`, 4 by default), with their own HTTP client and a timeout per answer (`--candidate-timeout`, 5 seconds by default). A candidate that is down, slow, hangs or fails only fills the report with errors.
- **`--sample 0.1`** copies one request in ten. The share is counted, not drawn at random, so 0.1 means exactly every tenth request.
- **Only GET and HEAD are copied by default**, as with replay. `--include-writes` copies the rest too. Point the candidate at its own data when you use it: a candidate sharing the primary's database would make every write twice. Copies run in parallel, so writes reach the candidate in order only with `--workers 1`.

The request goes to the candidate as the client sent it, real credentials included, since the candidate has to answer the same request. The candidate doesn't get the hop-by-hop headers the primary doesn't get either (`Connection`, `Proxy-Authorization` and the rest), and `Accept-Encoding` is left to the HTTP client, as in replay. A gzip answer from the primary is decoded before it is compared, without its `Content-Encoding` and `Content-Length`, the way replay's client sees it. So shadow and replay report the same differences for the same traffic; the tests check this on the customer-api suite and on random answers.

### Config

shadow reads the same config file as record (see above):

- `when:` picks the requests that are copied. The rest pass through and are counted as outside `when:`, including those whose answer doesn't match its `status` or `content_type`.
- `redact:` keeps values out of the report. Query values under listed keys (and the credential keys) read `REDACTED` in the paths. A compared header on the list, such as `Set-Cookie`, is reported as `header Set-Cookie: differs (REDACTED)`. A difference at a listed JSON path is reported as `password: differs (REDACTED)` or `only in A (REDACTED)`, without either value.
- `limits:` sets `max_body` (1 MiB by default): an exchange with a bigger request or answer is passed through and not copied. `upstream_timeout` applies to the primary, as in record.
- `max_cases` isn't used.

Exchanges that can't be compared safely are passed through and counted with a reason: bodies over `max_body`, a request body the primary didn't read to the end, an answer that was cut off, an upgraded connection, or a primary answer compressed with something other than gzip.

### Report

The report is replay's format, with A the primary and B the candidate: the Markdown file at `--report` (`shadow-report.md` by default) and, with `--json FILE`, the same report as JSON. A `shadow` block adds the counts: requests served, copied, outside `when:`, left out by sampling, writes not sent, dropped because the queue was full, not copied and why, and copies not compared because shadow stopped (still queued, or cut short while waiting for the candidate). Only cases with a difference or an error are listed, at most 1000; the counts keep going after that.

Each file is rewritten at most once a second while traffic comes in, in one step (a temporary file renamed over the old one), so a reader never sees half a report. On Ctrl-C or SIGTERM, shadow stops taking requests, waits up to the candidate timeout and a second for copies in the queue, counts the ones left as not compared at stop (never as candidate errors), writes the final report and prints the summary. It exits 0: differences are findings, not failures.

The audit log has record's events plus one `compared` event per copy, with `same`, `different` or the error as the reason. Like the other logs, it never holds values.

Memory stays bounded: at most `--queue` copies wait, each holding up to `max_body` of request and of answer.

## A typical run

1. Put record in front of the current service and run the client or its test suite through it.
2. Start the new version, or the old one behind `aftermarket adapt`.
3. Replay the recording against both and read the differences.
4. Before the switch, run shadow in front of the current service with the new one as the candidate, and let real traffic find what the recording didn't cover.

The [customer-api example](../examples/customer-api) does this in its tests. The old client's twelve checks are recorded against v1. Replayed against v1 and v2 through adapt, all twelve answers are the same. Against v1 and v2 directly, all twelve differ. The same suite run live through shadow, with v1 as the primary, gives the same result: twelve the same against v2 through adapt, and twelve differences against v2 directly, listed exactly as replay lists them. The old client passes all twelve checks either way, since it only ever sees v1's answers.
