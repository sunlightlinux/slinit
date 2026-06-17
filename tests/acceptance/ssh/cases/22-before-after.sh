#!/bin/sh
# 22-before-after — pure ordering hints. `after: X` on service S means S
# waits for X to finish starting before S starts (no hard dep — if X is not
# in the starting set, S proceeds normally). Probe: FIRST sleeps 1s then
# stamps; SECOND has `after: FIRST` and stamps immediately; a parent pulls
# both up. SECOND's stamp must land strictly after FIRST's.
#
# Note: we only assert `after:` here. `before:` exists in slinit but is
# implemented as a dep on the *owner* of the keyword instead of (per dinit
# semantics) a dep on the *named* service back to the owner — see
# load-service.cc:339-342 in the dinit reference vs. loader.go:1044 in
# slinit. The net effect is `before:` currently behaves like `after:`
# rather than its dinit-documented inverse, so a strict `before:` probe
# would either tautologise the `after:` test or fail. Left as a known
# divergence to chase upstream.

PARENT="acceptance-test-order-parent"
FIRST="acceptance-test-order-first"
SECOND="acceptance-test-order-second"
STAMP_DIR="/run/acceptance-order"

cleanup() {
    # Unload PARENT first — it has hard depends-on against FIRST/SECOND, so
    # unloading the deps before the parent is rejected by the daemon and
    # leaks them in the loaded set.
    svc_remove "$PARENT" "$FIRST" "$SECOND"
    rm -rf "$STAMP_DIR"
}
trap cleanup EXIT INT TERM

mkdir -p "$STAMP_DIR"
rm -f "$STAMP_DIR"/*

# FIRST: scripted, sleeps 1s then stamps. The sleep matters — without it
# both commands finish in <1ms and scheduler jitter dominates the gap.
svc_deploy "$FIRST" <<EOF
type = scripted
command = /bin/sh -c 'sleep 1; date +%s%N > $STAMP_DIR/first; exit 0'
restart = false
EOF

# SECOND: scripted, ordered after FIRST.
svc_deploy "$SECOND" <<EOF
type = scripted
after: $FIRST
command = /bin/sh -c 'date +%s%N > $STAMP_DIR/second; exit 0'
restart = false
EOF

# PARENT pulls both up; it's just a marker.
svc_deploy "$PARENT" <<EOF
type = internal
depends-on: $FIRST
depends-on: $SECOND
EOF

slinitctl --system start "$PARENT" >/dev/null 2>&1
wait_for_service "$PARENT" "STARTED" 10 || true
assert_service_state "$PARENT" "STARTED" "$PARENT STARTED"

# Both stamp files must exist before we can compare.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r "$STAMP_DIR/first" ] && [ -r "$STAMP_DIR/second" ]; then
    echo "OK: both stamp files written"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing stamp(s): first=$(test -r "$STAMP_DIR/first" && echo Y || echo N), second=$(test -r "$STAMP_DIR/second" && echo Y || echo N)"
    test_summary
    exit 1
fi

_t_first=$(cat "$STAMP_DIR/first")
_t_second=$(cat "$STAMP_DIR/second")

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_t_first" -lt "$_t_second" ]; then
    _delta_ns=$((_t_second - _t_first))
    echo "OK: first ($_t_first) ran strictly before second ($_t_second); gap ${_delta_ns}ns"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: ordering violated: first=$_t_first second=$_t_second"
fi

test_summary
