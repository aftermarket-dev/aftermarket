#!/usr/bin/env bash
# Shadow case study: json-server 0.17.4 (primary) and json-server 1.0.0-beta.15
# (candidate, a rewrite), with writes copied to a candidate that has its own
# data file.
# Usage: run.sh /path/to/aftermarket [workdir]
# Needs node and npm with network access to the npm registry, and python3.
set -euo pipefail
AM=$(readlink -f "$1")
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${2:-$(mktemp -d)}
mkdir -p "$WORK"
cd "$WORK"

[ -x old/node_modules/.bin/json-server ] || { mkdir -p old && (cd old && npm init -y >/dev/null && npm i -s json-server@0.17.4 >/dev/null); }
[ -x new/node_modules/.bin/json-server ] || { mkdir -p new && (cd new && npm init -y >/dev/null && npm i -s json-server@1.0.0-beta.15 >/dev/null); }

PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT

# start NAME DIR PORT: a fresh copy of db.json for each server
start() {
  cp "$HERE/db.json" "$1.json"
  node "$2/node_modules/.bin/json-server" "$1.json" -p "$3" > "$1.log" 2>&1 & PIDS+=($!)
  for i in $(seq 50); do curl -sf "http://127.0.0.1:$3/profile" >/dev/null && break; sleep 0.2; done
}
stop_all() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; PIDS=()
  for i in $(seq 50); do curl -s -o /dev/null http://127.0.0.1:9201/ || curl -s -o /dev/null http://127.0.0.1:9202/ || break; sleep 0.1; done
}

{
  "$AM" version
  echo "primary: json-server $(node -p 'require("./old/node_modules/json-server/package.json").version') on 127.0.0.1:9201, data file primary.json"
  echo "candidate: json-server $(node -p 'require("./new/node_modules/json-server/package.json").version') on 127.0.0.1:9202, data file candidate.json (a separate copy)"
  echo "node $(node --version)"
  python3 --version
} > "$HERE/versions.txt"

# run NAME CANDIDATE_DIR [shadow flags...]
run() {
  local name=$1 cdir=$2; shift 2
  local out="$HERE/$name"
  rm -rf "$out"; mkdir -p "$out"
  stop_all
  start primary old 9201
  start candidate "$cdir" 9202
  local cmd=("$AM" shadow --primary http://127.0.0.1:9201 --candidate http://127.0.0.1:9202
             --listen 127.0.0.1:8290 --report "$out/report.md" --json "$out/report.json" --audit-log "" "$@")
  printf '%q ' "${cmd[@]}" | sed "s#$AM#aftermarket#; s#$out/##g; s#$HERE/##g" > "$out/command.txt"; echo >> "$out/command.txt"
  "${cmd[@]}" > "$out/stdout.txt" 2>&1 & local sp=$!
  for i in $(seq 50); do curl -sf http://127.0.0.1:8290/_aftermarket/healthz >/dev/null && break; sleep 0.1; done
  python3 "$HERE/client.py" http://127.0.0.1:8290 > "$out/client.txt"
  sleep 1
  kill -INT "$sp"; wait "$sp" || true
  sed -i "s#$out/##g" "$out/stdout.txt"
  cp primary.json "$out/primary-after.json"; cp candidate.json "$out/candidate-after.json"
}

# 1. Noise first: 0.17.4 on both sides, writes included.
run noise old --include-writes --workers 1
# 2. Reads only, the default: writes are counted, not sent.
run reads new
# 3. Writes too, in order, to the candidate's own data file.
run writes new --include-writes --workers 1
echo "done: results in $HERE"
