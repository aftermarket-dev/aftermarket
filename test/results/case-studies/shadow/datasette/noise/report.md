# Shadow report

- A (primary, answers clients): http://127.0.0.1:9101
- B (candidate, gets copies): http://127.0.0.1:9103
- Headers compared: Content-Type

22 case(s): 9 same, 13 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:22:30Z, updated 2026-10-31T10:22:32Z
- 22 request(s) served from A; 22 copied to B
- Not copied: 0 outside when:, 0 left out by sampling (1), 0 writes without --include-writes, 0 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 4 worker(s), candidate timeout 5s

## Differences

### 1 GET /parks/parks.json

`GET /parks/parks.json`

- query_ms: 12.344115000814782 vs 16.873082999154576
- suggested_facets.0.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state"
- suggested_facets.1.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=established" vs "http://127.0.0.1:9103/parks/parks.json?_facet=established"

### 3 GET /parks/parks.json

`GET /parks/parks.json?_size=5&_next=5`

- next_url: "http://127.0.0.1:9101/parks/parks.json?_size=5\u0026_next=10" vs "http://127.0.0.1:9103/parks/parks.json?_size=5\u0026_next=10"
- query_ms: 5.526615001144819 vs 8.517696000126307

### 2 GET /parks/parks.json

`GET /parks/parks.json?_size=5`

- next_url: "http://127.0.0.1:9101/parks/parks.json?_size=5\u0026_next=5" vs "http://127.0.0.1:9103/parks/parks.json?_size=5\u0026_next=5"
- query_ms: 22.396326001398847 vs 26.437513000928448
- suggested_facets.0.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_size=5\u0026_facet=state" vs "http://127.0.0.1:9103/parks/parks.json?_size=5\u0026_facet=state"
- suggested_facets.1.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_size=5\u0026_facet=established" vs "http://127.0.0.1:9103/parks/parks.json?_size=5\u0026_facet=established"

### 4 GET /parks/parks.json

`GET /parks/parks.json?state=CA`

- query_ms: 6.232105999515625 vs 20.255631001418806
- suggested_facets.0.toggle_url: "http://127.0.0.1:9101/parks/parks.json?state=CA\u0026_facet=established" vs "http://127.0.0.1:9103/parks/parks.json?state=CA\u0026_facet=established"

### 5 GET /parks/parks.json

`GET /parks/parks.json?state=UT&_sort_desc=visitors_2023`

- query_ms: 6.020791999617359 vs 71.05857100032154

### 10 GET /parks/parks/3.json

`GET /parks/parks/3.json`

- query_ms: 3.0812029999651713 vs 9.426721000636462

### 6 GET /parks/parks.json

`GET /parks/parks.json?_facet=state`

- facet_results.state.results.0.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=CA" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=CA"
- facet_results.state.results.1.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=UT" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=UT"
- facet_results.state.results.2.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=AK" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=AK"
- facet_results.state.results.3.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=AZ" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=AZ"
- facet_results.state.results.4.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=FL" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=FL"
- facet_results.state.results.5.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=ME" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=ME"
- facet_results.state.results.6.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=MT" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=MT"
- facet_results.state.results.7.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=WA" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=WA"
- facet_results.state.results.8.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026state=WY" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026state=WY"
- query_ms: 7.16043099964736 vs 112.94420700141927
- suggested_facets.0.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_facet=state\u0026_facet=established" vs "http://127.0.0.1:9103/parks/parks.json?_facet=state\u0026_facet=established"

### 11 GET /parks/parks/12.json

`GET /parks/parks/12.json`

- query_ms: 2.9137199999240693 vs 14.857948999633663

### 13 GET /parks.json

`GET /parks.json?sql=select+state,+count(*)+as+n+from+parks+group+by+state+order+by+n+desc`

- query_ms: 2.195004000896006 vs 2.0747149992530467

### 15 GET /parks.json

`GET /parks.json`

- query_ms: 7.219149998491048 vs 25.491823000265867

### 8 GET /parks/parks.json

`GET /parks/parks.json?_shape=objects`

- query_ms: 18.44755499951134 vs 91.39196800060745
- suggested_facets.0.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_shape=objects\u0026_facet=state" vs "http://127.0.0.1:9103/parks/parks.json?_shape=objects\u0026_facet=state"
- suggested_facets.1.toggle_url: "http://127.0.0.1:9101/parks/parks.json?_shape=objects\u0026_facet=established" vs "http://127.0.0.1:9103/parks/parks.json?_shape=objects\u0026_facet=established"

### 18 GET /parks/parks

`GET /parks/parks`

- body: 17309 bytes vs 17309 bytes, first difference at byte 305

### 19 GET /parks/parks/3

`GET /parks/parks/3`

- body: 4590 bytes vs 4590 bytes, first difference at byte 296
