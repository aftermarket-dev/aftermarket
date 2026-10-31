#!/usr/bin/env bash
# Runs the recipes from the "use examples" post that the case studies don't
# already cover. Usage: run.sh /path/to/aftermarket [workdir]
# Needs go (to build examples/customer-api from this repo), and the work
# folders of ../datasette and ../json-server (it reuses their installs if
# given the same workdir layout as run-all.sh, or installs them again).
set -euo pipefail
AM=$(readlink -f "$1")
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../../../../.." && pwd)
WORK=${2:-$(mktemp -d)}
mkdir -p "$WORK"; cd "$WORK"
PIDS=()
trap 'for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done' EXIT
wait_up() { for i in $(seq 50); do curl -s -o /dev/null "$1" && return 0; sleep 0.2; done; return 1; }

# shadow_run NAME PRIMARY CANDIDATE CLIENT_CMD [shadow flags...]
shadow_run() {
  local name=$1 primary=$2 candidate=$3 client=$4; shift 4
  local out="$HERE/$name"; rm -rf "$out"; mkdir -p "$out"
  local cmd=("$AM" shadow --primary "$primary" --candidate "$candidate"
             --listen 127.0.0.1:8390 --report "$out/report.md" --json "$out/report.json" --audit-log "$out/audit.jsonl" "$@")
  printf '%q ' "${cmd[@]}" | sed "s#$AM#aftermarket#; s#$out/##g; s#$HERE/##g; s#$REPO/##g" > "$out/command.txt"; echo >> "$out/command.txt"
  "${cmd[@]}" > "$out/stdout.txt" 2>&1 & local sp=$!
  wait_up http://127.0.0.1:8390/_aftermarket/healthz
  bash -c "$client" > "$out/client.txt" 2>&1
  sleep 1
  kill -INT "$sp"; wait "$sp" || true
  sed -i "s#$out/##g" "$out/stdout.txt"
}

{ "$AM" version; go version; } > "$HERE/versions.txt"

# 1. Redaction: customer-api v1 (primary) and v2 (candidate) with the
#    example's record.yaml; the client sends a token, an API key and emails.
(cd "$REPO" && go build -o "$WORK/cust-v1" ./examples/customer-api/v1 && go build -o "$WORK/cust-v2" ./examples/customer-api/v2)
./cust-v1 -listen 127.0.0.1:9301 > v1.log 2>&1 & PIDS+=($!)
./cust-v2 -listen 127.0.0.1:9302 > v2.log 2>&1 & PIDS+=($!)
wait_up http://127.0.0.1:9301/; wait_up http://127.0.0.1:9302/
shadow_run redaction http://127.0.0.1:9301 http://127.0.0.1:9302 \
  'for id in 7 42; do curl -s -H "Authorization: Bearer s3cret-token" "http://127.0.0.1:8390/api/v1/customer?id=$id&api_key=s3cret-key"; echo; done' \
  --config "$REPO/examples/customer-api/record.yaml"
{
  echo "occurrences of s3cret in report.md, report.json and audit.jsonl:"
  cat "$HERE/redaction/report.md" "$HERE/redaction/report.json" "$HERE/redaction/audit.jsonl" | grep -c s3cret || true
  echo "occurrences of @example in report.md, report.json and audit.jsonl:"
  cat "$HERE/redaction/report.md" "$HERE/redaction/report.json" "$HERE/redaction/audit.jsonl" | grep -c '@example' || true
} > "$HERE/redaction/leak-check.txt"

# 2. Ignore rules for timing and absolute URLs: Datasette 0.65.5 on both
#    sides, JSON only, with the four ignore rules from the case study.
[ -x ds-old/bin/datasette ] || { python3 -m venv ds-old && ds-old/bin/pip install -q datasette==0.65.5; }
python3 "$HERE/../datasette/make_db.py" parks.db
ds-old/bin/datasette parks.db -p 9101 > ds1.log 2>&1 & PIDS+=($!)
ds-old/bin/datasette parks.db -p 9103 > ds3.log 2>&1 & PIDS+=($!)
wait_up http://127.0.0.1:9101/-/versions.json; wait_up http://127.0.0.1:9103/-/versions.json
shadow_run ignore http://127.0.0.1:9101 http://127.0.0.1:9103 \
  "python3 '$HERE/../datasette/drive.py' http://127.0.0.1:8390 1 '$HERE/../datasette/paths.txt'" \
  --config "$HERE/../datasette/shadow.yaml" \
  --ignore query_ms --ignore next_url --ignore 'suggested_facets.*.toggle_url' \
  --ignore 'facet_results.*.results.*.toggle_url'

# 3. Ignoring generated ids: json-server 0.17.4 and 1.0.0-beta.15 with
#    writes, ignoring id at the top and in list items.
[ -x js-old/node_modules/.bin/json-server ] || { mkdir -p js-old && (cd js-old && npm init -y >/dev/null && npm i -s json-server@0.17.4 >/dev/null); }
[ -x js-new/node_modules/.bin/json-server ] || { mkdir -p js-new && (cd js-new && npm init -y >/dev/null && npm i -s json-server@1.0.0-beta.15 >/dev/null); }
cp "$HERE/../json-server/db.json" primary.json; cp "$HERE/../json-server/db.json" candidate.json
node js-old/node_modules/.bin/json-server primary.json -p 9201 > js1.log 2>&1 & PIDS+=($!)
node js-new/node_modules/.bin/json-server candidate.json -p 9202 > js2.log 2>&1 & PIDS+=($!)
wait_up http://127.0.0.1:9201/profile; wait_up http://127.0.0.1:9202/profile
shadow_run ids http://127.0.0.1:9201 http://127.0.0.1:9202 \
  "python3 '$HERE/../json-server/client.py' http://127.0.0.1:8390" \
  --include-writes --workers 1 --ignore id --ignore '*.id'
echo "done: results in $HERE"
