# AfterSchema reference

`aftermarket schema` helps an old app keep working after its SQLite database moved to a new schema. You describe each old table in terms of the new tables, and AfterSchema writes SQL views that show the old tables again, read from the new ones. Before you apply anything, it shows what changed between the two schemas, which old queries would break, and whether every old read gives the same rows through the views as it did on the old database.

AfterSchema never writes a database. It opens them read-only, and its only output is a `.sql` file you review and apply yourself.

```text
aftermarket schema diff   --old A --new B [--map map.yaml] [--json]
aftermarket schema compat --map map.yaml --out compat.sql [--new B]
aftermarket schema check  --queries old.sql [--map map.yaml] [--new B]
aftermarket schema test   --map map.yaml --queries old.sql [--old A] [--new B]
```

`A` and `B` are each a SQLite database file or a `.sql` file (a schema, with or without data, such as the output of `sqlite3 app.db .dump`). A `.sql` file is loaded into memory, never into a file.

AfterSchema works through the `sqlite3` command, which must be installed (any version from 3.37 on). Set `AFTERMARKET_SQLITE3` to use a particular one.

## The mapping file

```yaml
version: 1
old: v1/shop.sql          # the old schema (paths are relative to this file)
new: v2/shop.sql          # the new schema, used when --new isn't given

views:
  - old: customers        # the name old queries use
    from: accounts        # the new table (or view) it reads
    columns:
      name: first_name || ' ' || last_name
      fax: null           # no longer stored
      created: created_at
    where: deleted_at IS NULL

  - old: orders
    from: purchases
    columns:
      customer_id: account_id
      total: total_cents / 100.0
      status: state
      placed: placed_at
```

| Key | Meaning |
|---|---|
| `version` | Always `1`. |
| `old` | The old schema. Required, so every view can list the old columns in their old order. |
| `new` | The new schema. Optional; `--new` overrides it. |
| `views[].old` | An old table or view name. Each may be listed once. |
| `views[].from` | The new table or view the old one is read from. |
| `views[].columns` | Old column name to an SQL expression over `from`. `null` means the column is always NULL. |
| `views[].where` | Optional SQL condition. Rows that don't meet it are hidden from old queries. |

Old columns that aren't listed keep their name: the view reads the column with the same name in `from`, and `compat` stops if there is none. Each view shows every old column, in the old order, so `SELECT *` and `INSERT ... SELECT` keep the old shape.

Expressions are plain SQL and may call any function SQLite has. Each must be one piece of SQL: no `;`, no `--` comment (put a `#` comment after the YAML value instead, or use `/* */`), and no string or comment left open. An expression that doesn't compile on the new schema is reported with its line in the mapping file.

## diff

Lists every difference between two schemas and marks the ones that can break an old app.

```text
$ aftermarket schema diff --old v1/shop.sql --new v2/shop.sql
v1/shop.sql -> v2/shop.sql: 5 change(s), 2 breaking
  breaking  table customers removed
  breaking  table orders removed
  safe      column products.sku added (TEXT)
  safe      table accounts added (id, first_name, last_name, email, created_at, deleted_at)
  safe      table purchases added (id, account_id, total_cents, state, placed_at)
```

Breaking: a table, view or column removed; a table replaced by a view that doesn't show all its columns; an `INTEGER PRIMARY KEY` that is no longer the table's rowid; a column whose type now reads values differently (a different type affinity); a column that may now be NULL; a view that no longer shows a column; a table that became `WITHOUT ROWID`. Other changes are listed as safe, with a note where old writes could now fail, such as a new `NOT NULL` column with no default, a new unique index or a table that became `STRICT`. Declared types with the same affinity (`VARCHAR(10)` and `TEXT`) are the same to SQLite and are listed as safe.

With `--map`, breaking changes the views take care of are marked `covered`:

```text
$ aftermarket schema diff --map map.yaml
v1/shop.sql -> v2/shop.sql: 5 change(s), 0 breaking
  covered   table customers removed, covered by the compatibility view customers
  covered   table orders removed, covered by the compatibility view orders
  ...
```

A view counts as covering only when every view in the mapping compiles on the new schema; otherwise the problems are listed and nothing is marked covered. `diff` exits 1 when any breaking change is left uncovered, so it fits in CI. `--json` prints the same list as JSON. The inner tables of a virtual table (such as FTS5's) and its hidden columns aren't listed.

## compat

Writes the views to a file:

```text
$ aftermarket schema compat --map map.yaml --out compat.sql
wrote compat.sql: 2 view(s): customers, orders
```

```sql
BEGIN;

DROP VIEW IF EXISTS "customers";
CREATE VIEW "customers" ("id", "name", "email", "fax", "created") AS
  SELECT
    "id",
    first_name || ' ' || last_name,
    "email",
    NULL,
    created_at
  FROM "accounts"
  WHERE deleted_at IS NULL;
...
COMMIT;
```

Before writing, `compat` reads both schemas, checks the mapping against them and compiles every view on the new schema. If anything is wrong, every problem is listed and nothing is written. The same mapping always gives the same bytes. `--out` may not be one of the inputs, and the file is replaced atomically.

Apply it yourself when you're ready, for example `sqlite3 new.db < compat.sql`. Running it again replaces the views.

## check

Compiles each old query on the new schema, with the views when `--map` is given, without running it:

```text
$ aftermarket schema check --queries reports.sql --new v2/shop.sql
FAIL  line 4: SELECT id, name, email FROM customers ORDER BY id
      no such table: customers
...
1 of 12 queries compile on the new schema

$ aftermarket schema check --queries reports.sql --map map.yaml
12 of 12 queries compile on the new schema with the compatibility views
```

The queries file holds statements separated by `;`, with `--` and `/* */` comments, as in any `.sql` file; a `CREATE TRIGGER` ends at the `;` after its `END`. Writes are compiled too, so `check` also shows which old `INSERT`, `UPDATE` and `DELETE` statements no longer fit. `EXPLAIN` statements are compiled as they are.

## test

Runs every old read on the old database and on the new one with the views, and compares the results:

```text
$ aftermarket schema test --map map.yaml --queries reports.sql
PASS  line 4: SELECT id, name, email FROM customers ORDER BY id
...
12 of 12 reads give the same rows on the new schema with the compatibility views as on the old one
```

Results must match in column names, values and types: `1` and `1.0` and `'1'` are all different, and a text with a line break is compared whole. When neither side gives any rows, the column names are still compared. A query with `ORDER BY` must give the rows in the same order, ties included; one without may give them in any order (an `ORDER BY` inside a subquery or a window doesn't count). Only reads are compared: `SELECT`, `VALUES`, and `WITH` when its main statement is a `SELECT`. Everything else, `EXPLAIN` included, is skipped. When a read differs, `test` names the first row that differs and shows both values, so give it sample data, not production data. It exits 1 on any difference.

For `test` to mean something, the old and new databases must hold the same data, each in its own shape: for example a copy of the old database and the result of running your migration on it.

## Promises

- **No database is ever written.** Databases are opened read-only and the `sqlite3` shell runs in safe mode, from an empty temporary folder, so SQL can't write, attach another file or run a program. `.sql` files are loaded into memory. Every piece of SQL from a file or a mapping is split into statements first, and each statement is checked exactly as it will be sent: it is refused if the `sqlite3` shell could read any line of it as one of its own commands (a line starting with `.` or `#` where no statement is open, or a line that is only `GO` or `/`, comments allowed after), if a string or comment never ends, or if it holds a control character other than a tab or a line break. A line that is only `GO` or `/` is refused even inside a string or comment. The tests try these attacks and check that every file stays the same byte for byte and no new file appears.
- **`compat` writes only its `--out` file**, never one of its inputs, and only when every view compiles on the new schema and the script reads back as exactly the statements written. The tests apply it to a real database.
- **The same mapping always writes the same file.**
- **What `test` passes reads the same.** Every read in the queries file gives the same rows, with the same types, through the views as on the old database. The tests check this with randomly generated schemas, renames, split columns and scaled numbers.

## Limits

- Views serve reads. An old app that writes to a mapped table gets an error from SQLite, because views can't be written to. `check` shows which statements are affected.
- A view can't have the same name as a table, so an old table whose name the new schema still uses for a table can't get a compatibility view. `diff` lists the changes to that table, and the mapping refuses it with a clear message.
- One old table is read from one new table or view. When an old table is spread over several new ones, create a view in the new database that joins them and map to that.
- A `CREATE TRIGGER` whose body has `CASE ... END;` is cut at that `END;`, as the `sqlite3` shell cuts it, so an error inside such a trigger may name the wrong line.
- SQLite only. Other databases have different rules for views and types.
- A database in WAL mode gets the usual `-wal` and `-shm` helper files next to it when it is read, as with any SQLite reader. The database file itself is not changed.
- `test` compares results on the data it is given. It can't know about rows the sample doesn't hold, such as an old customer who still had a fax number.
