# Shadow report

- A (primary, answers clients): http://127.0.0.1:9101
- B (candidate, gets copies): http://127.0.0.1:9102
- Headers compared: Content-Type

5484 case(s): 5484 same, 0 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:25:16Z, updated 2026-10-31T10:25:53Z
- 6000 request(s) served from A; 5484 copied to B
- Not copied: 0 outside when:, 0 left out by sampling (1), 0 writes without --include-writes, 516 dropped because the queue (1024) was full, 0 that couldn't be compared safely
- 8 worker(s), candidate timeout 5s
