# Shadow report

- A (primary, answers clients): http://127.0.0.1:9201
- B (candidate, gets copies): http://127.0.0.1:9202
- Headers compared: Content-Type

13 case(s): 0 same, 13 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:22:12Z, updated 2026-10-31T10:22:13Z
- 19 request(s) served from A; 13 copied to B
- Not copied: 0 outside when:, 0 left out by sampling (1), 6 writes without --include-writes, 0 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 4 worker(s), candidate timeout 5s

## Differences

### 1 GET /posts

`GET /posts`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- 0.id: 1 vs "1"
- 1.id: 2 vs "2"
- 2.id: 3 vs "3"
- 3.id: 4 vs "4"
- 4.id: 5 vs "5"
- 5.id: 6 vs "6"

### 2 GET /posts/1

`GET /posts/1`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- id: 1 vs "1"

### 3 GET /posts

`GET /posts?authorId=1`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- 0.id: 1 vs "1"
- 1.id: 3 vs "3"
- 2.id: 6 vs "6"

### 4 GET /posts

`GET /posts?_sort=views&_order=desc`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- body: 6 items vs 0 items

### 5 GET /posts

`GET /posts?_page=1&_limit=2`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- body: a list vs an object

### 6 GET /posts

`GET /posts?title_like=report`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- body: 2 items vs 0 items

### 7 GET /posts/1/comments

`GET /posts/1/comments`

- status: 200 vs 404
- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- body: a list vs an object

### 8 GET /posts

`GET /posts?_embed=comments`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- 0.comments: 2 items vs 0 items
- 0.id: 1 vs "1"
- 1.id: 2 vs "2"
- 2.comments: 1 items vs 0 items
- 2.id: 3 vs "3"
- 3.id: 4 vs "4"
- 4.id: 5 vs "5"
- 5.id: 6 vs "6"

### 9 GET /posts/99

`GET /posts/99`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- error: only in B ("Not Found")

### 10 GET /profile

`GET /profile`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"

### 15 GET /comments

`GET /comments`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- body: 2 items vs 3 items
- 0.id: 1 vs "1"
- 1.id: 2 vs "2"

### 16 GET /posts/7

`GET /posts/7`

- status: 200 vs 404
- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- authorId: only in A (2)
- error: only in B ("Not Found")
- id: only in A (7)
- title: only in A ("Release notes for 2.0")
- views: only in A (0)

### 19 GET /posts

`GET /posts`

- header Content-Type: "application/json; charset=utf-8" vs "application/json"
- 0.id: 1 vs "1"
- 1.id: 2 vs "2"
- 1.views: 86 vs 85
- 2.id: 3 vs "3"
- 3.id: 4 vs "4"
- 4.id: 5 vs "5"
- 5.id: 6 vs "6"
