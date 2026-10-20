# Aftermarket

Tools that repair, adapt, test and preserve software you can't change.

Aftermarket is one program, `aftermarket`, with a subcommand per tool. Each tool is a small reverse proxy that sits between a client and the service it calls. The client and the service stay exactly as they are.

- **AfterPatch** (`aftermarket patch`) fixes traffic with small rules you can read. Rename a JSON field, add or drop a header, move a path.
- **AfterAPI** (`aftermarket adapt`) keeps an old client working after its API was replaced. You describe each old endpoint in terms of the new one, and adapt translates the request on the way in and rebuilds the old response on the way out.

It's a single Go binary with no dependencies, licensed under Apache 2.0. Website: [aftermarket.dev](https://aftermarket.dev/).

## Which tool?

Use **patch** when one thing is off: a renamed field, a missing header, an endpoint that moved but otherwise answers the same way. Use **adapt** when a whole API changed shape, and you'd rather write down what the old one looked like than stack up a dozen small edits.

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

aftermarket version
```

- **`run`** starts the proxy. `--listen` defaults to `127.0.0.1:8080`. `--audit-log -` (the default) writes to stderr, a file name appends to that file, and an empty value turns the log off.
- **`--dry-run`** forwards every request and response untouched and logs what would have changed. Use it to try new rules or mappings on live traffic.
- **`check`** validates the file, lists what's in it, and points out entries that can match the same traffic. For patch that's allowed: rules run in file order. For adapt the first matching endpoint wins, so a later one may never be reached for those requests.
- **`test`** runs saved requests and responses through the proxy against a stand-in upstream and compares the results with what you expect. Both tools use the same fixture format. It exits 1 if any case fails, so it fits in CI.
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

Recorded results, including the latency each tool adds, are in [test/results](test/results).

## Limits in this version

- The upstream can be `http://` or `https://`. Connection upgrades such as WebSocket pass through, but nothing looks inside them.
- JSON is the only body format either tool can change. Keys that contain dots can't be addressed by a dot path yet.
- adapt maps one old call to one new call. An old endpoint that needs several new calls (fan-out) isn't supported yet.
- adapt doesn't rewrite `Location` headers or links inside bodies, so a redirect from the new API points at a new URL.
- Captured parameters are always strings. A template that needs a number from the URL gets `"7"`, not `7`.
- Config files use a strict subset of YAML: mappings, lists, quoted and plain values, one-line `[ ]` and `{ }` collections, `|` and `>` blocks, and comments. Anchors, aliases, tags and multiple documents are rejected with a line number.
- One upstream per proxy. Run one proxy per service.

## Building and testing

```sh
go test -race ./...     # the whole suite
go run ./tools/record   # run everything and write test/results
```

## Status and plans

0.2.0 adds `aftermarket adapt` (AfterAPI). Next come `aftermarket record` and `replay` (AfterTest and AfterDiff), to capture real traffic and compare two versions of a service. The plan is on [aftermarket.dev/roadmap](https://aftermarket.dev/roadmap/).

Questions and problems: open an [issue](https://github.com/aftermarket-dev/aftermarket/issues) or write to info@aftermarket.dev.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
