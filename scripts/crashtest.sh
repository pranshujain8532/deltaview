#!/usr/bin/env bash
# Crash test for exactly-once processing (milestone M8).
#
#   scripts/crashtest.sh kill [CYCLES]            kill -9 the engine at random times
#   scripts/crashtest.sh failpoint NAME [CYCLES]  let a fail point exit the engine
#
# A writer runs the tester workload the whole time. The engine is killed and
# restarted CYCLES times while it writes, including during the initial
# snapshot. At the end the writer is stopped, the engine catches up, and the
# full differential check must pass: no change lost, none applied twice.
set -euo pipefail
MODE=${1:-kill}
if [ "$MODE" = failpoint ]; then
  FP=${2:?fail point name: before_commit, after_commit or after_ack}
  CYCLES=${3:-30}
else
  FP=""
  CYCLES=${2:-200}
fi
SEED=${SEED:-7}
PORT=${DV_TEST_PORT:-18081}
WORK=${DV_TEST_DIR:-/tmp/deltaview-crashtest}
BIN=${BIN:-./bin}
STATE="$WORK/state"

go build -o "$BIN/" ./cmd/deltaview ./cmd/dvtest
mkdir -p "$WORK"
rm -rf "$STATE"
"$BIN/dvtest" --reset --seed "$SEED" --initial 60 >/dev/null

"$BIN/dvtest" --seed "$SEED" --txns 0 --check-every 0 --pause 2ms >"$WORK/writer.log" 2>&1 &
writer=$!
trap 'kill $writer 2>/dev/null || true' EXIT

fired=0
for i in $(seq 1 "$CYCLES"); do
  if [ -n "$FP" ]; then
    # Fire on a random hit, so the crash lands at a different transaction each time.
    DV_FAILPOINT="$FP" DV_FAILPOINT_AFTER=$((1 + RANDOM % 25)) \
      "$BIN/deltaview" --state-dir "$STATE" --http "127.0.0.1:$PORT" >>"$WORK/engine.log" 2>&1 &
    engine=$!
    for _ in $(seq 1 100); do
      kill -0 "$engine" 2>/dev/null || break
      sleep 0.05
    done
    if kill -0 "$engine" 2>/dev/null; then
      kill -9 "$engine" 2>/dev/null || true
    fi
    set +e; wait "$engine"; code=$?; set -e
    [ "$code" -eq 77 ] && fired=$((fired + 1))
  else
    "$BIN/deltaview" --state-dir "$STATE" --http "127.0.0.1:$PORT" >>"$WORK/engine.log" 2>&1 &
    engine=$!
    # Random lifetime between 0.1 s and 1.6 s.
    sleep "0.$((1 + RANDOM % 9))$((RANDOM % 10))"
    [ $((RANDOM % 4)) -eq 0 ] && sleep 0.7
    kill -9 "$engine" 2>/dev/null || true
    set +e; wait "$engine" 2>/dev/null; set -e
  fi
  if [ $((i % 20)) -eq 0 ]; then echo "crashtest: $i/$CYCLES cycles"; fi
done

kill -TERM "$writer"
wait "$writer" || true
trap - EXIT
echo "crashtest: writer stopped: $(tail -n 1 "$WORK/writer.log")"

"$BIN/deltaview" --state-dir "$STATE" --http "127.0.0.1:$PORT" >>"$WORK/engine.log" 2>&1 &
engine=$!
set +e
"$BIN/dvtest" --check-only --seed "$SEED" --engine "http://127.0.0.1:$PORT"
code=$?
set -e
kill "$engine" 2>/dev/null || true
wait "$engine" 2>/dev/null || true
if [ -n "$FP" ]; then
  echo "crashtest: fail point $FP fired in $fired of $CYCLES cycles"
fi
if [ "$code" -ne 0 ]; then
  echo "crashtest FAILED after $CYCLES cycles (engine log: $WORK/engine.log)"
  exit "$code"
fi
echo "crashtest: $CYCLES cycles ($MODE ${FP}) then a clean differential check"
