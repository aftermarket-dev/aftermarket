# Aftermarket

Tools that repair, adapt, test and preserve software you can't change.

Aftermarket is one program, `aftermarket`, with a subcommand per tool. The tools sit between a client and the service it calls, or replay what passed between them. The client and the service stay exactly as they are.

- **AfterPatch** (`aftermarket patch`) fixes traffic with small rules you can read. Rename a JSON field, add or drop a header, move a path.
- **AfterAPI** (`aftermarket adapt`) keeps an old client working after its API was replaced. You describe each old endpoint in terms of the new one, and adapt translates the request on the way in and rebuilds the old response on the way out.
- **AfterTest** (`aftermarket record`) saves real traffic as test fixtures, with credentials and any values you list blanked out.
- **AfterDiff** (`aftermarket replay`) sends those fixtures to two versions of a service and reports where the answers differ.
- **AfterConfig** (`aftermarket config`) changes a vendor's config file through an overlay, without ever editing the file, and stops when an upgrade changed something your overlay relies on.
- **AfterHook** (`aftermarket hook`) watches files, folders, HTTP endpoints and command output that change without telling anyone, and sends a signed webhook for each change.

It's a single Go binary with no dependencies, licensed under Apache 2.0. Website: [aftermarket.dev](https://aftermarket.dev/).

## Which tool?

Use **patch** when one thing is off: a renamed field, a missing header, an endpoint that moved but otherwise answers the same way. Use **adapt** when a whole API changed shape, and you'd rather write down what the old one looked like than stack up a dozen small edits. Use **record** and **replay** to find out what changed in the first place, and to prove a repair works on real traffic. Use **config** when the thing you can't change is a vendor's config file that every upgrade overwrites. Use **hook** when software changes data but never says so, and something else needs to know.

## AfterPatch in one example

An old client calls `GET /api/v1/customer?id=7` and expects `{"customer_id": 7, "name": ..., "email": ...}`. The service it used to call was replaced by one that answers `GET /api/v2/customers/7` with `{"customer": {"id": 7, "full_name": ..., "email": ..., "customer_since": ...}}`. Two rules fix it:

```yaml
version: 1
rules:
  - id: v1-customer-path
    when:
      method: GET
      path: /api/v1/customer
      query: {id: "*"}
    request:
      rewrite_path:
        from: "^/api/v1/customer$"
        to: "/api/v2/customers/{query.id}"

  - id: v1-customer-shape
    when:
      method: GET
      path: /api/v1/customer
      status: 200
      content_type: application/json
    response:
      json:
        - rename: {from: customer.id, to: customer_id}
        - rename: {from: customer.full_name, to: name}
        - rename: {from: customer.email, to: email}
        - delete: {path: customer}
```

This exact case is in [`examples/legacy-customer`](examples/legacy-customer), and the tests check that the client fails without the rules and works with them. The full rule format is in [docs/reference.md](docs/reference.md).

## AfterAPI in one example

Now the whole customer API moved to v2. Lists come back wrapped in `{"data": [...]}`, creating a customer answers `201` where v1 answered `200`, deleting answers `204` with no body, and errors arrive as `{"error": {"code": ..., "message": ...}}` where v1 sent `{"message": ...}`. One mapping per old endpoint:

```yaml
version: 1
endpoints:
  - id: get-customer
    old: GET /api/v1/customer?id={id}
    new: GET /api/v2/customers/{id}
    response:
      body:
        customer_id: $.customer.id
        name: $.customer.full_name
        email: $.customer.email
      error:
        message: $.error.message

  - id: list-customers
    old: GET /api/v1/customers
    new: GET /api/v2/customers
    response:
      body:
        $each: $.data
        $item:
          customer_id: $.id
          name: $.full_name

  - id: create-customer
    old: POST /api/v1/customers
    new: POST /api/v2/customers
    request:
      body:
        full_name: $.name
        email: $.email
    response:
      status: {201: 200, 422: 400}
      body:
        customer_id: $.customer.id
        name: $.customer.full_name
        email: $.customer.email
      error:
        message: $.error.message

  - id: delete-customer
    old: DELETE /api/v1/customer?id={id}
    new: DELETE /api/v2/customers/{id}
    response:
      status: {204: 200}
      body: {deleted: true}
      error:
        message: $.error.message
```

`{id}` captures a value from the old URL and puts it in the new one. In a body template, `$.customer.id` takes a value from the source body, `$params.id` takes a captured parameter, `$each` and `$item` turn one list into another, and anything else is copied as written. Fields the template doesn't name are left out, so the old client gets exactly the shape it expects.

The full example, with a search endpoint as well, is [`examples/customer-api`](examples/customer-api). It has the old v1 service, the new v2 service and the old client's test suite: twelve checks of fields, status codes and error messages. The suite passes against v1, fails every check against v2 directly, and passes every check against v2 through adapt. The full mapping format is in [docs/adapt.md](docs/adapt.md).

## AfterTest and AfterDiff in one example

Put record in front of the old service and run the old client's tests through it:

```sh
aftermarket record --upstream http://127.0.0.1:9001 --out recorded.yaml --listen 127.0.0.1:8090
```

Every request and answer passes through unchanged, and each finished exchange is written to `recorded.yaml` as a fixture case. `Authorization`, cookies and credential query keys such as `api_key` are saved as `REDACTED`; a config file can add more headers, query keys and JSON fields. Then send the recording to the old service and the new one:

```sh
aftermarket replay --fixture recorded.yaml --a http://127.0.0.1:9001 --b http://127.0.0.1:8080
```

replay compares status codes, `Content-Type` and bodies. JSON is compared by meaning, so key order and number formatting don't count, and each difference is listed by path:

```text
### 1 GET /api/v1/customer

`GET /api/v1/customer?id=7`

- status: 200 vs 404
- customer_id: only in A (7)
- error: only in B (an object)
```

By default replay sends only GET and HEAD requests, so it can't change data; `--include-writes` sends the rest. It exits 1 when anything differs, so it fits in CI. In the customer-api example, the old client's twelve checks are recorded against v1. Replayed against v2 directly, all twelve differ. Replayed against v2 through adapt, all twelve answers are the same. The full format is in [docs/record.md](docs/record.md).

## AfterConfig in one example

A vendor ships `config.json`, and every upgrade overwrites it. Your changes go in an overlay instead, each guarded by the value it expects to find:

```yaml
version: 1
base: 1.0/config.json
changes:
  - set: {path: server.timeout, value: 120, expect: 30}
  - set: {path: telemetry.enabled, value: false, expect: true}
  - set: {path: database.password, value: from-our-vault}
  - set: {path: server.max_body, value: 10MiB, expect_absent: true}
```

```sh
aftermarket config diff  --overlay overlay.yaml                     # field by field, passwords REDACTED
aftermarket config apply --overlay overlay.yaml --out /etc/app/config.json
```

apply writes the result to a separate file, in one step, and never touches the vendor's file. Fields the overlay doesn't name keep their place and exact value. When version 1.1 changes the default timeout to 60, apply stops instead of quietly overriding it:

```text
line 8: set expects server.timeout to be 30, but the base has 60
nothing was written
```

It reads JSON, YAML and `.env` files. The example is [`examples/vendor-app`](examples/vendor-app), and the full format is in [docs/config.md](docs/config.md).

## AfterHook in one example

A legacy app rewrites `status.json` when an order moves on, drops CSV exports in a folder, and keeps its orders in SQLite. It tells nobody. One config watches all three:

```yaml
version: 1
state: hook-state.json
webhooks:
  - id: ops
    url: https://hooks.example.com/aftermarket
    secret_env: HOOK_SECRET
sources:
  - id: order-status
    file: {path: status.json}
    ignore: [updated_at]
  - id: exports
    folder: {path: exports, pattern: "*.csv"}
  - id: orders-table
    command:
      run: [sqlite3, -json, app.db, "select id, status, total from orders order by id"]
```

```sh
HOOK_SECRET=... aftermarket hook run --config hook.yaml
```

Each change becomes one signed POST, listed by field:

```json
{"id":"35e1242054585c9525bf477f","source":"orders-table","kind":"command","sequence":1,"time":"2026-10-21T12:25:52Z",
 "changes":[{"op":"changed","path":"0.status","old":"open","new":"paid"},
            {"op":"added","path":"1","new":{"id":1002,"status":"open","total":12}}]}
```

The first check only sets a baseline. A file rewritten with the same content, or a change to an ignored field, sends nothing, and a file still being written waits for the next check. While the receiver is down, events wait in the state file and are retried until delivered, in order, across restarts. The example is [`examples/legacy-app`](examples/legacy-app), with a receiver that checks signatures, and the full format is in [docs/hook.md](docs/hook.md).

## Install

Download the archive for your system from the [latest release](https://github.com/aftermarket-dev/aftermarket/releases/latest), unpack it and put `aftermarket` on your PATH.

| System | Archive |
| --- | --- |
| Linux, x86-64 | [aftermarket_linux_amd64.tar.gz](https://github.com/aftermarket-dev/aftermarket/releases/latest/download/aftermarket_linux_amd64.tar.gz) |
| Linux, ARM64 | [aftermarket_linux_arm64.tar.gz](https://github.com/aftermarket-dev/aftermarket/releases/latest/download/aftermarket_linux_arm64.tar.gz) |
| macOS, Apple silicon | [aftermarket_darwin_arm64.tar.gz](https://github.com/aftermarket-dev/aftermarket/releases/latest/download/aftermarket_darwin_arm64.tar.gz) |
| macOS, Intel | [aftermarket_darwin_amd64.tar.gz](https://github.com/aftermarket-dev/aftermarket/releases/latest/download/aftermarket_darwin_amd64.tar.gz) |
| Windows, x86-64 | [aftermarket_windows_amd64.zip](https://github.com/aftermarket-dev/aftermarket/releases/latest/download/aftermarket_windows_amd64.zip) |

Each release also has a `SHA256SUMS` file. Or build from source with Go 1.24 or newer:

```sh
go install github.com/aftermarket-dev/aftermarket/cmd/aftermarket@latest
```

## Quick start

From a copy of this repository, start the new v2 service, then adapt in front of it:

```sh
go run ./examples/customer-api/v2 -listen 127.0.0.1:9002 &
aftermarket adapt run --config examples/customer-api/adapt.yaml \
  --upstream http://127.0.0.1:9002 --listen 127.0.0.1:8080
```

Now run the old client's test suite against each of them:

```sh
go run ./examples/customer-api/client -base http://127.0.0.1:9002   # 12 checks, 12 failed
go run ./examples/customer-api/client -base http://127.0.0.1:8080   # 12 checks, 0 failed
```

The suite creates and deletes a customer, so restart the v2 service before running it again. The patch example works the same way with `examples/legacy-customer`.

Every change either tool makes is written to the audit log (stderr by default), one JSON line each:

```json
{"time":"...","event":"changed","request":1,"rule":"get-customer","phase":"response","method":"GET","path":"/api/v1/customer","query_keys":["id"],"status":200,"actions":["rebuild body"]}
```

## Commands

```text
aftermarket patch run   --config rules.yaml --upstream URL [--listen ADDR] [--dry-run]
                        [--audit-log FILE|-] [--health-path PATH]
aftermarket patch check --config rules.yaml
aftermarket patch test  --config rules.yaml --fixture fixture.yaml

aftermarket adapt run   --config adapt.yaml --upstream URL [same options as patch run]
aftermarket adapt check --config adapt.yaml
aftermarket adapt test  --config adapt.yaml --fixture fixture.yaml

aftermarket record --upstream URL --out fixture.yaml [--config record.yaml] [--listen ADDR]
aftermarket record check --config record.yaml
aftermarket replay --fixture fixture.yaml --a URL [--b URL] [--include-writes]
                   [--ignore PATH] [--header NAME] [--set-header "Name: value"]
                   [--report FILE] [--json FILE]

aftermarket config check --overlay overlay.yaml [--base FILE]
aftermarket config diff  --overlay overlay.yaml [--base FILE]
aftermarket config apply --overlay overlay.yaml --out FILE [--base FILE]
aftermarket config test  --overlay overlay.yaml --fixture fixture.yaml

aftermarket hook check --config hook.yaml
aftermarket hook run   --config hook.yaml [--once] [--dry-run] [--log FILE|-]
aftermarket hook test  --config hook.yaml --fixture fixture.yaml

aftermarket version
```

- **`run`** starts the proxy. `--listen` defaults to `127.0.0.1:8080`. `--audit-log -` (the default) writes to stderr, a file name appends to that file, and an empty value turns the log off.
- **`--dry-run`** forwards every request and response untouched and logs what would have changed. Use it to try new rules or mappings on live traffic.
- **`check`** validates the file, lists what's in it, and points out entries that can match the same traffic. For patch that's allowed: rules run in file order. For adapt the first matching endpoint wins, so a later one may never be reached for those requests.
- **`test`** runs saved requests and responses through the proxy against a stand-in upstream and compares the results with what you expect. All the tools use the same fixture format, so a recording from `record` can be run with `patch test` or `adapt test`. It exits 1 if any case fails, so it fits in CI.
- **`record`** never overwrites a file: `--out` must not exist yet. The file is a valid fixture after every case, so stop it with Ctrl-C whenever you have enough.
- **`replay`** without `--b` compares one service with the answers in the recording.
- The proxy answers `GET /_aftermarket/healthz` itself with `200 ok`. It sits under `/_aftermarket/` so it never hides an upstream `/healthz`. Change it with `--health-path`, or pass an empty value to turn it off.

## What the tools promise

The main promise is that a repair changes what it's meant to change and breaks nothing else. In practice:

- **Traffic nothing matches passes through byte for byte**, and it streams. Neither tool buffers it or decompresses it. The tests check this with hundreds of random requests and responses: binary bodies, odd paths, gzip, chunked and empty bodies, and every common status. For adapt that includes near misses, such as a v1 path with the wrong method or without its query parameter.
- **Anything that can't be changed safely is passed on unchanged and logged as skipped.** That covers bodies that aren't valid JSON, JSON that wouldn't survive being rewritten (invalid UTF-8, duplicate keys), bodies over `max_body` (1 MiB by default), compression other than gzip, and responses that have no body.
- **An adapt mapping applies fully or not at all.** If the request can't be translated, it goes on exactly as the client sent it. If the response can't be rebuilt, it comes back exactly as the upstream sent it, status and headers included. Nothing is half translated.
- **Gzip bodies are decoded, changed and compressed again**, and `Content-Length` is corrected. `ETag` and digest headers that described the old bytes are removed.
- **Paths can't be tricked by `..`.** A rule with a `path` condition, or an adapt endpoint, never matches a path with `.` or `..` segments or an encoded slash. A value that goes into a new path must stay inside one segment: `..`, `.` and anything with a slash are refused.
- **A dead upstream gets a clean `502` and a slow one a `504`** after `upstream_timeout` (30 seconds by default). That includes a body the proxy is waiting for. A client that stalls while sending a body the proxy needs gets `408` after the same time. All three are logged.
- **The audit log never contains values**: no header values, JSON values, query values or bodies, and upstream errors are described in fixed words. AfterPatch logs the path as the client sent it; adapt logs the matched endpoint's pattern, such as `/api/v1/customers/{id}`, so path parameters never appear. Credentials (`Authorization`, `Cookie`, `Set-Cookie` and their proxy cousins) are never changed by adapt.
- **Dry run changes zero bytes**, and never turns a slow answer into an error. The tests check both.
- **Recording changes zero bytes too**, and streams. Credential headers and query keys are always saved as `REDACTED`, and so are the headers, query keys and JSON fields you list; the tests send secrets both ways and search the file for them. Values in a URL path and in bodies that aren't JSON are saved as sent. An exchange that can't be stored safely (too big, not text, compressed in a way record can't read, or cut off) still passes through and is logged as skipped.
- **Replay doesn't change data unless you ask.** Only GET and HEAD are sent without `--include-writes`.
- **hook delivers every change at least once.** Each change gives exactly one event, nothing is lost while the receiver is down or across restarts, and a repeat after a crash carries the same id. Values under secret-looking keys are sent as `REDACTED`, and the signing secret never leaves the environment.
- **config never writes the vendor's file,** whatever path you give `--out`. A failed apply leaves the old output as it was, and applying twice gives the same bytes. Values under keys such as `password` or `token` are never printed.

Recorded results, including the latency each tool adds, are in [test/results](test/results).

## Limits in this version

- The upstream can be `http://` or `https://`. Connection upgrades such as WebSocket pass through, but nothing looks inside them.
- JSON is the only body format either tool can change. Keys that contain dots can't be addressed by a dot path yet.
- adapt maps one old call to one new call. An old endpoint that needs several new calls (fan-out) isn't supported yet.
- adapt doesn't rewrite `Location` headers or links inside bodies, so a redirect from the new API points at a new URL.
- Captured parameters are always strings. A template that needs a number from the URL gets `"7"`, not `7`.
- AfterHook checks on an interval instead of using the operating system's file notifications, so a change is seen within one interval (5 seconds by default). It watches SQLite through the `sqlite3` command, which must be installed.
- AfterConfig doesn't read TOML yet, and doesn't keep comments when it writes YAML. The vendor's file keeps them.
- Config files use a strict subset of YAML: mappings, lists, quoted and plain values, one-line `[ ]` and `{ }` collections, `|` and `>` blocks, and comments. Anchors, aliases, tags and multiple documents are rejected with a line number.
- One upstream per proxy. Run one proxy per service.
- record redacts headers, query values and JSON fields. Values in a URL path and in bodies that aren't JSON are stored as sent, so keep those out with `when:`. Binary bodies aren't recorded at all.

## Building and testing

```sh
go test -race ./...     # the whole suite
go run ./tools/record   # run everything and write test/results
```

## Status and plans

0.5.0 adds `aftermarket hook` (AfterHook). 0.4.0 added `aftermarket config` (AfterConfig), 0.3.0 added `aftermarket record` and `replay` (AfterTest and AfterDiff), and 0.2.0 added `aftermarket adapt` (AfterAPI). What comes next is decided one tool at a time; the plan is on [aftermarket.dev/roadmap](https://aftermarket.dev/roadmap/).

Questions and problems: open an [issue](https://github.com/aftermarket-dev/aftermarket/issues) or write to info@aftermarket.dev.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
