# AfterPatch reference

This is the full format of an AfterPatch rule file and fixture file. The [README](../README.md) explains what the tool is for.

## Rule file

```yaml
version: 1                 # optional; must be 1

limits:                    # optional
  max_body: 1MiB           # largest body a rule will read and change
  upstream_timeout: 30s    # how long to wait for the upstream's response headers

rules:
  - id: my-rule            # required, unique; letters, digits, . _ -
    description: text      # optional
    when: { ... }          # required; {} matches all traffic
    request: { ... }       # optional
    response: { ... }      # optional; a rule needs request, response or both
```

Unknown keys are errors, so a typo never fails silently. `aftermarket patch check` lists every problem in a file at once, with line numbers.

### limits

| Key | Default | Format |
| --- | --- | --- |
| `max_body` | `1MiB` | A byte count, or a number with `B`, `KB`, `MB`, `GB` (powers of 1000) or `KiB`, `MiB`, `GiB` (powers of 1024). At most `1GiB`. |
| `upstream_timeout` | `30s` | A Go duration such as `500ms`, `30s` or `2m`. |

A body bigger than `max_body`, compressed or not, is passed on unchanged and logged as skipped. When an upstream doesn't send response headers within `upstream_timeout`, the client gets `504`. The same happens when a response body that a rule needs to change doesn't arrive in full within `upstream_timeout` after the headers, except in a dry run, which waits as long as it takes. A request body may arrive slowly, but each part must come within `upstream_timeout` of the last, whether a rule reads it or it is streamed through. A client that stalls mid-body for longer gets `408` and its connection is closed. When the upstream answers, or fails, before the client has sent its whole body, the answer closes the connection.

### when

Every condition you write must hold. Conditions you leave out match anything.

| Key | Matches | Example |
| --- | --- | --- |
| `method` | One method or a list. Case-insensitive. | `GET` or `[GET, HEAD]` |
| `path` | The request path, as a glob. `*` matches within one segment, `**` matches across segments. Must start with `/`. | `/api/v1/customers/*` |
| `query` | Each named parameter must be present with a value matching its glob. `"*"` means any value. | `{id: "*", format: json}` |
| `headers` | Each named request header must have a value matching its glob. Names are case-insensitive. `Host` can't be matched. | `{User-Agent: "LegacyApp/*"}` |
| `status` | Response status codes or classes. | `200`, `[200, 201]`, `2xx` |
| `content_type` | The response media type, without parameters, as a glob. Case-insensitive. | `application/json`, `application/*json` |

In value globs, `*` matches any run of characters, including none.

`when` always looks at the request as the client sent it, before any rule rewrote it. A rule that uses `status` or `content_type` can only have `response:` actions, because those are only known once the response arrives.

A rule with a `path` condition never matches a request whose path has `.` or `..` segments or an encoded slash (`%2F`), such as `/public/../admin`. Upstream servers resolve those paths in different ways, so a glob can't say safely where they lead. Such requests still pass through, untouched by those rules.

### request actions

```yaml
request:
  set_headers: {X-Client: legacy, Accept: application/json}
  remove_headers: [X-Debug]
  rewrite_path:
    from: "^/api/v1/customer$"                  # Go regular expression, matched against the escaped path
    to: "/api/v2/customers/{query.id}"          # replacement; $1, ${1} and ${name} refer to groups
  json:
    - rename: {from: qty, to: quantity}
```

- `from` is matched against the path in its escaped form, as the client sent it (`%2F` stays `%2F`), and the result is forwarded in that form. Nothing is decoded twice.
- `to` can use `{query.NAME}`, which becomes the URL-escaped value of that query parameter. If the parameter is missing, or its value is `.`, `..` or contains a slash, the rewrite is not applied and the audit log says why.
- A rewrite whose result has `.` or `..` segments is not applied.
- `to` changes the path only, so it can't contain `?` or `#`. The query string is forwarded exactly as the client sent it.
- Every `$name` in `to` must be a group in `from`. Go reads `$1_detail` as a group called `1_detail`, so write `${1}_detail`, and `$$` for a literal `$`.
- Request `json` operations only run when the request `Content-Type` is JSON (`application/json` or `*/*+json`).
- If the upstream URL has a path (`--upstream http://host/base`), forwarded paths are joined onto it.

### response actions

```yaml
response:
  set_headers: {Cache-Control: no-store}
  remove_headers: [X-Powered-By]
  json:
    - delete: {path: internal}
```

Response `json` operations only run on JSON responses that have a body. That rules out `HEAD` requests and `1xx`, `204` and `304` responses.

### Headers

`set_headers` replaces every value of a header with one value. `remove_headers` deletes it. Header names must be valid HTTP tokens, and values can't contain control characters. The proxy manages these headers itself, so rules can't set or remove them: `Content-Length`, `Transfer-Encoding`, `Content-Encoding`, `Host`, `Connection`, `Keep-Alive`, `Proxy-Connection`, `Proxy-Authorization`, `Proxy-Authenticate`, `TE`, `Trailer` and `Upgrade`.

The proxy forwards `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto` and `Forwarded` as the client sent them and doesn't add its own. Rules can set or remove them like any other header. Headers that the client lists in its `Connection` header are never forwarded.

### JSON operations

Operations run in the order written. Paths are dot-separated. A number selects an array element: `items.0.price`.

| Operation | Form | Effect |
| --- | --- | --- |
| `rename` | `{from: a.b, to: c}` | Moves a value. Within the same object, the key keeps its position. |
| `copy` | `{from: a.b, to: c}` | Copies a value. The original stays. |
| `set` | `{path: a.b, value: 1}` | Sets a value and creates missing objects along the way. |
| `delete` | `{path: a.b}` | Removes a key or array element. |

`set` values are written in YAML. Unquoted `true` and `false` become JSON booleans, unquoted `null`, `Null`, `NULL` and `~` become null, and unquoted numbers in JSON's own format (`3`, `-12`, `2.50`, `1e3`) become JSON numbers with their exact text. A number too large for JSON readers, such as `1e400`, is an error. Everything else is a string, including `yes`, `True`, `0x1F` and `007`, and anything quoted. Lists and mappings become JSON arrays and objects.

An operation that can't apply, such as a `rename` whose source is missing, is skipped and listed under `not_applied` in the audit log. The rest of the rule still runs.

When at least one operation changes the body, it's written back as compact JSON. Other keys keep their order, values and numbers keep their original text. Strings keep their value, though an escape such as `\u00e9` may be written as the character itself. A gzip body is compressed again. `Content-Length` is set to the new length. `ETag`, `Content-MD5`, `Digest`, `Content-Digest` and `Repr-Digest` describe the old bytes, so they're removed, and so is a `Trailer` announcement.

A body that wouldn't survive this unchanged is not touched at all and is logged as skipped. That covers invalid UTF-8, duplicate keys and unpaired `\ud800`-style escapes.

### Order

Every rule that matches is applied, in file order. Later rules see the changes earlier rules made. `aftermarket patch check` points out pairs of rules that can match the same traffic and touch the same header, path or top-level JSON key.

## Audit log

One JSON object per line.

| Field | Meaning |
| --- | --- |
| `time` | UTC timestamp |
| `event` | `changed` (something changed), `would_change` (the same, in a dry run), `unchanged` (matched, nothing to do), `skipped` (matched, but nothing could be applied safely) or `upstream_error` |
| `request` | A number shared by all events for one request |
| `rule`, `phase` | Which rule, and whether it ran on the `request` or `response` |
| `method`, `path`, `query_keys`, `status` | What the traffic was. `path` is the path the client sent. |
| `actions` | What changed, such as `rename customer.id -> customer_id` or `set header X-Client` |
| `not_applied` | Operations that matched but had nothing to do, with the reason |
| `reason` | Why the JSON part of a rule was skipped, or a fixed description of the upstream error |

Values are never logged. When a rule changes headers but has to skip its JSON operations, the event is `changed`, the JSON operations are listed under `not_applied`, and `reason` says why.

## Fixture file

`aftermarket patch test` runs each case through the rules against a stand-in upstream that gives a fixed answer.

```yaml
cases:
  - name: old path reaches the new endpoint
    request:                       # what the client sends
      method: GET                  # default GET
      path: /api/v1/customer?id=7  # path with query string
      headers: {Accept: application/json}
      body: '...'
    upstream:                      # what the stand-in upstream answers
      status: 200                  # default 200
      headers: {Content-Type: application/json}
      body: '{"customer":{"id":7}}'
    expect:                        # at least one of these
      upstream_request:            # what the upstream should receive
        method: GET
        path: /api/v2/customers/7?id=7
        headers: {X-Client: legacy}
        body: '...'
      response:                    # what the client should get
        status: 200
        headers: {X-Removed: ""}   # empty value: the header must be absent
        body: '{"customer_id":7}'
```

Only the headers you list are checked. Bodies that parse as JSON are compared by meaning, so key order and spacing don't matter. Anything else is compared byte for byte. Use `|` blocks for long bodies.

## YAML subset

Rule and fixture files are read by a small built-in YAML reader, so Aftermarket has no third-party dependencies. It supports:

- block mappings and lists
- one-line `[ ]` and `{ }` collections (they may wrap onto following lines)
- plain, `'single'` and `"double"` quoted values, with escapes in double quotes
- `|` and `>` blocks, including the `-` and `+` variants
- `#` comments
- an optional leading `---`

Anchors, aliases, tags, merge keys, complex keys, tabs in indentation and multiple documents are rejected with a line number.
