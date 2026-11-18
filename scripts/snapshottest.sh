#!/usr/bin/env bash
# Initial snapshot under live writes (milestone M7).
#
#   scripts/snapshottest.sh [ROWS] [RUNS]
#
# Each run: load ROWS orders, start a writer that never stops, start a fresh
# engine (it snapshots while the writer commits), wait for it to be ready,
# let it catch up, pause the writer, run the differential check. Any gap or
# double count at the snapshot/stream handoff shows up as a mismatch.
set -euo pipefail
ROWS=${1:-1000000}
RUNS=${2:-20}
PORT=${DV_TEST_PORT:-18082}
WORK=${DV_TEST_DIR:-/tmp/deltaview-snapshottest}
BIN=${BIN:-./bin}
VIEWS=${VIEWS:-testdata/views_large.sql}

go build -o "$BIN/" ./cmd/deltaview ./cmd/dvtest
mkdir -p "$WORK"
for run in $(seq 1 "$RUNS"); do
  seed=$((100 + run))
  "$BIN/dvtest" --reset --seed "$seed" --initial 60 --bulk "$ROWS" >/dev/null
  "$BIN/dvtest" --seed "$seed" --views "$VIEWS" --txns 0 --check-every 0 --pause 1ms >"$WORK/writer.log" 2>&1 &
  writer=$!
  sleep 0.5
  rm -rf "$WORK/state"
  start=$(date +%s.%N)
  "$BIN/deltaview" --views "$VIEWS" --state-dir "$WORK/state" --http "127.0.0.1:$PORT" >"$WORK/engine-$run.log" 2>&1 &
  engine=$!
  until curl -sf "127.0.0.1:$PORT/status" | grep -q '"ready":true'; do
    kill -0 "$engine" || { echo "engine died"; tail "$WORK/engine-$run.log"; exit 1; }
    sleep 0.2
  done
  ready=$(date +%s.%N)
  sleep 2 # catch up while the writer keeps going
  kill -TERM "$writer"
  wait "$writer" || true
  set +e
  "$BIN/dvtest" --check-only --seed "$seed" --views "$VIEWS" --engine "http://127.0.0.1:$PORT"
  code=$?
  set -e
  kill "$engine"; wait "$engine" 2>/dev/null || true
  if [ "$code" -ne 0 ]; then
    echo "snapshottest run $run FAILED (log $WORK/engine-$run.log)"
    exit "$code"
  fi
  printf 'snapshottest run %d/%d passed: %s rows, snapshot took %.1fs, writer committed %s\n' \
    "$run" "$RUNS" "$ROWS" "$(echo "$ready - $start" | bc)" "$(tail -n 1 "$WORK/writer.log" | cut -d, -f1)"
done
echo "snapshottest: $RUNS of $RUNS runs passed with $ROWS rows and a live writer"
