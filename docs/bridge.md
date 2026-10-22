# AfterBridge reference

`aftermarket bridge` serves an old app's CSV exports and SQLite files as a read-only JSON API. It's for software that only hands its data over as files: a nightly export in a folder, a SQLite database nobody else is meant to open. Other programs then ask for `/orders?paid=false` or `/products/BK-001` instead of parsing the files themselves.

AfterBridge never writes the files it reads, and nothing in a request ever reaches SQL or the file system. Each source is read into a snapshot in memory, read again when its file changes, and every answer comes from a snapshot.

```text
aftermarket bridge serve   --config bridge.yaml [--listen ADDR] [--log FILE|-]
aftermarket bridge check   --config bridge.yaml
aftermarket bridge convert --config bridge.yaml --source NAME --out FILE
aftermarket bridge test    --config bridge.yaml --fixture fixture.yaml
```

SQLite sources are read through the `sqlite3` command, 3.37 or later, as AfterSchema does. CSV sources need nothing else.

## The config file

```yaml
version: 1

limits:                     # all optional
  max_rows: 100000          # a source with more rows is refused
  max_file: 64MiB           # a CSV file bigger than this is refused
  check_every: 2s           # how often sources are checked for changes
  settle: 2s                # how long a CSV file must be left alone before it is read
  page_size: 100            # default limit for lists
  max_page_size: 1000       # largest limit a request may ask for

sources:
  - id: orders
    csv:
      folder: exports
      pattern: "orders-*.csv"   # the last file by name, so the newest day
    key: id
    columns:
      id: integer
      total: number
      paid: boolean
      placed: date
    exclude: [internal_note]

  - id: products
    sqlite:
      db: inventory.db          # a database file, or a .sql dump
      table: products           # or query: "SELECT ... FROM ..."
    key: sku
    columns:
      price: number
      discontinued: boolean
    exclude: [photo]

endpoints:
  - path: /orders
    source: orders
    filters: [paid, placed]
  - path: /orders/{id}
    source: orders
  - path: /products/{sku}
    source: products
    fields:
      sku: sku
      title: name
      price: price
```

Paths in the file are relative to the config file's folder.

### Sources

| Key | Meaning |
|---|---|
| `id` | The source's name. Letters, digits, `.`, `_` and `-`. |
| `csv.file` | One CSV file. |
| `csv.folder`, `csv.pattern` | The last file by name in a folder that matches a pattern, such as `orders-*.csv`. Exports named by date give the newest day. |
| `csv.delimiter` | `,` (the default), `;`, `|` or `tab`. |
| `sqlite.db` | A SQLite database file, or a `.sql` dump, which is loaded into memory. |
| `sqlite.table` | A table to serve whole. |
| `sqlite.query` | Or one fixed `SELECT`, written in the config. |
| `key` | The column item endpoints look rows up by. Every row must have one, and no two the same. |
| `columns` | Declared types: `text`, `integer`, `number`, `boolean` or `date`. |
| `exclude` | Columns never read into the snapshot or served. |

A CSV file has a header row. Columns not listed under `columns` are text. In a declared column, an empty value is `null`; `boolean` reads `true`, `false`, `yes`, `no`, `1` and `0`; `date` must be `YYYY-MM-DD` and a real day. A value that doesn't fit its type refuses the whole file, with the line and column, and never the value itself.

A SQLite column keeps the type SQLite stored: integers as integers, reals as numbers with every digit kept, text as strings, NULL as `null`. A declared type is checked: `integer` must hold integers, `number` takes integers and reals, `boolean` must hold 0 or 1, `date` must be text in `YYYY-MM-DD`. A blob can't go into JSON, so a source with one is refused; exclude the column.

### Endpoints

| Key | Meaning |
|---|---|
| `path` | `/orders` answers a list. `/orders/{id}` answers one row by the source's key. Only the last part can be a `{name}`. |
| `source` | The source it reads. |
| `fields` | What is shown, in order: a list of columns, or a map of names to columns, which renames them. Without it, every column under its own name. |
| `filters` | Field names a list can be filtered by. |

## Requests and answers

A list:

```text
GET /orders?paid=false&limit=2&offset=0
{"items":[{"id":1004,"customer":"Turing, Alan","total":8.5,"paid":false,"placed":"2026-10-21"}],"total":1,"limit":2,"offset":0}
```

`total` counts every matching row; `limit` (1 to `max_page_size`) and `offset` page through them, in the source's order. A filter is an exact match, read as the column's type: `paid=yes` and `paid=true` find the same rows, `total=8.50` finds 8.5. A filter value that can't be the column's type gets 400.

One row:

```text
GET /orders/1004
{"id":1004,"customer":"Turing, Alan","total":8.5,"paid":false,"placed":"2026-10-21"}
```

A key is read as the key column's type, so `/orders/01004` finds order 1004 when `id` is an integer. A key that isn't there gets 404.

Numbers keep their type: a `number` that happens to be whole is written `15.0`, an `integer` is written `15`.

| Answer | When |
|---|---|
| 200 | A list or a row. |
| 400 | A query parameter the endpoint doesn't list, one given twice, a bad `limit`, `offset` or filter value, any query on an item endpoint, or a path that could be read more than one way (`..`, an encoded slash, a backslash or a control character). |
| 404 | No endpoint for the path, or no row with that key. |
| 405 | Anything but GET and HEAD, with `Allow: GET, HEAD`. |
| 503 | The source hasn't been read yet, with `Retry-After`. |

Errors are JSON, `{"error": "..."}`. Every answer has `Content-Length`. HEAD gets the same headers without the body. `GET /_aftermarket/healthz` answers `200 ok`.

## When files change

Every `check_every`, AfterBridge looks at each source's file: which file, its size and its modification time. When they change, the source is read again into a new snapshot, and requests move to it at once. Until then, every request is answered from the snapshot it had.

A CSV file is read only once it has been left alone for `settle`, measured from its modification time, and only if it doesn't change while it is read. It is refused, and the last good snapshot stays, when:

- it doesn't end with a line break, or is empty;
- a quoted value never ends, or a row has the wrong number of values;
- it has a blank line (Go's CSV reader would skip it, and in a one-column file that is a row lost);
- a value doesn't fit its column's type, a key is missing or repeated, or a column the config uses is gone;
- it isn't valid UTF-8, or is over `max_file` or `max_rows`.

Each problem is logged once. A source that was never read answers 503 until it can be.

An export written slowly, with pauses longer than `settle`, can still be read between two pauses. If your app does that, have it write to another name and rename the file when it's done (renaming is instant), or raise `settle`.

A SQLite source's rows are read with a single query, which SQLite answers from one consistent state, so it is read again as soon as the database or its `-wal` file changes, with no settle time. Reading a database in WAL mode leaves the usual `-wal` and `-shm` helper files next to it, as any SQLite reader does; the database itself is not changed.

## The log

`--log` (stderr by default) gets one JSON line per request and per source event:

```json
{"time":"...","event":"loaded","request":0,"rule":"orders","method":"-","path":"-","reason":"4 rows from orders-2026-10-21.csv"}
{"time":"...","event":"served","request":3,"method":"GET","path":"/orders/{id}","status":200}
{"time":"...","event":"served","request":4,"method":"GET","path":"/orders","query_keys":["paid"],"status":400}
{"time":"...","event":"kept","request":0,"rule":"orders","method":"-","path":"-","reason":"line 3, column id: not a whole number; still serving the last good snapshot"}
```

Requests are logged by endpoint pattern and query parameter names, never by key or value.

## check, convert and test

`bridge check` reads every source once and lists what it found, or every problem, and exits 1 if any source can't be served.

`bridge convert --source orders --out orders.json` writes one source's snapshot as a JSON array, one row per line, every column under its own name. It writes atomically, and refuses an `--out` that is the config, a source file, a database or its helper files, or a name the source's folder pattern would pick up.

`bridge test` runs a fixture file in the format the other tools use, against the bridge with its sources read once:

```yaml
cases:
  - name: one order by id
    request:
      path: /orders/1003
    expect:
      response:
        status: 200
        body: '{"id":1003,"customer":"Grace Hopper","total":249.99,"paid":true,"placed":"2026-10-21"}'
```

## Promises

- **Sources are never written.** CSV files are only opened for reading, databases are read through `sqlite3` read-only and in safe mode, and the tests check every file byte for byte and that no new file appears.
- **Requests can't reach SQL or files.** The only SQL is the config's own; a request picks rows from a snapshot in memory. A test puts a recorder in front of `sqlite3` and checks that requests, including ones built to inject SQL or shell commands, never start it.
- **A half-written file is never served,** in the sense above: a file still changing within `settle`, or one that is incomplete or broken. A test rewrites an export in pieces with pauses while requests run, and every answer is one complete version.
- **Every answer matches its snapshot,** values and types alike. A test writes random values of every type and reads them back exactly.

## Limits

- Reads only: no writes, no authentication. Put [AfterGuard](guard.md) in front for logins and rate limits.
- One source per endpoint; no joins across sources.
- CSV and SQLite only; no XML, fixed-width files or other databases.
- A whole source is held in memory, up to `max_rows` rows.
- Filters are exact matches; no ranges, sorting or search.
