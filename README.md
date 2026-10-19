# Aftermarket

Tools that repair, adapt, test and preserve software you can't change.

Aftermarket is one program, `aftermarket`, with a subcommand per tool. The first tool is **AfterPatch** (`aftermarket patch`): a small reverse proxy that sits between a client and the service it calls, and fixes the traffic with rules you can read. Rename a JSON field. Move an endpoint. Add or drop a header. The client and the service stay exactly as they are.

It's a single Go binary with no dependencies, licensed under Apache 2.0. Website: [aftermarket.dev](https://aftermarket.dev/).

## Why you'd want it

Say an old client calls `GET /api/v1/customer?id=7` and expects this:

```json
{"customer_id": 7, "name": "Ada Lovelace", "email": "ada@example.com"}
```

The service it used to call has been replaced. The new one answers `GET /api/v2/customers/7` with this:

```json
{"customer": {"id": 7, "full_name": "Ada Lovelace", "email": "ada@example.com", "customer_since": "2019-03-01"}}
```

You can't rebuild the client and you can't change the new service. Two rules fix it:

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

Point the old client at AfterPatch instead of the service, and it works again. This exact case is in [`examples/legacy-customer`](examples/legacy-customer), and the test suite checks that the client fails without the rules and works with them.

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

From a copy of this repository, start the example's new service, then AfterPatch in front of it:

```sh
go run ./examples/legacy-customer/service -listen 127.0.0.1:9000 &
aftermarket patch run --config examples/legacy-customer/rules.yaml \
  --upstream http://127.0.0.1:9000 --listen 127.0.0.1:8080
```

Now run the old client against each of them:

```sh
go run ./examples/legacy-customer/client -base http://127.0.0.1:9000   # fails
go run ./examples/legacy-customer/client -base http://127.0.0.1:8080   # works
```

Every change AfterPatch makes is written to the audit log (stderr by default), one JSON line each:

```json
{"time":"...","event":"changed","request":1,"rule":"v1-customer-path","phase":"request","method":"GET","path":"/api/v1/customer","query_keys":["id"],"actions":["rewrite path"]}
```

## Commands

```text
aftermarket patch run   --config rules.yaml --upstream URL [--listen ADDR] [--dry-run]
                        [--audit-log FILE|-] [--health-path PATH]
aftermarket patch check --config rules.yaml
aftermarket patch test  --config rules.yaml --fixture fixture.yaml
aftermarket version
```

- **`patch run`** starts the proxy. `--listen` defaults to `127.0.0.1:8080`. `--audit-log -` (the default) writes to stderr, a file name appends to that file, and an empty value turns the log off.
- **`--dry-run`** forwards every request and response untouched and logs what each rule *would* have changed. Use it to try new rules on live traffic.
- **`patch check`** validates a rule file, lists the rules, and points out rules that can match the same traffic and change the same thing. That's allowed (rules run in file order), but it's worth knowing.
- **`patch test`** runs saved requests and responses through your rules without a real upstream and compares the results with what you expect. See [`examples/legacy-customer/fixture.yaml`](examples/legacy-customer/fixture.yaml). It exits 1 if any case fails, so it fits in CI.
- The proxy answers `GET /_aftermarket/healthz` itself with `200 ok`. It sits under `/_aftermarket/` so it never hides an upstream `/healthz`. Change it with `--health-path`, or pass an empty value to turn it off.

## How rules work

A rule file has a list of rules. Each rule has an `id`, a `when:` block that picks the traffic, and `request:` and/or `response:` actions. The full format is in [docs/reference.md](docs/reference.md).

- `when:` can check `method`, `path` (a glob: `*` within one segment, `**` across segments), `query`, `headers`, `status` and `content_type`. Every condition you write must hold. `when: {}` matches everything.
- `when:` always looks at the request **as the client sent it**, before any rewrite.
- Request actions: `set_headers`, `remove_headers`, `rewrite_path`, `json`.
- Response actions: `set_headers`, `remove_headers`, `json`.
- JSON operations: `rename`, `copy`, `set` and `delete`, using dot paths such as `customer.addresses.0.city`.
- Every rule that matches is applied, in file order. The same input always gives the same output.

## What AfterPatch promises

The main promise is that a rule repairs one thing and breaks nothing else. In practice:

- **Traffic no rule matches passes through byte for byte**, and it streams. AfterPatch doesn't buffer it or decompress it. The test suite checks this with hundreds of random requests and responses: binary bodies, odd paths, gzip, chunked and empty bodies, and every common status.
- **A body it can't safely change is passed on unchanged and logged as skipped.** That covers bodies that aren't valid JSON, JSON that wouldn't survive being rewritten (invalid UTF-8, duplicate keys), bodies over `max_body` (1 MiB by default), compression other than gzip, and responses that have no body. When a body is changed, every other field keeps its value and keys keep their order.
- **Gzip bodies are decoded, changed and compressed again**, and `Content-Length` is corrected. `ETag` and digest headers that described the old bytes are removed.
- **Path rules can't be tricked by `..`.** A rule with a `path` condition never matches a path with `.` or `..` segments or an encoded slash, and a path rewrite never produces one.
- **A dead upstream gets a clean `502` and a slow one a `504`** after `upstream_timeout` (30 seconds by default). That includes a body a rule is waiting for. Both are logged.
- **The audit log never contains values**: no header values, JSON values, query values or bodies, and upstream errors are described in fixed words. It records rule ids, method, path, query parameter names, status codes and the names of what changed.
- **Dry run changes zero bytes.** The tests check that too.

Recorded results, including the latency the proxy adds, are in [test/results](test/results).

## Limits in this version

- The upstream can be `http://` or `https://`. Connection upgrades such as WebSocket pass through, but rules don't look inside them.
- JSON is the only body format rules can change. Keys that contain dots can't be addressed by a dot path yet.
- Rule files use a strict subset of YAML: mappings, lists, quoted and plain values, one-line `[ ]` and `{ }` collections, `|` and `>` blocks, and comments. Anchors, aliases, tags and multiple documents are rejected with a line number.
- One upstream per proxy. Run one proxy per service.

## Building and testing

```sh
go test -race ./...     # the whole suite
go run ./tools/record   # run everything and write test/results
```

## Status and plans

0.1.0 is the first release. Next comes `aftermarket adapt` (AfterAPI) for bigger API translations, then `aftermarket record` and `replay` (AfterTest and AfterDiff). The plan is on [aftermarket.dev/roadmap](https://aftermarket.dev/roadmap/).

Questions and problems: open an [issue](https://github.com/aftermarket-dev/aftermarket/issues) or write to info@aftermarket.dev.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
