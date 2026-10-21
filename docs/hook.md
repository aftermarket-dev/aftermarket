# AfterHook reference

`aftermarket hook` watches places that change without telling anyone (a file, a folder, an HTTP endpoint, a command's output) and sends a signed webhook for each change. The [README](../README.md) explains what the tool is for.

## Commands

```text
aftermarket hook check --config hook.yaml
aftermarket hook run   --config hook.yaml [--once] [--dry-run] [--log FILE|-]
aftermarket hook test  --config hook.yaml --fixture fixture.yaml
```

- **`check`** validates the config, lists the sources and webhooks, and says whether each webhook's signing secret is set. URLs show only their scheme and host, since webhook URLs often carry a secret in the path or query.
- **`run`** checks each source on its interval and delivers events until stopped with Ctrl-C. When it stops it says how many events are still waiting; they are sent on the next run.
- **`--once`** checks every source once, delivers what's due, and exits. It fits a cron job or a CI step.
- **`--dry-run`** prints the events it finds as JSON lines instead of saving and sending them, each change once. The state file isn't touched.
- **`--log`** writes the log as JSON lines: `-` for stderr (the default), a file name to append to, or empty for none. The log never holds values from your sources.
- **`test`** runs before/after fixtures through the config's sources and compares the changes with what you expect.

Exit codes: 0 for success, 1 for problems in the config, the state file or a fixture, 2 for usage errors.

## Config file

```yaml
version: 1                       # optional; must be 1
state: hook-state.json           # required; relative to this file's folder
interval: 5s                     # optional; how often each source is checked
settle: 200ms                    # optional; see "Half-written files"
max_body: 1MiB                   # optional; the most read from one source
retry:                           # optional
  backoff: 1s                    # the first wait after a failed delivery
  max_backoff: 5m                # waits double up to this
  max_attempts: 0                # 0 (the default) retries until delivered

webhooks:
  - id: ops                      # required, unique
    url: https://hooks.example.com/aftermarket
    secret_env: HOOK_SECRET      # optional; the environment variable with the signing secret
    headers: {X-Team: ops}       # optional
    timeout: 10s                 # optional

sources:
  - id: order-status             # required, unique
    file: {path: status.json}
    ignore: [updated_at]         # optional; fields left out of field-by-field comparisons
    interval: 5s                 # optional; overrides the global one
    send_to: [ops]               # optional; every webhook by default
```

Unknown keys are errors, and `check` lists every problem with its line number. Paths are relative to the config file's folder.

## Sources

Each source watches one thing.

### file

```yaml
- id: order-status
  file: {path: status.json, format: json}
```

`format` is `json`, `yaml`, `toml`, `env` or `text`, and by default comes from the name (`.json`, `.yaml`, `.yml`, `.toml`, `.env`). For JSON, YAML, TOML and `.env` files, changes are listed by field, compared by meaning: key order, spacing and number formatting don't count, so a file rewritten with the same content gives no event. Other files are compared as text, line by line. A file that appears or disappears is reported too.

### folder

```yaml
- id: exports
  folder: {path: exports, pattern: "*.csv", recursive: false}
```

Reports files added, changed or removed, with their size and SHA-256. `pattern` matches file names. Subfolders are included only with `recursive: true`. Only regular files count; symbolic links are skipped. A file whose modification time changes but whose content doesn't gives no event.

A file is hashed again only when its size or modification time changes. A rewrite that keeps both, such as `cp -p` or `rsync -t` copying in a file of the same size, isn't noticed.

### http

```yaml
- id: price-list
  http: {url: https://internal.example.com/prices.json, headers: {Accept: application/json}, timeout: 10s}
  interval: 1m
```

GETs the URL on each check. A change in status code is reported, and so is a change in the body: field by field when both bodies are JSON, and line by line when neither is. When one is JSON and the other isn't, such as an error page in place of the usual answer, the event gives only the size and SHA-256 of each, so nothing the field comparison would have hidden goes out. An empty body counts as `null`. A request that fails is logged and checked again next time, without an event.

### command

```yaml
- id: orders-table
  command:
    run: [sqlite3, -json, app.db, "select id, status, total from orders order by id"]
    timeout: 10s
```

Runs the command without a shell, in the config file's folder, and compares its output the same way as an HTTP body. Empty output counts as an empty list, since `sqlite3 -json` prints nothing when a query finds no rows. This is how AfterHook watches a SQLite database without a database driver. The `sqlite3` program must be installed where AfterHook runs, and `-json` gives output that can be compared row by row. A row that changes is reported by its position and field (`0.status`), and a new row at the end as added. A command that fails, or doesn't finish within `timeout`, is logged and tried again next time, without an event. On timeout the command and anything it started are stopped.

## The first check

The first time a source is checked, its current state becomes the baseline, and no event is sent. The same happens when a source's definition changes, such as a new path or command, so a config edit never floods a webhook.

## Half-written files

A change is only reported when the source is complete:

- A file is read twice, `settle` apart. If the two reads differ, it is still being written, and it's checked again next time.
- A JSON, YAML, TOML or `.env` file that doesn't read is treated the same way. A half-written JSON document never reads; YAML, TOML and `.env` can read when cut short, so the next rule covers them.
- A source that disappears, or a JSON, YAML, TOML or `.env` file that loses more than half its size, is only reported when the next check sees the same thing. That check comes after the source's interval, or after a second if the interval is longer. A file replaced by delete-and-write, or rewritten in two steps, gives no event when it ends up as it was.
- In a folder, a new or changed file must keep the same size and modification time across `settle` before it's hashed. A file still being written is left out until it settles, and the rest of the folder is reported as usual.

## Events

Each change is POSTed as JSON:

```json
{
  "id": "4091f2c0b187cb2590e4e757",
  "source": "order-status",
  "kind": "file",
  "sequence": 1,
  "time": "2026-10-21T12:25:52Z",
  "changes": [
    {"op": "changed", "path": "status", "old": "open", "new": "paid"}
  ]
}
```

- `op` is `added`, `removed` or `changed`. `path` is a field by dot path, a file name in a folder, `status` for an HTTP status, `line` for text compared line by line, or empty for the whole source. At most 100 changes are listed per event, followed by one that says how many more there were.
- `sequence` counts the changes of one source: 1, 2, 3.
- `id` is derived from the source, the sequence, the content before and after, and a random value chosen with each baseline. A change found again after a crash, before the state was saved, gets the same id, and a new baseline never repeats an old one.
- Values under keys that look secret (`password`, `token`, `api_key`, `secret`, `auth` and similar, as in [AfterConfig](config.md#secrets)) are sent as `REDACTED`. So are user names and passwords inside URLs. In text compared line by line, the same goes for pairs written as `key = value`, `key: value`, `"key": "value"` or `key=value` in a query string.

### Headers and signature

| Header | Value |
| --- | --- |
| `Content-Type` | `application/json` |
| `User-Agent` | `aftermarket-hook/VERSION` |
| `X-Aftermarket-Event` | the event id |
| `X-Aftermarket-Timestamp` | Unix seconds when this attempt was sent |
| `X-Aftermarket-Signature` | `sha256=` and the hex HMAC-SHA256 of `TIMESTAMP.BODY` with the secret, when `secret_env` is set |

To check a delivery, compute the HMAC over the timestamp header, a dot and the raw body, and compare it with the signature in constant time. Reject old timestamps so a captured request can't be replayed later. [`examples/legacy-app/receiver`](../examples/legacy-app/receiver/main.go) is a small receiver that does both and drops repeated event ids.

The secret is read from the environment when `run` starts, and `run` stops if it isn't set. It never goes in the config, the state file or the log.

## Delivery

Delivery is at least once:

- Any 2xx answer means delivered. Anything else, or no answer within the webhook's `timeout`, is a failure.
- A failed delivery is retried after `backoff`, then twice as long each time, up to `max_backoff`. By default it's retried until it's delivered.
- Each webhook gets its events in order. While the oldest waits for a retry, the later ones wait too.
- With `max_attempts` set, an event that still fails after that many attempts is moved to the dead-letter file, `STATE.dead.jsonl` next to the state file, and the next one is sent.
- When a webhook is removed from the config, its waiting events go to the dead-letter file, whatever `max_attempts` says.
- Stopping with Ctrl-C during a send doesn't count as an attempt; the event is sent on the next run.
- A webhook named twice in `send_to` still gets each event once.

Because delivery is at least once, a receiver can occasionally get the same event twice, for example when AfterHook stops after the receiver accepted an event but before it saved that. Use the event id to drop repeats.

## The state file

The state file holds each source's last snapshot and the events not yet delivered. It's written in one step after each round of checks and each round of deliveries, readable by its owner only, because snapshots can hold file contents. If AfterHook stops between a delivery and the save after it, that event is sent again next time, which the event id lets a receiver spot. Keep it between runs: it's what makes restarts lose nothing.

If it can't be read, `run` stops rather than start over, since its queue may hold events not yet sent. Move it aside to start from a new baseline.

## Fixture file

```yaml
cases:
  - name: a status change is one field
    source: order-status
    before: '{"order": 1001, "status": "open"}'
    after: '{"order": 1001, "status": "paid"}'
    expect:
      - 'changed status: "open" -> "paid"'

  - name: only the timestamp moved, so there is no event
    source: order-status
    before: '{"status": "open", "updated_at": "09:00"}'
    after: '{"status": "open", "updated_at": "09:10"}'
    expect_none: true
```

Each case names a source from the config, its content before and after, and either the changes the event must list, one per line, in order, or `expect_none: true`. Changes are written as `changed PATH: OLD -> NEW`, `added PATH: NEW` and `removed PATH: OLD`, with values in JSON. Folder sources can't be tested this way.

## The example

[`examples/legacy-app`](../examples/legacy-app) watches a status file, an exports folder and a SQLite table, and comes with a fixture and the receiver. Its tests change all three sources and check that one signed event arrives for each, and nothing on a quiet check.
