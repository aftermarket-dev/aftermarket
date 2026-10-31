# Shadow report

- A (primary, answers clients): http://127.0.0.1:9301
- B (candidate, gets copies): http://127.0.0.1:9302
- Headers compared: Content-Type

2 case(s): 0 same, 2 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:28:14Z, updated 2026-10-31T10:28:15Z
- 2 request(s) served from A; 2 copied to B
- Not copied: 0 outside when:, 0 left out by sampling (1), 0 writes without --include-writes, 0 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 4 worker(s), candidate timeout 5s

## Differences

### 1 GET /api/v1/customer

`GET /api/v1/customer?id=7&api_key=REDACTED`

- status: 200 vs 404
- customer_id: only in A (7)
- email: only in A (REDACTED)
- error: only in B (an object)
- name: only in A ("Ada Lovelace")

### 2 GET /api/v1/customer

`GET /api/v1/customer?id=42&api_key=REDACTED`

- status: 200 vs 404
- customer_id: only in A (42)
- email: only in A (REDACTED)
- error: only in B (an object)
- name: only in A ("Grace Hopper")
