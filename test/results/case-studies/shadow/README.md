# Shadow case studies

Real runs of `aftermarket shadow` in front of two released versions of open source services, used by the case study posts on aftermarket.dev. Every number in those posts comes from the files here.

- `datasette/`: Datasette 0.65.5 as the primary, 1.0a41 as the candidate, read traffic on a small SQLite database. `noise/` puts 0.65.5 on both sides to find the fields that differ on their own, `raw/` compares the two versions with nothing ignored, `final/` ignores the noise and copies only the JSON API. `redirect.txt` follows the one redirect by hand.
- `json-server/`: json-server 0.17.4 as the primary, 1.0.0-beta.15 as the candidate, an old client that reads and writes. `noise/` is 0.17.4 on both sides with writes, `reads/` the default (writes counted, not sent), `writes/` with `--include-writes --workers 1` and a separate data file for the candidate. `*-after.json` are both data files after the run.
- `load/`: the Datasette pair under 8 keep-alive clients: a direct baseline, shadow with the defaults, with `--sample 0.1`, and with a bigger queue.

Each folder has `versions.txt`, and each run has `command.txt` (the shadow command), `client.txt` (what the client got), `stdout.txt`, `report.md` and `report.json`.

Rerun with `./run-all.sh /path/to/aftermarket`, or one case's `run.sh`. The services are installed from PyPI and npm into a work folder. Timings and the drop counts under load depend on the machine; these were recorded on a shared two-core Linux workspace with the downloaded `aftermarket` 0.10.1.
