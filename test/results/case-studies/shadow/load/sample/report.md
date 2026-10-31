# Shadow report

- A (primary, answers clients): http://127.0.0.1:9101
- B (candidate, gets copies): http://127.0.0.1:9102
- Headers compared: Content-Type

600 case(s): 600 same, 0 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:24:39Z, updated 2026-10-31T10:25:16Z
- 6000 request(s) served from A; 600 copied to B
- Not copied: 0 outside when:, 5400 left out by sampling (0.1), 0 writes without --include-writes, 0 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 4 worker(s), candidate timeout 5s
