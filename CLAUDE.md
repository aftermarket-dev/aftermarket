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

### Later, decided with the owner at the time
AfterGuard (needs a written threat model first), AfterHook, AfterConfig. Afterlife and AfterTrace stay research; Afterlife only ever replaces documented APIs, never licence checks or copy protection.

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

## Next

The approved plan is done as of 0.3.0. What comes next (AfterGuard, AfterHook, AfterConfig or something else) is decided with the owner; nothing is started until then.
