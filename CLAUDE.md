# CLAUDE.md

This file says where the next session starts and what comes next. There is no separate roadmap file. Any change to the plan below is agreed with the owner before it goes in here.

## Project

Aftermarket is a family of developer tools that repair, adapt, test and preserve software after its release, without changing its source. It ships as one Go binary, `aftermarket`, with a subcommand per tool.

- Code: github.com/aftermarket-dev/aftermarket (this repo), default branch `main`
- Site: github.com/photozeitgeist/aftermarket.dev (Hugo, Launch theme), served at https://aftermarket.dev/
- Licence: Apache 2.0 (LICENSE, NOTICE, THIRD_PARTY_LICENSES.md)
- Contact on the site: info@aftermarket.dev

## Workflow (low maintenance)

The owner gives the go-ahead and creates repos. Everything else is done here: code, tests, docs, CI, releases, site and upkeep.

- README is the manual and the plain-language explainer. Reference docs are Markdown in `docs/`. No Word or PDF documents.
- Tests for every operation, plus failure tests aimed at the main promise. A script records results to `test/results/`. Every number on the site comes from those files.
- Before calling a release done: an independent review agent, fixes, then a fresh recording.
- CI on every push to main, Linux and macOS.
- Releases are automated. Raise the version in code on main. When CI finishes green on that commit, the release workflow sees an untagged version, builds Linux, macOS and Windows binaries, smoke-tests them, tags vX.Y.Z and publishes "Aftermarket vX.Y.Z" with the binaries, SHA256SUMS and notes. Archive names carry no version so releases/latest/download links stay valid. The workflow uses the repo's own GitHub token. Then watch the run, check the files and links, and run a downloaded binary. Never ask the owner to publish a release.
- Site: content and config only, theme untouched, no public/ folder. No em dashes and none of the owner's listed AI writing tells.

## Approved plan (Oct 19 2026)

### Decisions
- One `aftermarket` binary with subcommands. A tool gets its own binary only if it proves it needs one.
- Shared YAML `when:` match block across tools; actions are per tool.
- Standard library first. A YAML parser is the expected only dependency.
- Test fixture: `examples/legacy-customer`, an old client calling `/api/v1/customer` and a new service at `/api/v2/customers/{id}` with a different JSON shape. Used by end-to-end tests and the quick start.

### 0.1.0 AfterPatch (next)
- `aftermarket patch run --config rules.yaml --upstream URL --listen :8080`
- Match: method, path glob, query, header, status, content type
- Actions on request and response: set/remove headers, rewrite path, rename/copy/set/delete JSON fields by dot path
- `--dry-run`: forward untouched, log what would change
- Audit log as JSON lines with secrets redacted
- `aftermarket patch check` validates config and flags overlapping rules
- `aftermarket patch test` runs a fixture through the rules and compares with expected output
- Safety: body size cap with pass-through, gzip handling, streaming for unmatched traffic, upstream timeouts, `/healthz`
- Failure tests for "repairs one thing, breaks nothing else":
  - unmatched traffic byte-identical to a direct call (randomized)
  - malformed, oversize, compressed and chunked bodies pass unchanged and get logged
  - deterministic rule order; dry run changes zero bytes
  - upstream down or slow gives a clean 502 or 504
  - concurrency soak; added latency p50/p99 recorded to `test/results`
  - legacy client fails without the rule and passes with it
- Site at 0.1.0: quick start, download and reference pages; posts for the launch, a plain explainer of the software aftermarket, how it was tested, and the name; diagram redrawn from the real flow; badge and figures from `test/results`. Suggest adding Aftermarket to softdevstudio.com.

### 0.2.0 AfterAPI (details approved Oct 20 2026)
`aftermarket adapt`: one mapping per old endpoint, describing the old API in terms of the new one.
- Endpoint mapping: path and query parameters move into the new path; the method can change.
- Request templates build the new request body from the old one; response templates rebuild the old response shape from the new one (fields not named are dropped).
- Status and error mapping (e.g. the new `{"error": {...}}` into the old `{"message": ...}`).
- Header renames; no credential handling.
- Same safety rules as AfterPatch: dry run, audit log, `adapt check`, `adapt test` with fixtures, pass-through for anything no mapping covers.
- Promise test: an old client written against the v1 contract passes its full test suite against only the v2 service.
- Template syntax: `$.customer.id`-style paths (a tiny subset of JSONPath); nothing more planned.
- Out of scope: fan-out (one old call needing several new calls); kept for later.
- `adapt` stays a subcommand of the same binary; split it out only if the code shows a real reason.

### 0.3.0 AfterTest and AfterDiff (details left to Claude by the owner, Oct 20 2026)
`aftermarket record` captures traffic through the proxy into redacted fixtures. `aftermarket replay` sends fixtures to two upstreams and writes a semantic diff report.
- record is a pass-through proxy (changes zero bytes, streams) that writes each finished exchange as a case in the existing fixture format, so recordings run with `patch test` and `adapt test`.
- Optional record config: the shared `when:` block picks traffic; `redact:` lists extra headers, query keys and JSON dot paths (`*` for any element). Credential headers are always redacted. Redacted values read `REDACTED`.
- Exchanges that can't be stored safely (bodies over max_body, not valid UTF-8, compression other than gzip, JSON that can't be redacted, cut off) pass through and are logged as skipped.
- replay sends each case to two upstreams (or one, compared with the recorded response). Only GET and HEAD by default; `--include-writes` for the rest.
- Comparison: status, chosen headers (Content-Type by default), JSON bodies by meaning (key order and number formatting ignored, `--ignore` dot paths), other bodies byte for byte. Markdown and JSON reports; exit 1 on any difference.
- Promise tests: recording changes zero bytes; no credential or listed value reaches a fixture; the customer-api suite recorded against v1 replays with no difference against v2 through adapt, and with differences against v2 directly.

### 0.4.0 AfterConfig (approved Oct 21 2026)
`aftermarket config`: overlays that change a vendor's config file without ever editing it, so upgrades can't wipe the changes.
- Overlay file names the vendor (base) config and lists changes by dot path: set, delete, rename, copy (the same operations as AfterPatch JSON).
- Guards: a change can say what value it expects to find (`expect`, or `expect_absent`). If an upgrade changed it, apply stops and names the line. Guards are optional per change.
- Commands: `config check` (validate overlay against the current base, every problem listed), `config diff` (field by field, secret-looking keys shown as REDACTED), `config apply --out FILE` (atomic write), `config test` (fixtures).
- Formats: JSON, YAML (the subset internal/yamlite reads; comments are not kept in the output) and .env. TOML later.
- Output always goes to a separate `--out` file; apply refuses to write the base file itself.
- Promises: the base file is never written; applying twice gives the same bytes; a failed apply leaves the old output untouched; fields the overlay doesn't name keep their order and exact value.
- Example: a vendor app upgraded from 1.0 to 1.1, where the guard stops apply exactly where the vendor changed a default.
- No new dependencies.

### 0.5.0 AfterHook (approved Oct 21 2026; the three open choices left to Claude)
`aftermarket hook`: watch places that change but never tell anyone, and send webhooks.
- Sources: a file (field-level changes for JSON, YAML and .env), a folder (files added, changed, removed, optional name pattern), an HTTP endpoint polled (JSON by meaning), a command's output (SQLite through the `sqlite3` command, no driver).
- Watching by checking on an interval (default 5s); no OS notifications, no dependency.
- Delivery: signed JSON POSTs (HMAC-SHA256 with a secret from an environment variable), retries with growing waits until delivered; with max_attempts set, a dead-letter file after the last attempt (a fixed limit by default would have dropped events during a long outage). At least once: a state file keeps snapshots and the pending queue across restarts; event ids are derived from the change, so a repeat has the same id.
- First check sets the baseline silently. A half-written file is never reported (unreadable structured content or content that changes between two reads waits for the next check).
- Commands: `hook check`, `hook run [--once] [--dry-run]`, `hook test` with before/after fixtures. Secret-looking values are never printed or logged.
- Promises: every change gives exactly one event; nothing is lost while the receiver is down or across restarts; an unchanged source sends nothing; a half-written file is never reported.
- No new dependencies.

### 0.6.0 AfterGuard (approved Oct 21 2026; threat model in docs/guard-threat-model.md; the three open choices left to Claude)
`aftermarket guard`: an HTTP security gateway in front of apps that can't protect themselves.
- `guard run --config guard.yaml --upstream URL --listen :8080 [--dry-run]`, `guard check`, `guard test` with fixtures.
- Rules with the shared `when:` block, plus client address (CIDR) and credential; actions allow, deny (with status), and limit. Default deny; `check` warns on an allow default.
- Credentials: API keys and HTTP Basic, stored as SHA-256 hashes, compared in constant time; identity header set by AfterGuard, client copies removed; the credential removed before forwarding unless the config keeps it. `X-Forwarded-For` trusted only from listed proxies.
- Rate limits: token buckets per address (IPv6 per /64), key or rule; a stricter limit on failed logins; a fixed-size table.
- Ambiguous requests (conflicting framing, encoded slash/dot/backslash, NUL, non-canonical paths, bad header bytes) rejected with 400, never cleaned up; the forwarded request is rebuilt from AfterGuard's own parse.
- Response headers: security headers added and version headers removed, by config.
- Limits on header size, body size, slow clients and connections. Optional TLS from certificate and key files; plain HTTP by default.
- JSON-lines decision log with secrets redacted and denial floods summarised.
- Promises: nothing ambiguous gets through; a denied request never reaches the app; allowed traffic passes byte for byte; limits hold and memory stays bounded; a client can't choose its identity or address; no secret reaches a log. Example `examples/legacy-admin`.
- Not in 0.6.0: single sign-on, attack signatures, shared limits, WebSocket inspection, automatic certificates.
- No new dependencies.

### 0.7.0 TOML for AfterConfig (chosen by the owner Oct 21 2026; the "TOML later" of 0.4.0, details left to Claude)
- `aftermarket config` reads and writes TOML 1.0 files: tables, arrays of tables, dotted and quoted keys, inline tables, every string kind, integers in all bases, floats, booleans, dates and times. Invalid TOML is refused with a line number.
- Changes are made on the file's own lines, as for .env: comments, layout and every untouched line stay byte for byte; renames keep their line; dates keep their type. When a change can't be made on the lines, the file is written from its values, still exactly right but without comments.
- Same promises as 0.4.0; `config diff` shows lists item by item. Example `examples/vendor-app-toml`. No new dependencies.

### 0.8.0 AfterSchema (approved Oct 22 2026; the two open choices left to Claude: views for reads only, output to a file only)
`aftermarket schema`: SQLite compatibility, so old reports, scripts and integrations keep working after an app's schema changes.
- Reads databases and `.sql` schema dumps through the `sqlite3` program (as AfterHook does), read-only; no driver, no new dependencies. The sqlite3 code moves into one shared package.
- `schema diff --old A --new B`: tables, columns, types, NOT NULL, defaults, keys, indexes, foreign keys, views and triggers, each change marked breaking for old readers or safe; text or JSON; exit 1 on anything breaking.
- `schema compat --map map.yaml --out compat.sql`: compatibility views from a mapping file describing the old tables in terms of the new (renamed tables and columns, a column built from an expression, a fixed value for a dropped column); one deterministic SQL file, reviewed and applied by the user. Views serve reads only.
- `schema check --map map.yaml --db new.db --queries old.sql`: prepares each old query against the new schema plus the views without running it, and lists every one that would fail, with its line; queries can also come from a record fixture.
- `schema test`: fixtures pairing an old query, sample data and the expected rows.
- Promises: the database is never written; old queries get the same rows through the views as from the old schema; generated SQL is the same bytes every run; check lists every problem.
- Example `examples/legacy-shop` (v1 and v2 shop databases, a dozen old report queries that fail on v2 directly and pass on v2 with the views).
- Not in 0.8.0: writes through the views, an apply step, PostgreSQL and MySQL, guessed renames, scanning app code.
- Release smoke runs the schema commands where sqlite3 is present (Linux, macOS).

### 0.9.0 AfterBridge (approved Oct 22 2026; written out from the approved summary, details left to Claude)
`aftermarket bridge`: a read-only JSON API over an old app's CSV exports and SQLite files, for software that only hands its data over as files.
- Sources: a CSV file, or the newest file in a folder matching a pattern (exports by date); a SQLite table or a fixed query, read through the shared sqlitecli runner, read-only, in safe mode. No values from requests ever go into SQL.
- Each source is read into a snapshot held in memory, typed by declared columns (text, integer, number, boolean, date); it is read again when the file changes. A file that is half written, unreadable or over its limits is never served: the last good snapshot stays, and the problem is logged. A source never loaded answers 503.
- Endpoints in `bridge.yaml`: a list (`/orders`, with `limit` and `offset` and exact-match filters on declared fields) and one item by key (`/orders/{id}`, 404 when missing). Columns can be renamed and left out. GET and HEAD only; anything else 405; unknown query parameters 400.
- Commands: `bridge serve --config bridge.yaml [--listen ADDR]`, `bridge check` (config and sources, every problem listed), `bridge convert --source NAME --out FILE` (one typed snapshot written to JSON, atomically, never over an input), `bridge test` (the existing fixture format: request in, expected response out).
- Same plumbing as the other tools: health check at /_aftermarket/healthz, JSON-lines access log without query values, error answers with Content-Length.
- Promises: sources are never written; a half-written file is never served; every answer matches the snapshot it came from, values and types alike; requests can't reach SQL or files outside the config.
- Example `examples/legacy-exports`: an old app's daily orders CSV exports and its SQLite inventory, served as `/orders`, `/orders/{id}` and `/products/{sku}`.
- Not in 0.9.0: writes, authentication (put AfterGuard in front), joins across sources, XML or fixed-width files, other databases.
- No new dependencies.

### Later, decided with the owner at the time
Afterlife and AfterTrace stay research; Afterlife only ever replaces documented APIs, never licence checks or copy protection.

## Status

- Oct 19 2026: plan approved. Site cleaned and aligned with this plan. Repo initialised with LICENSE, NOTICE and this file.
- Oct 19 2026: 0.1.0 AfterPatch built. Independent review found 24 issues (query strings dropped, path rewrite traversal, header handling, YAML block scalars, release workflow gaps and more); all fixed, each with a regression test. Results recorded in test/results. Version raised to 0.1.0 for the automated release.
- Deviation from the plan, agreed in chat: no YAML library (Go modules could not be downloaded in the workspace); internal/yamlite reads the YAML subset the files need, so there are zero third-party dependencies.
- Health check lives at /_aftermarket/healthz (not /healthz) so it never hides an upstream path.
- Oct 19 2026: 0.1.0 released and verified (all archives, SHA256SUMS, latest links, downloaded binary run as a live proxy). Site published with quick start, download, reference and release posts. Aftermarket added to softdevstudio.com as the twenty-third project.

- Oct 20 2026: 0.2.0 AfterAPI built. Shared plumbing moved into internal/httpx and internal/fixture (patch unchanged). Independent review found no high-severity issue and 10 medium/low ones (audit leak on skipped-then-failed requests, dry run 504 on slow bodies, stalled request bodies, leftover query collisions, header rename overlaps, invalid UTF-8 params, repeated-parameter matching order, explicit headers overridden on rebuild, literal number typing, check not listing every problem) plus test gaps; all fixed with regression tests. Two of them also fixed in patch (dry run, stalled bodies). Version raised to 0.2.0 for the automated release.
- Oct 20 2026: 0.2.0 released and verified (all archives, SHA256SUMS, latest links, downloaded binary ran adapt check, adapt test and the live twelve-check client suite against v2). Site: AfterAPI reference page (/adapt-reference/), quick start part two, roadmap, concepts, release and testing posts, hero badge and figures from test/results. softdevstudio.com note published.

- Oct 20 2026: 0.3.0 AfterTest and AfterDiff built (internal/record, internal/replay, `record`, `record check`, `replay`). Independent review found 1 high issue (101 upgrades broke through record) and 10 medium/low ones (query redaction missed `;`, empty absolute-form paths broke the fixture, JSON without a JSON Content-Type wasn't redacted, upstream URL secrets in the file header and reports, max_cases skips not logged, wrong request id on upstream errors, non-UTF-8 headers stored altered, loose big-number comparison, a replay comparing nothing passed, a busy port left an empty fixture) plus test gaps; all fixed with regression tests. Credential query keys (access_token, api_key, apikey, client_secret, password, sig, signature, token) are now redacted by default. 272 tests recorded. Version raised to 0.3.0 for the automated release.

- Oct 20 2026: CI on ubuntu-latest (moving to Ubuntu 26 from Oct 19) failed in patch's random pass-through test, in the test's direct call to its own server, not through the proxy; the same seed passes locally. CI pinned to ubuntu-24.04, and CI now copies test failures into annotations, since job logs can't be downloaded from this workspace. Revisit the pin when Ubuntu 26 settles.
- Oct 20 2026: 0.3.0 released and verified (all archives, SHA256SUMS, latest links, downloaded binary ran record check and the live loop: the client suite recorded through record against v1, replayed 12 of 12 the same against v2 through adapt and 12 of 12 different against v2 directly). Site: AfterTest and AfterDiff reference page (/record-reference/), quick start part three, roadmap, concepts, release and testing posts, hero badge and figures from test/results. softdevstudio.com note published.

- Oct 20 2026: at the owner's request, aftermarket.dev got a Use cases page (/use-cases/, in the menu) and three posts tagged "case studies" (exchange rate endpoint with patch, customer API migration with adapt, release check with record and replay). They are worked cases on stand-in services, said so in each post; every command and output comes from a real run of the 0.3.0 binary, and every snippet passes check.

- Oct 21 2026: 0.4.0 AfterConfig built (internal/config, `config check|diff|apply|test`, examples/vendor-app, docs/config.md). Independent review found 1 high issue (rename into a child of itself deleted the value) and 7 medium ones (YAML strings with `:` written bare, YAML key types changed for other readers, config test failures printed secrets, secrets printed after rename/copy, missing secret words and URL passwords, .env rewrites expanding `$` and losing comments, docs overclaiming byte-for-byte JSON) plus low ones (CRLF, top-level list delete, deep nesting, release smoke gaps); all fixed with regression tests. Every output is now read back before it's written, and an overlay with nothing to change returns the base byte for byte. yamlite gained Pair.KeyQuoted. 296 tests recorded. This workspace machine is about twice as slow as before (direct baseline 57 µs vs 25-35 µs), so latency figures in test/results doubled with no proxy change; the site hero no longer shows a latency figure. Version raised to 0.4.0 for the automated release.

- Oct 21 2026: 0.4.0 released and verified (all archives, SHA256SUMS, latest links; the downloaded binary ran config check, diff, apply, the 1.1 stop with the old output unchanged, the refusal to write the base, and config test). Release smoke now covers apply, the 1.1 stop and the refusal on every platform. Site: AfterConfig reference page (/config-reference/), quick start part four, use case, roadmap, concepts, release, testing and case study posts; hero figures now 296 tests, 12 of 12, 2 of 7, 0 dependencies. softdevstudio.com note published.

- Oct 21 2026: 0.5.0 AfterHook built (internal/hook, `hook check|run|test`, examples/legacy-app with a signature-checking receiver, docs/hook.md). Independent review found 1 high issue (secrets sent in plain text when a comparison fell back to lines: JSON turning into an error page, sqlite3 printing nothing for no rows, text files) and 6 medium ones (a file gone for one check gave two events, truncated YAML and .env reported, one busy file blocked a whole folder, event ids repeated after a new baseline, a command's child holding stdout beat the timeout, `hook check` printed URL paths and queries) plus low ones (dry run repeating events, 50ms wakeups during backoff, Ctrl-C counting an attempt, a save per delivery, receiver data race, send_to duplicates, no "and N more" for fields); all fixed with regression tests. Removals and big shrinks now need two checks in a row. Release smoke now runs `hook run --once` twice. The recorder prints a failing test's output; the known patch random pass-through flake failed once more in a full race run here and passed on four reruns. 331 tests recorded. Version raised to 0.5.0 for the automated release.

- Oct 21 2026: 0.5.0 released and verified (all archives, SHA256SUMS, latest links; the downloaded binary ran hook check, hook test and a live loop with the example receiver and sqlite3: three signed events, a quiet run sending nothing, no token reaching the receiver, and an event held through a receiver outage and delivered after). That live run showed queued events went out indented after a restart (valid signature, different bytes); 0.5.1 compacts them on load, and a state file with a queued event missing its body is refused. Short independent review of the patch found nothing blocking. 333 tests recorded. Version raised to 0.5.1 for the automated release.

- Oct 21 2026: 0.5.1 released and verified (all archives, SHA256SUMS, latest links; the downloaded binary ran the example with sqlite3 and the receiver: three signed events, a quiet run, an event held through an outage and delivered compact). Site: AfterHook reference page (/hook-reference/), quick start part five, use case, roadmap, concepts, about, home, download and architecture pages, release, testing and case study posts; hero now 333 tests and "Six tools are out". softdevstudio.com note published.

- Oct 21 2026: 0.6.0 AfterGuard built (internal/guard, `guard run|check|test|hash`, examples/legacy-admin, docs/guard.md, docs/guard-threat-model.md). Go's server silently accepts Content-Length with Transfer-Encoding and folded headers, so a tracker on each connection reads raw request heads and follows bodies exactly as Go 1.24 does. Independent review found 4 high issues, all fail-open paths in that tracker (an Upgrade header with no 101 switched checks off, OPTIONS * answered by Go shifted verdicts, an oversized pipelined head got no verdict, loosely read chunk sizes hid a request as trailers), and 7 medium ones (h2c upgrades tunnelling past rules, X-Original-Url and other override headers forwarded, limit-table eviction resetting other limits and lockouts, IPv6 connection limits per address instead of /64, non-ASCII case folding under ignore_case, lower-case methods, underscore header spellings) plus low ones (summaries for every event, dangerous configs unwarned, two Authorization headers, HTTP/1.0 with Transfer-Encoding, no write timeout, cert reload race, failed-login check not atomic, docs claims); all fixed with regression tests. A request without a verdict is now rejected, upgrades need a rule that names them, and a full limit table refuses new keys instead of resetting live ones. 404 tests recorded. Open: the random pass-through tests in patch and record fail rarely on CI's Ubuntu 24.04 with "unexpected EOF" reading a chunked body (seen in a direct call and through record); 40 local loops are clean. Version raised to 0.6.0 for the automated release.

- Oct 21 2026: 0.6.0 released and verified (all archives, SHA256SUMS, latest links; the downloaded binary ran guard check, guard test and a live run in front of the example panel: 404 for the debug page, 401 without a login, 403 for billing on admin, 400 for a path trick and for Content-Length with Transfer-Encoding, X-Original-Url not passed on, version headers removed, the sixth wrong password 429 and the lockout clearing after a minute). Release smoke runs a live guard on every platform. Site: AfterGuard reference (/guard-reference/) and threat model (/guard-threat-model/) pages, quick start part six, use case, roadmap, concepts, about, home, download and architecture pages, release, testing and case study posts; hero now 404 tests and "Seven tools are out". softdevstudio.com note published.

- Oct 21 2026: 0.7.0 TOML for AfterConfig built (internal/config/toml.go, examples/vendor-app-toml, TOML section in docs/config.md). Independent review found no high issue and 6 medium ones (a change already in place still rewrote its line, secrets quoted in TOML error messages, a deeply nested file crashed the process, deleting a table took the comments above the next header, deleting the last dotted key rewrote the whole file, text set on a float or date could come out as an invalid bare date) plus low ones (invalid dates and overflowing floats accepted, a BOM refused, a final newline added on delete, a new root key above the file's opening comment, key order lost in the fallback writer, config test blind to TOML types); all fixed with regression tests, plus a test of 22 changes to a hand-written TOML file. TOML read-back and config test now compare types strictly. Also fixed outside TOML: ojson refuses JSON nested more than 10,000 levels, so a hostile body passes through patch, adapt and record instead of exhausting the stack. AfterHook already picked formats with config's FormatOf, so `.toml` files would have been watched by field while `format: toml` was refused; it's now a listed hook format, with docs and a test. The first 0.7.0 CI run was cancelled so the release carries that. 444 tests recorded. Version raised to 0.7.0 for the automated release.

- Oct 21 2026: 0.7.0 released and verified (all archives, SHA256SUMS, latest links; the downloaded binary ran config check, diff and apply on examples/vendor-app-toml with exactly the seven changed lines differing from the vendor's file, applying twice gave the same bytes, the 1.1 stop at lines 9 and 15 with the old output unchanged, the refusal to write the base, config test, an overlay already in place giving the base byte for byte with hex and underscores kept, the 1.1 file passing after the two overlay edits, and hook queuing a TOML field change with the password redacted). Site: AfterConfig reference regenerated from docs/config.md, quick start part four, use case, roadmap, concepts, architecture, home and download pages, release, testing and case study posts; hero now 444 tests with the badge "TOML for AfterConfig in 0.7.0". softdevstudio.com note published (row 2h). The merged site branch toml-0.7.0 couldn't be deleted from this workspace (branch deletes are blocked by the proxy); it is harmless.

- Oct 21 2026: at the owner's choice, the CI flake was tracked down. It was a real proxy bug, not a test one: the failing calls were always the ones through the proxy (the earlier notes saying "direct call" were wrong). When a request with a body got a fast answer, Go's server discarded and closed the client's body as the response headers went out, while the transport was still reading it to see its end; the transport then dropped the upstream connection mid-answer. It reproduced here under CPU load (about 1 in 100 runs). Fix: httpx.KeepRequestBody (ResponseController.EnableFullDuplex) right before each reverse proxy call in patch, adapt, record and guard; never before a handler answers by itself, which made a health check with a body drop the connection with a logged panic (found by the independent review, along with guard's exposure and a test gap). New deterministic tests: an upstream answering after reading all, half or none of the body, and a health check with a body on a reused connection. Also from the review: after a 408 for a stalled body, patch and adapt kept waiting on the client because the read deadline was cleared; it now stays, with a test. 390 load runs of the two random tests passed. Still open, older than this: on a 502 from a failed dial, the transport closes the client's body before the 502 goes out and can wait on a stalled client, since the server has no ReadTimeout. 452 tests recorded. Version raised to 0.7.1 for the automated release. The ubuntu-24.04 CI pin was most likely this same bug; moving back to ubuntu-latest is for the owner to decide.

- Oct 21 2026: 0.7.1 released and verified (all archives, SHA256SUMS, latest links). Live check with the downloaded binaries in front of an upstream that answers a 100 KB upload at once without reading it: 0.7.0 cut off 15 of 300 answers and logged 15 ReverseProxy copy errors; 0.7.1 cut off 0 of 300 with a clean log, and a health check with a body kept its connection for a second request. Site: post "Aftermarket Fixes Proxy Answers Cut Off Under Load", roadmap status 0.7.1, hero 452 tests. No softdevstudio.com note for this patch release; the next studio update can cover it. The merged site branch fix-0.7.1 is left on GitHub (branch deletes are blocked from this workspace).

- Oct 21 2026: at the owner's choice, CI moved back to ubuntu-latest; four runs green on Linux and macOS.
- Oct 22 2026: at the owner's choice, the stalled-upload case, built as 0.7.2. httpx.WatchBody wraps the client's live body once at the top of patch, adapt and record: every read must make progress within upstream_timeout (slow but moving uploads pass, a stall gets 408), the deadline is cleared once every byte has arrived and never moved after that, after a 101 or after the handler; the request is a copy, so Go's server keeps its own body checks; a read error sticks, so a cut-off body gets 400 and is never translated, drained or recorded as whole; an answer that starts before the body has ended closes the connection (set in ModifyResponse); the forwarded context outlives a half-close by upstream_timeout, so half-closed clients get their answer and a client that is gone frees the upstream call; adapt reads off bodies it replaces or drops, so those keep their connection; error answers carry Content-Length; done() closes the real body inside the handler. Four independent reviews: the first design brought back the empty-200 and connection-reuse traps in Go's server (deadlines during the background read, the server's own Request.Body replaced, bodies closed after the handler), the second found max_body+1, adapt keep-alive, half-close, 101 and Expect cases, the third a client that leaves no longer stopping the upstream call plus cut-off bodies, the fourth nothing blocking (two nits fixed, docs made exact). Every finding has a test that fails on the commit reviewed. Load runs clean, no panics logged. 474 tests recorded. Version raised to 0.7.2 for the automated release.

- Oct 22 2026: 0.7.2 released and verified (GitHub cancelled the Intel macOS smoke job for lack of runners; rerun, then published). All archives, SHA256SUMS, latest links; the downloaded binary ran live in front of a test upstream with upstream_timeout 500ms: a stalled upload got 408 at 503ms and its connection closed, a stalled upload to a dead upstream got 502 in 1ms, a byte every 100ms got through whole, a cut-off upload got 400, a half-closed client got the real answer, an ordinary POST kept its connection; audit reasons right, no panics. Site: post "Aftermarket Hardens Patch, Adapt and Record Against Stalled and Cut Off Uploads", roadmap status 0.7.2, hero 474 tests. Hugo leaves out future-dated posts, so a post dated by the owner's clock (UTC+3) ahead of UTC doesn't build; date posts by UTC. No softdevstudio.com note for this patch release.

- Oct 22 2026: 0.8.0 AfterSchema built (internal/sqlitecli shared runner with internal/proc moved out of hook, internal/schema, `schema diff|compat|check|test`, examples/legacy-shop, docs/schema.md). Three independent reviews. The first found 2 high issues (user SQL reached the sqlite3 shell as script, so `.trace FILE` after a `GO` or `/` line could write any file and overwrite a database; `test` passed differing values because quote mode printed raw line breaks) and 4 medium ones (the `;` guard bypassed through comments, a `--` comment in `where` breaking compat.sql, a table replaced by a view missing columns marked safe, `diff --map` covering without compiling) plus lows (empty results, WITH...INSERT, EXPLAIN, triggers, WITHOUT ROWID text, rowid alias, virtual tables, WAL files, Available per call). The second found three ways around the first shell fix (statements moved to column 0, NUL bytes, `GO -- x` lines); the guard now replays the shell's own line rules on the exact text sent, and refuses control bytes. The third, with 2.1 million differential cases against sqlite3_complete and 37,000 fuzzed runs, found no shell bypass, only the map file name reaching a compat.sql comment; fixed, and compat now checks the kind of every statement it writes. Every finding has a test. Plan deviations: `check` reads queries from a `.sql` file only (record fixtures hold HTTP, not SQL); `test` compares old and new databases instead of fixtures with expected rows, which needs no third format. A table whose old name the new schema still uses for a table can't get a view (SQLite shares one namespace); diff and the mapping say so. 506 tests recorded (macOS CI then showed sqlite3 3.50 escapes line breaks with unistr() and has no FTS5; tests now accept both). Version raised to 0.8.0 for the automated release.

- Oct 22 2026: 0.8.0 released and verified (all archives, SHA256SUMS, latest links; release smoke ran the schema commands on Linux and macOS). The downloaded binary ran the legacy-shop example: diff 2 breaking and 0 with the map, check 1 of 12 without the views and 12 of 12 with them, test 12 of 12, compat the same bytes as the committed compat.sql; against real database files the checksums stayed the same, compat.sql applied with plain sqlite3, an old report gave the same rows on both databases, a 1.0 customer with a fax failed one read, a mapping without the cents division failed 6 of 12, and a mapping with a GO line and a dot-command was refused with nothing written. Site (dated Oct 21 by UTC): AfterSchema reference (/schema-reference/, from docs/schema.md), quick start part seven, use case, roadmap, concepts, architecture, about, home, download and reference pages, release, testing and case study posts; hero now 506 tests, "Eight tools are out" and the badge "AfterSchema in 0.8.0". softdevstudio.com note published (row 2j), with a line on 0.7.1 and 0.7.2. Both Cloudflare deploys succeeded; the pages couldn't be fetched from this workspace to look at.

- Oct 22 2026: 0.9.0 AfterBridge built (internal/bridge, `bridge serve|check|convert|test`, examples/legacy-exports, docs/bridge.md). Writing the bridge turned up two real gaps, fixed before review: a version cut off at a line boundary was served when the writer paused longer than a check (CSV files are now read only after `settle`, 2s by default), and Go's CSV reader skips blank lines, which loses rows in a one-column file (blank lines are now refused). Independent review found 1 high issue (a `.sql` dump cut off between statements was served as complete) and 2 medium ones (a modification time in the future hung startup with no health answer; text that isn't UTF-8 came out of sqlite3's JSON mode mangled) plus 11 low ones (big integers rounded in number columns, Go-only number spellings, CRLF in quoted values, 0 and -0 keys, unknown query names logged, convert guard bypassed by a linked folder, filters on hidden columns, an item path hiding a fixed path, no inode in the fingerprint, unclear limits). Fixes: settle counts on the bridge's own clock from first sight (or an older modification time), dumps settle and must end with a line break, `;` and no open transaction, SQLite text read as hex and checked, numbers decimal only, serve listens before reading sources, only known query names logged, fixed paths first, inode and change time in the fingerprint. A short second review confirmed every fix and found low ones (OpenBSD build, sqlite3 output cap halved by hex, big whole numbers in CSV number columns, a dump cut inside a statement), also fixed. Every finding has a test. 530 tests recorded. Version raised to 0.9.0 for the automated release.

## Next

1. 0.9.0 AfterBridge, as above (approved Oct 22 2026).
