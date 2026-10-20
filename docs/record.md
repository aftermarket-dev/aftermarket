# AfterTest and AfterDiff reference

`aftermarket record` (AfterTest) saves real traffic as fixture cases. `aftermarket replay` (AfterDiff) sends those cases to two versions of a service and reports where the answers differ. The [README](../README.md) explains what the tools are for, and [reference.md](reference.md#fixture-file) describes the fixture format that all the tools share.

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
- a JSON body that can't be read while JSON fields are listed for redaction, since record can't check it for those values
- a body that was cut off, or a request body the upstream didn't read to the end
- an upstream that failed (the client gets the usual `502` or `504`)
- anything after `max_cases` cases

Traffic that doesn't match `when:` passes through without a log line.

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
- `redact.headers` adds more header names.
- `redact.query` names query keys. Their values become `REDACTED`, and the rest of the query string keeps its bytes: `?api_key=s3cret&page=2` is stored as `?api_key=REDACTED&page=2`.
- `redact.json` names JSON fields by dot path, in request and response bodies with a JSON `Content-Type`. `*` stands for any key or list element, so `data.*.email` covers every email in a `data` list. A number selects one list element. A redacted value becomes the string `"REDACTED"`, whatever it was. A body where nothing matched is stored exactly as sent.

Values in a URL path, such as `/users/ada@example.com`, and in bodies that aren't JSON are not redacted. Keep those out with `when:`.

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
- **JSON bodies** (both sides valid JSON) are compared by meaning. Key order and spacing don't count, and neither does the way a number is written: `1`, `1.0` and `1e0` are the same. Differences are listed by dot path, such as `customer.email: "a@x" vs "b@x"`, `name: only in A`, or `data: 3 items vs 2 items`. At most 20 are listed per case.
- `--ignore` leaves a dot path out of the comparison, with `*` for any key or element: `--ignore data.*.updated_at`. It can be given more than once.
- **Other bodies** are compared byte for byte, after gzip is undone. The report gives both lengths and where they first differ.

### Report

The Markdown report goes to stdout, or to `--report FILE` with only the summary line on stdout. `--json FILE` writes the same report as JSON. Every case has an outcome: `same`, `different`, `skipped` or `error` (a side couldn't be reached or answered too late).

replay exits 0 when every case it sent got the same answer from both sides, 1 when any differed or failed, and 2 for a usage error. That makes it fit in CI.

## A typical run

1. Put record in front of the current service and run the client or its test suite through it.
2. Start the new version, or the old one behind `aftermarket adapt`.
3. Replay the recording against both and read the differences.

The [customer-api example](../examples/customer-api) does this in its tests. The old client's twelve checks are recorded against v1. Replayed against v1 and v2 through adapt, all twelve answers are the same. Against v1 and v2 directly, all twelve differ.
