# Shadow report

- A (primary, answers clients): http://127.0.0.1:9101
- B (candidate, gets copies): http://127.0.0.1:9103
- Headers compared: Content-Type
- Ignored: query_ms, next_url, suggested_facets.*.toggle_url, facet_results.*.results.*.toggle_url

18 case(s): 18 same, 0 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:28:18Z, updated 2026-10-31T10:28:19Z
- 22 request(s) served from A; 18 copied to B
- Not copied: 4 outside when:, 0 left out by sampling (1), 0 writes without --include-writes, 0 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 4 worker(s), candidate timeout 5s
