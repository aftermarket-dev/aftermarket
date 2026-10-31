#!/usr/bin/env bash
# Shadow under load: Datasette 0.65.5 (primary) and 1.0a41 (candidate) on the
# same machine, 8 keep-alive clients, read paths whose answers match in both.
# Usage: run.sh /path/to/aftermarket [workdir]   (reuses ../datasette/make_db.py)
set -euo pipefail
AM=$(readlink -f "$1")
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${2:-$(mktemp -d)}
mkdir -p "$WORK"; cd "$WORK"
[ -x old/bin/datasette ] || { python3 -m venv old && old/bin/pip install -q datasette==0.65.5; }
[ -x new/bin/datasette ] || { python3 -m venv new && new/bin/pip install -q --pre datasette==1.0a41; }
python3 "$HERE/../datasette/make_db.py" parks.db
PIDS=()
trap 'for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done' EXIT
old/bin/datasette parks.db -p 9101 >old.log 2>&1 & PIDS+=($!)
new/bin/datasette parks.db -p 9102 >new.log 2>&1 & PIDS+=($!)
for p in 9101 9102; do for i in $(seq 50); do curl -sf "http://127.0.0.1:$p/-/versions.json" >/dev/null && break; sleep 0.2; done; done
THREADS=8 TOTAL=6000
{ "$AM" version; echo "primary: datasette 0.65.5, candidate: datasette 1.0a41, same machine ($(nproc) cores)"; echo "load: $THREADS keep-alive clients, $TOTAL requests per run"; } > "$HERE/versions.txt"

python3 "$HERE/load.py" 127.0.0.1 9101 $THREADS $TOTAL "$HERE/paths.txt" > "$HERE/direct.txt"

run() {
  local name=$1; shift
  local out="$HERE/$name"; rm -rf "$out"; mkdir -p "$out"
  local cmd=("$AM" shadow --primary http://127.0.0.1:9101 --candidate http://127.0.0.1:9102
             --listen 127.0.0.1:8190 --report "$out/report.md" --json "$out/report.json" --audit-log "" "$@")
  printf '%q ' "${cmd[@]}" | sed "s#$AM#aftermarket#; s#$out/##g; s#$HERE/##g" > "$out/command.txt"; echo >> "$out/command.txt"
  "${cmd[@]}" > "$out/stdout.txt" 2>&1 & local sp=$!
  for i in $(seq 50); do curl -sf http://127.0.0.1:8190/_aftermarket/healthz >/dev/null && break; sleep 0.1; done
  python3 "$HERE/load.py" 127.0.0.1 8190 $THREADS $TOTAL "$HERE/paths.txt" > "$out/client.txt"
  kill -INT "$sp"; wait "$sp" || true
  sed -i "s#$out/##g" "$out/stdout.txt"
}
run defaults
run sample --sample 0.1
run bigqueue --queue 1024 --workers 8
echo "done: results in $HERE"
