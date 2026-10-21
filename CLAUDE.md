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

## Next

1. 0.7.0 TOML for AfterConfig, as above.
2. After it: AfterSchema, AfterBridge, the CI flake or something else, decided with the owner; nothing is started until then.
