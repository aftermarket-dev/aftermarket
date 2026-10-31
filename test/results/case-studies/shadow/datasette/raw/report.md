# Shadow report

- A (primary, answers clients): http://127.0.0.1:9101
- B (candidate, gets copies): http://127.0.0.1:9102
- Headers compared: Content-Type

22 case(s): 5 same, 17 different, 0 skipped, 0 error(s)

## Traffic

- Started 2026-10-31T10:22:32Z, updated 2026-10-31T10:22:33Z
- 22 request(s) served from A; 22 copied to B
- Not copied: 0 outside when:, 0 left out by sampling (1), 0 writes without --include-writes, 0 dropped because the queue (64) was full, 0 that couldn't be compared safely
- 4 worker(s), candidate timeout 5s

## Differences

### 1 GET /parks/parks.json

`GET /parks/parks.json`

- allow_execute_sql: only in A (true)
- columns: only in A (a list)
- database: only in A ("parks")
- expandable_columns: only in A (a list)
- expanded_columns: only in A (a list)
- facet_results: only in A (an object)
- filtered_table_rows_count: only in A (12)
- human_description_en: only in A ("")
- is_view: only in A (false)
- ok: only in B (true)
- primary_keys: only in A (a list)
- private: only in A (false)
- query: only in A (an object)
- query_ms: only in A (16.722318001484382)
- rows.0: a list vs an object
- rows.1: a list vs an object
- rows.2: a list vs an object
- rows.3: a list vs an object
- rows.4: a list vs an object
- rows.5: a list vs an object
- and 9 more

### 2 GET /parks/parks.json

`GET /parks/parks.json?_size=5`

- allow_execute_sql: only in A (true)
- columns: only in A (a list)
- database: only in A ("parks")
- expandable_columns: only in A (a list)
- expanded_columns: only in A (a list)
- facet_results: only in A (an object)
- filtered_table_rows_count: only in A (12)
- human_description_en: only in A ("")
- is_view: only in A (false)
- next_url: "http://127.0.0.1:9101/parks/parks.json?_size=5\u0026_next=5" vs "http://127.0.0.1:9102/parks/parks.json?_size=5\u0026_next=5"
- ok: only in B (true)
- primary_keys: only in A (a list)
- private: only in A (false)
- query: only in A (an object)
- query_ms: only in A (17.872811000415822)
- rows.0: a list vs an object
- rows.1: a list vs an object
- rows.2: a list vs an object
- rows.3: a list vs an object
- rows.4: a list vs an object
- and 3 more

### 3 GET /parks/parks.json

`GET /parks/parks.json?_size=5&_next=5`

- allow_execute_sql: only in A (true)
- columns: only in A (a list)
- database: only in A ("parks")
- expandable_columns: only in A (a list)
- expanded_columns: only in A (a list)
- facet_results: only in A (an object)
- filtered_table_rows_count: only in A (12)
- human_description_en: only in A ("")
- is_view: only in A (false)
- next_url: "http://127.0.0.1:9101/parks/parks.json?_size=5\u0026_next=10" vs "http://127.0.0.1:9102/parks/parks.json?_size=5\u0026_next=10"
- ok: only in B (true)
- primary_keys: only in A (a list)
- private: only in A (false)
- query: only in A (an object)
- query_ms: only in A (6.410302999938722)
- rows.0: a list vs an object
- rows.1: a list vs an object
- rows.2: a list vs an object
- rows.3: a list vs an object
- rows.4: a list vs an object
- and 3 more

### 4 GET /parks/parks.json

`GET /parks/parks.json?state=CA`

- allow_execute_sql: only in A (true)
- columns: only in A (a list)
- database: only in A ("parks")
- expandable_columns: only in A (a list)
- expanded_columns: only in A (a list)
- facet_results: only in A (an object)
- filtered_table_rows_count: only in A (3)
- human_description_en: only in A ("where state = \"CA\"")
- is_view: only in A (false)
- ok: only in B (true)
- primary_keys: only in A (a list)
- private: only in A (false)
- query: only in A (an object)
- query_ms: only in A (10.959451999951852)
- rows.0: a list vs an object
- rows.1: a list vs an object
- rows.2: a list vs an object
- suggested_facets: only in A (a list)
- table: only in A ("parks")
- units: only in A (an object)

### 5 GET /parks/parks.json

`GET /parks/parks.json?state=UT&_sort_desc=visitors_2023`

- allow_execute_sql: only in A (true)
- columns: only in A (a list)
- database: only in A ("parks")
- expandable_columns: only in A (a list)
- expanded_columns: only in A (a list)
- facet_results: only in A (an object)
- filtered_table_rows_count: only in A (2)
- human_description_en: only in A ("where state = \"UT\" sorted by visitors_2023 descending")
- is_view: only in A (false)
- ok: only in B (true)
- primary_keys: only in A (a list)
- private: only in A (false)
- query: only in A (an object)
- query_ms: only in A (13.24058800128114)
- rows.0: a list vs an object
- rows.1: a list vs an object
- suggested_facets: only in A (a list)
- table: only in A ("parks")
- units: only in A (an object)

### 6 GET /parks/parks.json

`GET /parks/parks.json?_facet=state`

- allow_execute_sql: only in A (true)
- columns: only in A (a list)
- database: only in A ("parks")
- expandable_columns: only in A (a list)
- expanded_columns: only in A (a list)
- facet_results.results: only in B (an object)
- facet_results.state: only in A (an object)
- facet_results.timed_out: only in B (a list)
- filtered_table_rows_count: only in A (12)
- human_description_en: only in A ("")
- is_view: only in A (false)
- ok: only in B (true)
- primary_keys: only in A (a list)
- private: only in A (false)
- query: only in A (an object)
- query_ms: only in A (14.726859000802506)
- rows.0: a list vs an object
- rows.1: a list vs an object
- rows.2: a list vs an object
- rows.3: a list vs an object
- and 11 more

### 8 GET /parks/parks.json

`GET /parks/parks.json?_shape=objects`

- allow_execute_sql: only in A (true)
- columns: only in A (a list)
- database: only in A ("parks")
- expandable_columns: only in A (a list)
- expanded_columns: only in A (a list)
- facet_results: only in A (an object)
- filtered_table_rows_count: only in A (12)
- human_description_en: only in A ("")
- is_view: only in A (false)
- ok: only in B (true)
- primary_keys: only in A (a list)
- private: only in A (false)
- query: only in A (an object)
- query_ms: only in A (17.25055200040515)
- suggested_facets: only in A (a list)
- table: only in A ("parks")
- units: only in A (an object)

### 10 GET /parks/parks/3.json

`GET /parks/parks/3.json`

- columns: only in A (a list)
- ok: only in B (true)
- query_ms: 2.843246000338695 vs 10.418756000944995
- rows.0: a list vs an object
- truncated: only in B (false)
- units: only in A (an object)

### 11 GET /parks/parks/12.json

`GET /parks/parks/12.json`

- columns: only in A (a list)
- ok: only in B (true)
- query_ms: 3.144271999190096 vs 4.298711999581428
- rows.0: a list vs an object
- truncated: only in B (false)
- units: only in A (an object)

### 12 GET /parks/parks/99.json

`GET /parks/parks/99.json`

- error: "Record not found" vs "Row not found"
- errors: only in B (a list)
- title: only in A (null)

### 13 GET /parks.json

`GET /parks.json?sql=select+state,+count(*)+as+n+from+parks+group+by+state+order+by+n+desc`

- status: 200 vs 302
- header Content-Type: "application/json; charset=utf-8" vs "text/plain"
- body: 406 bytes vs 0 bytes, first difference at byte 0

### 14 GET /parks.json

`GET /parks.json?sql=select+name+from+parks+where+area_km2+>+5000&_shape=array`

- status: 200 vs 302
- header Content-Type: "application/json; charset=utf-8" vs "text/plain"
- body: 69 bytes vs 0 bytes, first difference at byte 0

### 15 GET /parks.json

`GET /parks.json`

- metadata: only in B (an object)
- ok: only in B (true)
- queries_count: only in B (0)
- queries_more: only in B (false)
- query_ms: only in A (3.559443999620271)
- table_columns: only in B (an object)
- tables.0.count_truncated: only in B (false)

### 20 GET /-/versions.json

`GET /-/versions.json`

- datasette.version: "0.65.5" vs "1.0a41"
- ok: only in B (true)
- sqlite.extensions.json1: null vs true

### 22 GET /-/metadata.json

`GET /-/metadata.json`

- status: 200 vs 404
- error: only in B ("Database not found")
- errors: only in B (a list)
- ok: only in B (false)
- status: only in B (404)

### 19 GET /parks/parks/3

`GET /parks/parks/3`

- body: 4590 bytes vs 7932 bytes, first difference at byte 21

### 18 GET /parks/parks

`GET /parks/parks`

- body: 17309 bytes vs 24722 bytes, first difference at byte 21
