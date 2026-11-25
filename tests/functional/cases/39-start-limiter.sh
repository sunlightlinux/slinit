#!/bin/sh
# Test: --parallel-start-limit caps how many services are starting at
# once.
#
# The harness boots PID 1 with no flags, so the case runs a nested
# `slinit --user --parallel-start-limit 2` whose boot pulls in four
# scripted services. Each one's start takes 1.5 s and logs "+" when it
# begins and "-" when it ends; the most "+" outstanding at any moment
# must be 2, and all four must finish. Without the limit it is 4. (The
# case used to check only that three services started under PID 1,
# which never had the flag.)

NEST_ROOT=/tmp/functional-startlimit
NEST_SOCK="$NEST_ROOT/sock"
NEST_SVCS="$NEST_ROOT/svcs"
LOG="$NEST_ROOT/events"

mkdir -p "$NEST_SVCS"
: > "$LOG"
for s in a b c d; do
    cat > "$NEST_SVCS/lim-$s" <<EOF
type = scripted
command = /bin/sh -c "echo + >> $LOG; sleep 1.5; echo - >> $LOG"
EOF
done
cat > "$NEST_SVCS/boot" <<'EOF'
type = internal
waits-for: lim-a
waits-for: lim-b
waits-for: lim-c
waits-for: lim-d
EOF

setsid /sbin/slinit --user \
    --socket-path "$NEST_SOCK" \
    --services-dir "$NEST_SVCS" \
    --log-file "$NEST_ROOT/log" \
    --parallel-start-limit 2 \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!

# Two waves of 1.5 s, plus start-up.
_e=0
while [ "$_e" -lt 20 ] && [ "$(grep -c '^-' "$LOG")" -lt 4 ]; do
    sleep 0.5; _e=$((_e + 1))
done

assert_eq "$(grep -c '^-' "$LOG")" "4" "all four services finished starting"

peak=$(awk '/^\+/ { n++; if (n > m) m = n } /^-/ { n-- } END { print m + 0 }' "$LOG")
echo "INFO: at most $peak starts in flight"
assert_eq "$peak" "2" "no more than 2 services were starting at once"

kill "$NEST_PID" 2>/dev/null
test_summary
