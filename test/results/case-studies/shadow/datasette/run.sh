#!/usr/bin/env bash
# Shadow case study: Datasette 0.65.5 (primary) and Datasette 1.0a41 (candidate).
# Usage: run.sh /path/to/aftermarket [workdir]
# Needs python3 with venv and network access to PyPI. Writes results next to
# this script: versions.txt and one folder per run (report.md, report.json,
# stdout.txt, client.txt, command.txt).
set -euo pipefail
AM=$(readlink -f "$1")
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${2:-$(mktemp -d)}
mkdir -p "$WORK"
cd "$WORK"

[ -x old/bin/datasette ] || { python3 -m venv old && old/bin/pip install -q datasette==0.65.5; }
[ -x new/bin/datasette ] || { python3 -m venv new && new/bin/pip install -q --pre datasette==1.0a41; }
python3 "$HERE/make_db.py" parks.db

PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT

old/bin/datasette parks.db -p 9101 >old.log 2>&1 & PIDS+=($!)
old/bin/datasette parks.db -p 9103 >old2.log 2>&1 & PIDS+=($!)
new/bin/datasette parks.db -p 9102 >new.log 2>&1 & PIDS+=($!)
for p in 9101 9102 9103; do
  for i in $(seq 50); do curl -sf "http://127.0.0.1:$p/-/versions.json" >/dev/null && break; sleep 0.2; done
done

{
  "$AM" version
  echo "primary: datasette $(old/bin/datasette --version | awk '{print $3}') on 127.0.0.1:9101 (and a second copy on 9103 for the noise run)"
  echo "candidate: datasette $(new/bin/datasette --version | awk '{print $3}') on 127.0.0.1:9102"
  python3 --version
  echo "sqlite $(python3 -c 'import sqlite3; print(sqlite3.sqlite_version)')"
} > "$HERE/versions.txt"

# run NAME CANDIDATE_PORT ROUNDS [shadow flags...]
run() {
  local name=$1 cport=$2 rounds=$3; shift 3
  local out="$HERE/$name"
  rm -rf "$out"; mkdir -p "$out"
  local cmd=("$AM" shadow --primary http://127.0.0.1:9101 --candidate "http://127.0.0.1:$cport"
             --listen 127.0.0.1:8190 --report "$out/report.md" --json "$out/report.json" --audit-log "" "$@")
  printf '%q ' "${cmd[@]}" | sed "s#$AM#aftermarket#; s#$out/##g; s#$HERE/##g" > "$out/command.txt"; echo >> "$out/command.txt"
  "${cmd[@]}" > "$out/stdout.txt" 2>&1 & local sp=$!
  for i in $(seq 50); do curl -sf http://127.0.0.1:8190/_aftermarket/healthz >/dev/null && break; sleep 0.1; done
  python3 "$HERE/drive.py" http://127.0.0.1:8190 "$rounds" "$HERE/paths.txt" > "$out/client.txt"
  sleep 1
  kill -INT "$sp"; wait "$sp" || true
  sed -i "s#$out/##g" "$out/stdout.txt"
}

# 1. Noise first: the same version on both sides.
run noise 9103 1
# 2. The new version, nothing ignored.
run raw 9102 1
# 3. The new version with the noise ignored, JSON only, Location compared.
run final 9102 1 --config "$HERE/shadow.yaml" \
  --ignore query_ms --ignore next_url --ignore 'suggested_facets.*.toggle_url' \
  --ignore 'facet_results.*.results.*.toggle_url' \
  --header Content-Type --header Location

# Where the redirect for a ?sql= query leads, followed once by hand.
{
  echo '$ curl -si "http://127.0.0.1:9102/parks.json?sql=select+name+from+parks+where+area_km2+>+5000&_shape=array" | grep -i -E "^HTTP|^location"'
  curl -si "http://127.0.0.1:9102/parks.json?sql=select+name+from+parks+where+area_km2+>+5000&_shape=array" | tr -d '\r' | grep -i -E '^HTTP|^location'
  echo '$ curl -sL "http://127.0.0.1:9102/parks.json?sql=select+name+from+parks+where+area_km2+>+5000&_shape=array"'
  curl -sL "http://127.0.0.1:9102/parks.json?sql=select+name+from+parks+where+area_km2+>+5000&_shape=array"; echo
  echo '$ curl -s "http://127.0.0.1:9101/parks.json?sql=select+name+from+parks+where+area_km2+>+5000&_shape=array"'
  curl -s "http://127.0.0.1:9101/parks.json?sql=select+name+from+parks+where+area_km2+>+5000&_shape=array"; echo
} > "$HERE/redirect.txt"
echo "done: results in $HERE"
