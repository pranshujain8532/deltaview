#!/usr/bin/env bash
# Runs the differential tester for one or more seeds. For each seed: reset
# the database (with initial rows, so the snapshot path is tested), start a
# fresh engine, run the random workload with periodic checks, stop.
#
#   scripts/difftest.sh "1 2 3" [TXNS] [CHECK_EVERY]
set -euo pipefail
SEEDS=${1:-1}
TXNS=${2:-1000}
CHECK_EVERY=${3:-10}
PORT=${DV_TEST_PORT:-18080}
WORK=${DV_TEST_DIR:-/tmp/deltaview-difftest}
BIN=${BIN:-./bin}

go build -o "$BIN/" ./cmd/deltaview ./cmd/dvtest
mkdir -p "$WORK"
pass=0
for seed in $SEEDS; do
  "$BIN/dvtest" --reset --seed "$seed" --initial 60 >/dev/null
  rm -rf "$WORK/state"
  "$BIN/deltaview" --state-dir "$WORK/state" --http "127.0.0.1:$PORT" >"$WORK/engine-$seed.log" 2>&1 &
  engine=$!
  set +e
  "$BIN/dvtest" --seed "$seed" --txns "$TXNS" --check-every "$CHECK_EVERY" --engine "http://127.0.0.1:$PORT"
  code=$?
  set -e
  kill "$engine" 2>/dev/null || true
  wait "$engine" 2>/dev/null || true
  if [ "$code" -ne 0 ]; then
    echo "seed $seed FAILED (engine log: $WORK/engine-$seed.log)"
    tail -n 20 "$WORK/engine-$seed.log"
    exit "$code"
  fi
  pass=$((pass + 1))
done
echo "difftest: $pass seed(s) passed"
