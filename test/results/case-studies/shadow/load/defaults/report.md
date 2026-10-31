# Shadow report

- A (primary, answers clients): http://127.0.0.1:9101
- B (candidate, gets copies): http://127.0.0.1:9102
- Headers compared: Content-Type

4430 case(s): 4430 same, 0 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:24:08Z, updated 2026-10-31T10:24:39Z
- 6000 request(s) served from A; 4430 copied to B
- Not copied: 0 outside when:, 0 left out by sampling (1), 0 writes without --include-writes, 1570 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 4 worker(s), candidate timeout 5s
