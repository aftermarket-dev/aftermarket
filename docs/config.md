# AfterConfig reference

`aftermarket config` changes a vendor's configuration file through an overlay, without ever editing the file itself. The [README](../README.md) explains what the tool is for.

The idea: the vendor's file belongs to the vendor, and the next upgrade will overwrite it. Your changes live in a separate overlay file. `config apply` reads the vendor's file, applies the overlay and writes the result somewhere else, such as the path the app reads or a file a deploy step copies into place. Each change can say what value it expects to find first. When an upgrade changes that value, apply stops and tells you, instead of quietly overriding what the vendor changed.

## Commands

```text
aftermarket config check --overlay overlay.yaml [--base FILE]
aftermarket config diff  --overlay overlay.yaml [--base FILE]
aftermarket config apply --overlay overlay.yaml --out FILE [--base FILE]
aftermarket config test  --overlay overlay.yaml --fixture fixture.yaml
```

- **`check`** reads the overlay and the vendor's file, runs every change in memory, and lists them. Every problem is listed at once, by line. Exit 1 when there's any.
- **`diff`** shows field by field what the result changes: `~ server.timeout: 30 -> 120`, `+ server.max_body: "10MiB"`, `- telemetry`.
- **`apply`** writes the result to `--out`. It writes a temporary file in the same folder and renames it into place, so the output is never half written. The output gets the vendor file's permissions. When any change can't be applied, nothing is written and the old output stays as it was.
- **`test`** runs the overlay on the bases in a fixture file and compares the results with what you expect.
- `--base` reads a different vendor file than the one the overlay names, for example the next version before you upgrade.

Exit codes: 0 when everything applies, 1 for problems in the overlay or with the base, 2 for usage errors.

### What apply never does

- **It never writes the vendor's file.** If `--out` names it, whether directly, through a link or by another spelling of the path, apply refuses.
- **It never changes what the overlay doesn't name.** Other fields keep their place and their exact value. When every change is already in place, the output is the vendor's file byte for byte, whatever its layout.
- **It never writes something it can't read back.** The result is read back before it's written, and if it doesn't hold exactly what was meant, nothing is written.
- **It never leaves a half-written output.** Applying twice gives the same bytes.

## Overlay file

```yaml
version: 1                       # optional; must be 1
base: 1.0/config.json            # required; relative to the overlay's folder
format: json                     # optional; json, yaml, toml or env
redact: [database.url]           # optional; values never printed

changes:
  - set: {path: server.timeout, value: 120, expect: 30}
  - set: {path: server.max_body, value: 10MiB, expect_absent: true}
  - delete: {path: telemetry.endpoint}
  - rename: {from: log_level, to: logging.level}
  - copy: {from: server.port, to: health.port}
```

Without `format:`, the format comes from the base's name: `.json`, `.yaml` or `.yml`, `.toml`, and `.env` or names that start with `.env.` (such as `.env.production`). Unknown keys are errors, and `check` lists every problem with its line number.

### Changes

Changes run in the order written, and each one sees the result of the ones before. Paths are dot-separated, and a number selects a list element: `servers.0.host`. Keys that contain a dot can't be addressed yet.

| Change | Form | Effect |
| --- | --- | --- |
| `set` | `{path: a.b, value: 1}` | Sets a value. Missing objects along the way are created. |
| `delete` | `{path: a.b}` | Removes a key or list element. It must exist. |
| `rename` | `{from: a, to: b}` | Moves a value. Within the same object, the key keeps its place. `to` must not exist yet. |
| `copy` | `{from: a, to: b}` | Copies a value. `to` must not exist yet. |

`rename` and `copy` never overwrite a value that's already there: write a `delete` first if that's what you mean.

Values are written in YAML. Unquoted `true` and `false` are booleans, unquoted `null` and `~` are null, unquoted numbers in JSON's own format (`3`, `-12`, `2.50`, `1e3`) are numbers that keep their exact text, and everything else is text. Lists and mappings become lists and objects. A number too large for JSON readers, such as `1e400`, is an error.

### Guards

| Guard | Means |
| --- | --- |
| `expect: VALUE` | The value at `path` (or at `from`, for `rename` and `copy`) must be VALUE before the change runs. |
| `expect_absent: true` | `path` must not exist yet. Only for `set`. |

Values are compared by meaning: `30` and `30.0` are the same, and so are two objects with their keys in a different order. Guards are optional, but a `set` without one will override whatever the vendor ships, including a new default you might have wanted. That's why the example guards almost every change.

When a guard fails, the message names the line, what the overlay expected and what the base has:

```text
line 8: set expects server.timeout to be 30, but the base has 60
```

Usually that means the vendor changed something on purpose. Decide whether your change still makes sense, then update the `expect`, or drop the change.

### Secrets

Values are never printed when any part of their path contains `password`, `passwd`, `pass`, `pwd`, `secret`, `token`, `credential`, `auth`, `apikey`, `api_key`, `api-key`, `private`, `access_key`, `accesskey`, `dsn`, `cookie`, `session` or `signature`, in any letter case. The same goes for paths listed under `redact:` and everything inside them, and for a secret that `rename` or `copy` moves to another path. A user name and password inside a URL, as in `postgres://app:pw@db/app`, show as `postgres://REDACTED@db/app`. They show as `REDACTED` in `check`, `diff`, problem messages and `config test` failures. An object or list that holds such a value shows as `an object` or `a list`. The result written by `apply` has the real values, of course.

## Formats

### JSON

Keys keep their order, and numbers keep their exact text. When something changes, the file is written again with the vendor file's indentation (two spaces, four, a tab, or none for a one-line file), its line endings (`\n` or `\r\n`) and its final newline or lack of one. In an indented file, lists are written one item per line. Strings keep their value, though an escape such as `\u00e9` is written as the character itself. So a file laid out by hand may come out laid out differently, with the same content.

### YAML

AfterConfig reads the YAML subset the other Aftermarket tools read: mappings, lists, quoted and plain values, one-line `[ ]` and `{ }` collections, `|` and `>` blocks, and comments. Anchors, aliases, tags and multiple documents are rejected with a line number.

- **Comments are not kept in the output.** The vendor's file still has them.
- Values keep their order and meaning. An unquoted value the overlay doesn't touch is written back exactly as it was, so an unquoted `yes` stays an unquoted `yes` and means the same to any YAML reader. Keys keep their quotes or lack of them too, so `80:` stays a number to readers that type keys. A block (`|` or `>`) is written as a string with the same text, quoted when it spans lines.
- New text values and keys that some YAML reader could take for something else, such as `on`, `no`, `2`, `1.5`, or anything with `:` or `#` in it, are quoted.

### TOML

AfterConfig reads TOML 1.0: tables, arrays of tables, dotted and quoted keys, inline tables, multi-line arrays, every kind of string, integers in decimal, hex, octal and binary, floats, booleans, and dates and times. A file TOML doesn't allow, such as one that sets a key twice or defines a table twice, is refused with a line number.

- **Changes are made on the file's own lines.** A changed value is rewritten in place, keeping the key as written, the alignment and any `# comment` after it. Every line the overlay doesn't touch is written back byte for byte, comments and blank lines included.
- A new key goes at the end of the table section it belongs to, as `key = value`, or as a dotted key such as `features.beta = true` when its table has no section of its own. A deleted key loses its line; a deleted table loses its sections. A rename within one table changes only the key on its line, and renaming a table rewrites its `[header]` lines.
- A path into an inline table or an array, such as `server.tls.enabled` in `tls = { enabled = false }`, rewrites that one line with the new value inside it.
- Paths work as in JSON: `jobs.1.schedule` is the `schedule` of the second `[[jobs]]`.
- Dates and times keep their type: a date replaced with text in the same shape, such as `"2026-10-21T00:00:00Z"`, is written as a TOML date, not a string. Elsewhere, and in guards, dates read as text.
- TOML has no null, and its integers are 64-bit, so `value: null` or a larger integer is refused at the change that asks for it.
- The result is read back before it's written. If an edit can't be made on the lines, such as turning a table into a single value, the whole file is written again from its values instead, still with exactly the intended values, but without its comments. `inf` and `nan` are kept as written.

### .env

One `KEY=value` per line, with optional `export `, `# comments` and blank lines. Values can be bare, `"double quoted"` (with `\n`, `\t`, `\"` and `\\` escapes) or `'single quoted'` (taken as written).

- Every line the overlay doesn't change is written back byte for byte, comments and blank lines included.
- A changed value is rewritten in place, keeping `export` and any `# comment` after it. A renamed key keeps its line, value and comment exactly. New keys go at the end. Files with `\r\n` line endings keep them.
- Rewritten values are quoted so they read back as the same text: bare when simple, `'single quoted'` when they hold spaces or `$`, so nothing gets expanded, and `"double quoted"` with `\$` escaped when they hold a `'` or a line break.
- A path is one key, since the file is flat. Values are text: `set` accepts text, numbers and `true`/`false`, and a guard's `expect: 30` matches `30`.
- A key set twice in the same file is an error. Programs differ on which one wins, so AfterConfig won't guess.

## Fixture file

```yaml
cases:
  - name: the overlay applies to the 1.0 config
    base: |
      {"server": {"timeout": 30, "workers": 4}}
    expect: |
      {"server": {"timeout": 120, "workers": 8}}

  - name: an upgrade that changes the timeout default stops apply
    base: |
      {"server": {"timeout": 60, "workers": 4}}
    expect_error: "expects server.timeout to be 30, but the base has 60"
```

Each case has a `base` in the overlay's format and exactly one of `expect`, the output compared by meaning, or `expect_error`, text that one of the problems must contain. `config test` exits 1 when any case fails, so an overlay can sit in CI next to the app it configures.

## The examples

[`examples/vendor-app`](../examples/vendor-app) has a vendor's `config.json` at versions 1.0 and 1.1, an overlay with seven changes, and a fixture. On 1.0 every change applies. Version 1.1 raised the default timeout from 30 to 60 and switched logging to JSON itself, so on 1.1 apply stops at exactly those two lines and writes nothing:

```text
$ aftermarket config apply --overlay examples/vendor-app/overlay.yaml --base examples/vendor-app/1.1/config.json --out config.json
examples/vendor-app/overlay.yaml: 2 of 7 change(s) can't be applied to examples/vendor-app/1.1/config.json
  line 8: set expects server.timeout to be 30, but the base has 60
  line 14: set expects logging.format to be "text", but the base has "json"
nothing was written
```

[`examples/vendor-app-toml`](../examples/vendor-app-toml) is the same upgrade with a vendor's `config.toml`, comments and arrays of tables included. On 1.0 only the seven changed lines differ from the vendor's file; on 1.1 apply stops at the same two lines.
