#!/usr/bin/env bash
# Reruns every shadow case study. Usage: run-all.sh /path/to/aftermarket [workdir]
# Each case writes its results next to its own script. Needs python3 (venv),
# node and npm, and network access to PyPI and the npm registry.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${2:-$(mktemp -d)}
"$HERE/datasette/run.sh" "$1" "$WORK/datasette"
"$HERE/json-server/run.sh" "$1" "$WORK/json-server"
"$HERE/load/run.sh" "$1" "$WORK/load"
"$HERE/recipes/run.sh" "$1" "$WORK/recipes"
