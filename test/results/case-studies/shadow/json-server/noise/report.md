# Shadow report

- A (primary, answers clients): http://127.0.0.1:9201
- B (candidate, gets copies): http://127.0.0.1:9202
- Headers compared: Content-Type

19 case(s): 19 same, 0 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:22:09Z, updated 2026-10-31T10:22:11Z
- 19 request(s) served from A; 19 copied to B
- Not copied: 0 outside when:, 0 left out by sampling (1), 0 writes without --include-writes, 0 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 1 worker(s), candidate timeout 5s
