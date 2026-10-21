# AfterAPI reference

The full format of an `aftermarket adapt` mapping file. The [README](../README.md) explains what the tool is for, and [reference.md](reference.md) covers AfterPatch rules and the fixture format both tools share.

## Mapping file

```yaml
version: 1                 # optional; must be 1

limits:                    # optional, same as AfterPatch
  max_body: 1MiB
  upstream_timeout: 30s

endpoints:
  - id: get-customer               # required, unique; letters, digits, . _ -
    description: text              # optional
    old: GET /api/v1/customer?id={id}
    new: GET /api/v2/customers/{id}
    when:                          # optional extra conditions
      headers: {User-Agent: "LegacyApp/*"}
    request:                       # optional
      body: { ... }                # template for the new request body
      headers: { ... }             # rename, set, remove
    response:                      # optional
      status: {201: 200}           # new code: old code
      body: { ... }                # template for 2xx responses
      error: { ... }               # template for 4xx and 5xx responses
      headers: { ... }             # rename, set, remove
```

Unknown keys are errors. `aftermarket adapt check` lists every problem in a file at once, in line order.

## Matching

An endpoint matches a request when the method is the same, every path segment matches, every query parameter in `old` is present and not empty, and every `when.headers` glob matches. The **first** matching endpoint in the file is used, and the others aren't looked at. `adapt check` points out endpoints that can match the same requests.

Requests that no endpoint matches pass through byte for byte.

### old

`METHOD /path?key={name}&key2={name2}`

- The method is in capitals and must match exactly. A `GET` endpoint doesn't match `HEAD`.
- A path segment is either literal text or a whole `{name}`, which captures one segment. `/customers/{id}/orders` matches `/customers/7/orders` and captures `id` as `7`. Parts of a segment, such as `/files/{name}.json`, aren't supported.
- The match is exact: `/customers` and `/customers/` are different paths.
- Each query part must capture a parameter, such as `id={id}`. The parameter must be present and not empty, or the endpoint doesn't match. If it appears more than once and everything else matches, the request passes through and the audit log says why.
- Captured values must be valid UTF-8. A request with a value that isn't passes through and is logged as skipped.
- Parameter names are letters, digits and `_`, and each can be captured once.
- A request whose path has `.` or `..` segments or an encoded slash (`%2F`) never matches an endpoint.

### new

`METHOD /path/{name}?key=value&key2={name}`

- The method may be different from the old one.
- `{name}` can appear anywhere in the path or in a query value, and must be a parameter `old` captures. Literal text is written as it should appear in the URL, already escaped.
- Values are escaped when they're put in: path escaping in the path, query escaping in the query.
- A value that goes into the path must stay inside one segment. If it's `.` or `..`, or contains `/` or `\`, the request passes through untouched and the audit log says why.
- The new query string is the one written in `new`, followed by every query part the client sent that `old` didn't capture, exactly as the client wrote it. So `?limit=2` on an old list call reaches the new one as `?limit=2`. Parts whose key `new` sets itself are dropped, so a client can't override a translated value.

## Templates

A template builds a JSON value. It's written in YAML, and plain values are copied as written:

- Unquoted `true` and `false` become JSON booleans.
- Unquoted `null`, `Null`, `NULL` and `~`, and an empty value, become null.
- Unquoted numbers written the way JSON writes them, such as `3`, `-12`, `2.50` and `1e3`, become JSON numbers and keep their exact text. A number too large for JSON readers, such as `1e400`, is an error.
- Everything else is a string. That includes `yes`, `True`, `0x1F`, `+1`, `.5` and `007`, and anything quoted.

Strings that start with `$` take values from somewhere else:

| Written | Means |
| --- | --- |
| `$` | the whole source body |
| `$.customer.id` | a value from the source body by dot path; a number selects a list element, as in `$.items.0` |
| `$params.id` | a parameter captured from the old URL, always as a string |
| `$$5.00` | the literal string `$5.00` |

The source body is the old request body for `request.body`, and the new response body for `response.body` and `response.error`.

A mapping with exactly the keys `$each` and `$item` builds a list. There's one `$item` for each element of the list `$each` points at, and inside `$item`, `$` means that element:

```yaml
body:
  $each: $.data
  $item:
    customer_id: $.id
    name: $.full_name
```

Lists can be nested. A key that starts with `$` must be written `$$` to mean a literal `$`.

### When a value is missing

- A field whose source is missing is left out of the object, and listed under `not_applied` in the audit log, such as `customer.email not found`. In a list, a missing value becomes `null`.
- If the whole template has nothing to build from, nothing is changed and the event is logged as skipped. That covers a top-level reference that's missing, or an `$each` whose source is missing or isn't a list.

## Request

`request.body` replaces the body of the new request.

- If the template reads the old body (`$` or `$.`), the old request must have a JSON `Content-Type` and a body that can be read safely: valid JSON, within `max_body`, identity or gzip encoded. Otherwise the whole request passes through untouched. A gzip body comes out gzipped again.
- A request body may arrive slowly, but each part must come within `upstream_timeout` of the last. A client that stalls mid-body gets `408` and its connection is closed; when the proxy needed the body, the event is logged as skipped.
- If the template doesn't read the old body, for example when it only uses `$params` and literals, it's built anyway, sent as `application/json` and not compressed.
- A `request.body` on an endpoint whose new method is `GET` or `HEAD` is an error.
- Without `request.body`, the old body is forwarded as it is, unless the new method is `GET` or `HEAD`. Then it's dropped, along with `Content-Type` and `Content-Encoding`.

## Response

- `response.status` maps codes from the new API to codes the old client expects, from 200 to 599. Mapping to `204` or `304` isn't allowed, because those can't carry a body.
- `response.body` applies to `2xx` responses from the new API, judged on the code before mapping. `response.error` applies to `4xx` and `5xx`. Other codes, such as redirects, only get the status and header changes.
- A template that reads the body needs a JSON response with a body. A template that doesn't, such as `{deleted: true}`, also works on a `204`, as long as `status` maps it to a code that has a body.
- A rebuilt body keeps the upstream's JSON `Content-Type`. If the upstream sent none, or something that isn't JSON, it becomes `application/json`. A gzip body is compressed again. `Content-Length` is set, and `ETag`, digest headers and `Trailer` are removed. Header actions come after this, so a mapping that sets `Content-Type` or `ETag` itself gets what it set.
- A status without a body (`204`) mapped to one that has a body (`200`) gets an empty body with `Content-Length: 0` when no template builds one.

### All or nothing

If a response body can't be rebuilt, the whole response comes back exactly as the upstream sent it, status and headers included, and the event is logged as `skipped` with the reason. The same goes for requests: a request that can't be translated is forwarded exactly as the client sent it. Nothing is half translated.

## Headers

```yaml
headers:
  rename: {X-Old-Trace: X-Trace}   # old name: new name; every value moves
  set: {X-Client: legacy}          # replaces every value with one
  remove: [X-Debug]
```

They apply in this order: rename, remove, set, after any body change. Names must be valid HTTP header names, and values can't contain control characters. Each header name may appear only once across `rename` (as either name), `set` and `remove`, so the order never matters.

A rename moves every value of the old header to the new name. If the new name is already present, the rename isn't applied, so values the other side sent are never overwritten, and the audit log says so.

Two kinds of header can't be renamed, set or removed:

- **Headers the proxy manages:** `Content-Length`, `Transfer-Encoding`, `Content-Encoding`, `Host`, `Connection`, `Keep-Alive`, `Proxy-Connection`, `TE`, `Trailer` and `Upgrade`.
- **Credentials:** `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `WWW-Authenticate` and `Proxy-Authenticate`.

Credentials always pass through exactly as the client and the upstream sent them.

## Audit log

The same format as AfterPatch (see [reference.md](reference.md#audit-log)), with these differences:

- `rule` is the endpoint id.
- `path` is the old endpoint's pattern with parameters shown as `{name}`, such as `/api/v1/customers/{id}/orders`, so parameter values never reach the log. That holds for every event of a matched request, including a skipped one followed by an upstream error, which share one request number.
- Traffic no endpoint matches only appears in the log when the upstream fails. Then `path` is the path the client sent, as in AfterPatch, since there are no parameters to hide.
- `actions` uses entries such as `method POST -> GET`, `rewrite path`, `rewrite query`, `rebuild body`, `rebuild error body`, `status 201 -> 200`, `rename header X-Old -> X-New` and `drop body (GET has none)`.

There's one event for the request side and one for the response side. A response side with nothing configured logs nothing.

## Dry run

`--dry-run` forwards every request and response exactly as sent and logs what each endpoint would have changed. To report that, it reads the response bodies a mapping would rebuild, with no time limit, so a slow upstream is never turned into a `504`.

## Testing a mapping

`aftermarket adapt test` uses the fixture format described in [reference.md](reference.md#fixture-file). See [examples/customer-api/fixture.yaml](../examples/customer-api/fixture.yaml).
