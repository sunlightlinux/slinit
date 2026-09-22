#!/bin/sh
# soak.sh — spawn and shut down slinit-as-PID-1 containers, many times.
#
#   ./tests/container/soak.sh              # 100 cycles
#   CYCLES=500 ./tests/container/soak.sh
#   KEEP_IMAGE=1 ./tests/container/soak.sh # reuse the built image
#
# A single case proves a path works once. Container mode's failures have
# been timing-dependent (a boot service dying before the event loop
# started hung slinit forever; acceptance case 69 was flaky), so this
# runs the lifecycle over and over and reports every cycle that was not
# clean.
#
# Each cycle boots a small but realistic tree — a plain worker, a
# service that leaves orphans for PID 1 to reap, and one that ignores
# SIGTERM so the stop path has to escalate — then stops it the way a
# runtime or an operator would, rotating through `docker stop`,
# SIGINT (Ctrl-C through `docker run`) and SIGRTMIN+4 (poweroff).
#
# A cycle fails if the container is not ready in 10s, does not exit
# within 10s of being told to, exits non-zero, or has to be SIGKILLed by
# the runtime (137). Logs of failed cycles are kept in _build/soak/.
set -u

CT_DIR="$(cd "$(dirname "$0")" && pwd)"
export CT_DIR
. "$CT_DIR/lib.sh"

CYCLES="${CYCLES:-100}"
STOP_BUDGET_MS="${STOP_BUDGET_MS:-5000}"
OUT="$BUILD_DIR/soak"

"$RUNTIME" info >/dev/null 2>&1 || { echo "soak: '$RUNTIME' is not usable" >&2; exit 2; }
if [ "${KEEP_IMAGE:-0}" != "1" ] || ! "$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "building $IMAGE ..."
    build_image || { echo "image build failed" >&2; exit 2; }
fi

SVC=$(new_svcdir)
N=slinit-ct-soak-$$
trap 'ct_rm $N; rm -rf "$SVC"' EXIT
rm -rf "$OUT"
mkdir -p "$OUT"

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: worker
depends-on: orphaner
depends-on: stubborn
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF
cat > "$SVC/orphaner" <<'EOF'
type = process
command = /bin/sh -c "while :; do (sleep 0.05 &); sleep 0.1; done"
EOF
cat > "$SVC/stubborn" <<'EOF'
type = process
command = /bin/sh -c "trap '' TERM; while :; do sleep 1; done"
stop-timeout = 1
EOF

ready_ms="" stop_ms="" failures=0 killed=0
i=1
while [ "$i" -le "$CYCLES" ]; do
    case $((i % 3)) in
        0) how=stop ;;
        1) how=INT ;;
        2) how=RTMIN+4 ;;
    esac

    t0=$(date +%s%N)
    ct_start $N "$SVC"
    if ! ct_wait_ready $N 10; then
        failures=$((failures + 1))
        echo "cycle $i: not ready in 10s"
        ct_logs $N > "$OUT/cycle-$i.log"
        ct_rm $N
        i=$((i + 1))
        continue
    fi
    t1=$(date +%s%N)
    ready_ms="$ready_ms $(((t1 - t0) / 1000000))"

    t0=$(date +%s%N)
    if [ "$how" = stop ]; then
        "$RUNTIME" stop -t 10 $N >/dev/null
    else
        "$RUNTIME" kill -s "$how" $N >/dev/null
        if ! ct_wait_exit $N 10; then
            "$RUNTIME" kill $N >/dev/null 2>&1
        fi
    fi
    t1=$(date +%s%N)
    ms=$(((t1 - t0) / 1000000))
    stop_ms="$stop_ms $ms"

    ec=$(ct_exit_code $N)
    bad=""
    [ "$ec" = "137" ] && { bad="SIGKILLed by the runtime"; killed=$((killed + 1)); }
    [ -z "$bad" ] && [ "$ec" != "0" ] && bad="exit $ec"
    [ -z "$bad" ] && [ "$ms" -gt "$STOP_BUDGET_MS" ] && bad="stop took ${ms}ms"
    if [ -n "$bad" ]; then
        failures=$((failures + 1))
        echo "cycle $i ($how): $bad"
        ct_logs $N > "$OUT/cycle-$i.log"
    fi
    ct_rm $N

    [ $((i % 25)) -eq 0 ] && echo "  ... $i/$CYCLES cycles, $failures failed"
    i=$((i + 1))
done

# pct P LIST — the P-th percentile of a space-separated list of numbers.
pct() {
    echo "$2" | tr ' ' '\n' | grep -v '^$' | sort -n | awk -v p="$1" '
        { v[NR] = $1 }
        END { if (NR == 0) { print "-"; exit }
              i = int((NR - 1) * p / 100) + 1; print v[i] }'
}

echo
echo "Soak: $CYCLES cycles, $failures failed ($killed SIGKILLed by the runtime)"
echo "  ready: p50 $(pct 50 "$ready_ms")ms  p95 $(pct 95 "$ready_ms")ms  max $(pct 100 "$ready_ms")ms"
echo "  stop:  p50 $(pct 50 "$stop_ms")ms  p95 $(pct 95 "$stop_ms")ms  max $(pct 100 "$stop_ms")ms"
[ "$failures" -gt 0 ] && echo "  logs of failed cycles: $OUT/"
[ "$failures" -eq 0 ]
