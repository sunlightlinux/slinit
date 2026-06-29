#!/bin/sh
# 77-manual-start — `manual = yes` opt-out of auto-start.
#
# pkg/service/types.go ManualStart: when true, the service does NOT
# auto-start, even if pulled in by a depends-on / waits-for / chain-to
# edge. The operator must `slinitctl start` it explicitly. The use case
# is maintenance services (one-shot disk checks, hand-driven migrations)
# that should never run as part of the regular boot graph.
#
# This case validates two scenarios:
#   1. A leaf service marked manual=yes that a parent depends-on points
#      at: starting the parent must NOT pull the manual leaf up; the
#      parent should fall back to starting on its own (manual deps are
#      treated like waits-for soft deps when manual=yes — they don't
#      block).
#   2. Explicit `slinitctl start` on the manual service brings it up
#      and the marker proves the command actually ran.

PARENT="acceptance-test-manual-parent"
MANUAL="acceptance-test-manual-leaf"
PARFILE="/etc/slinit.d/$PARENT"
MANFILE="/etc/slinit.d/$MANUAL"
MARKER="/tmp/acceptance-manual.mark"

cleanup() {
    slinitctl --system stop "$PARENT" 2>/dev/null
    slinitctl --system stop "$MANUAL" 2>/dev/null
    slinitctl --system unload "$PARENT" 2>/dev/null
    slinitctl --system unload "$MANUAL" 2>/dev/null
    rm -f "$PARFILE" "$MANFILE" "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup

cat > "$MANFILE" <<EOF
type = process
command = /bin/sh -c 'touch $MARKER; exec sleep 600'
manual = yes
restart = false
EOF

# Parent runs a short clean command then declares chain-to → MANUAL.
# A regular chain target would auto-start on the parent's exit; the
# manual flag must refuse that auto-activation, which is the whole
# point of the directive. Parent uses always-chain so the trigger
# fires regardless of exit status — but the manual flag should still
# win.
cat > "$PARFILE" <<EOF
type = scripted
command = /bin/true
chain-to = $MANUAL
options = always-chain
restart = false
EOF

# --- Probe 1: parser accepts manual=yes -----------------------------
_chk=$(slinit-check -d /etc/slinit.d "$MANUAL" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinit-check accepts manual=yes"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: parser rejected manual=yes:"; echo "$_chk" | sed 's/^/  | /'
fi

# --- Probe 2: running the parent must NOT chain into the manual leaf ----
# Scripted parent exits clean; without manual=yes on the leaf, the
# always-chain edge would pull the leaf up. The leaf MUST refuse.
slinitctl --system start "$PARENT" >/dev/null 2>&1
sleep 2

_mst=$(slinitctl --system status "$MANUAL" 2>/dev/null | awk '/State:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_mst" = "STOPPED" ]; then
    echo "OK: manual leaf stayed STOPPED despite the chain-to edge"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: manual leaf in '$_mst' — manual=yes ignored by chain-to?"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARKER" ]; then
    echo "OK: leaf's command never ran (marker absent)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker $MARKER appeared — leaf command ran"
fi

# --- Probe 3: explicit start overrides manual=yes -------------------
slinitctl --system start "$MANUAL" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _mst=$(slinitctl --system status "$MANUAL" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_mst" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_mst" = "STARTED" ]; then
    echo "OK: explicit start brought the manual leaf up"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: leaf stuck at '$_mst' after explicit start"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARKER" ]; then
    echo "OK: marker present — explicit start actually ran the command"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker still missing despite STARTED"
fi

test_summary
