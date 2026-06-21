#!/bin/sh
# 45-capabilities — slinit's `capabilities = cap_a cap_b ...` configures
# the AMBIENT set (pkg/process/params.go:178 AmbientCaps →
# SysProcAttr.AmbientCaps). It is a positive list, not systemd-style
# bounding-set drop. To prove a cap was withheld, configure a small
# list that EXCLUDES it and read /proc/PID/status: CapAmb must contain
# the configured bits and *only* those (mod bit-31 quirks aside).
#
# Test fixture: ambient = cap_net_admin (bit 12). cap_net_raw (bit 13)
# must NOT appear in CapAmb. CapEff = CapPrm = CapAmb after execve as
# a non-root user, so the same check on CapEff confirms the effective
# capability set is also clipped.

SVC="acceptance-test-caps"
MARK="/run/acceptance-test-caps.status"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

rm -f "$MARK"
: > "$MARK"
chmod 0666 "$MARK"

_nobody_uid=$(getent passwd nobody | awk -F: '{print $3}')
if [ -z "$_nobody_uid" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cannot resolve nobody — capabilities semantics require non-root child"
    test_summary
    exit 1
fi

# Need run-as=nobody — ambient caps don't visibly narrow Permitted
# when the child is root (kernel keeps root's full Permitted across
# execve regardless of ambient configuration).
svc_deploy "$SVC" <<EOF
type = process
run-as = nobody
capabilities = cap_net_admin
command = /bin/sh -c 'grep -E "^(CapAmb|CapEff|CapPrm|CapBnd):" /proc/self/status > $MARK; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: child never produced /proc/self/status excerpt"
    test_summary
    exit 1
fi
echo "OK: child stamped its /proc/self/status"
echo "$(cat "$MARK")" | sed 's/^/  caps: /'

# CapAmb hex: just bit 12 (CAP_NET_ADMIN) → 0x0000000000001000.
# Linux pretty-prints CapAmb in 16-hex-digit form.
_amb=$(awk '/^CapAmb:/ {print $2}' "$MARK")
assert_eq "$_amb" "0000000000001000" "CapAmb = only CAP_NET_ADMIN (bit 12)"

# CapEff: same set after execve as nobody (effective shrinks to
# ambient when the new credential isn't root).
_eff=$(awk '/^CapEff:/ {print $2}' "$MARK")
assert_eq "$_eff" "0000000000001000" "CapEff = only CAP_NET_ADMIN (bit 12)"

# Sanity: bit 13 (CAP_NET_RAW = 0x2000) must not be set in CapEff.
_TESTS_RUN=$((_TESTS_RUN + 1))
_bit13=$(printf '%d' "0x$_eff" 2>/dev/null || echo 0)
_bit13=$(( (_bit13 >> 13) & 1 ))
if [ "$_bit13" = "0" ]; then
    echo "OK: CAP_NET_RAW (bit 13) not in CapEff"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: CapEff still has CAP_NET_RAW (bit 13): $_eff"
fi

svc_remove "$SVC"
rm -f "$MARK"
: > "$MARK"
chmod 0666 "$MARK"

# --- Sub-case B: capability-bounding-set narrows CapBnd -------------
# `capabilities = ...` only sets ambient — CapBnd stays full unless
# explicitly narrowed. capability-bounding-set is a positive keep list:
# every cap not on it is PR_CAPBSET_DROP'd by slinit-runner. Verify
# CapBnd is exactly bit 12 (cap_net_admin) after the drop.
svc_deploy "$SVC" <<EOF
type = process
run-as = nobody
capability-bounding-set = cap_net_admin
command = /bin/sh -c 'grep -E "^Cap(Bnd|Amb|Eff):" /proc/self/status > $MARK; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED (bounding-set)"

sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: bounding-set probe never produced status excerpt"
    test_summary
    exit 1
fi
echo "$(cat "$MARK")" | sed 's/^/  caps: /'

_bnd=$(awk '/^CapBnd:/ {print $2}' "$MARK")
assert_eq "$_bnd" "0000000000001000" "CapBnd narrowed to CAP_NET_ADMIN only"

# Spot-check: CAP_NET_RAW (bit 13) explicitly missing from CapBnd.
_TESTS_RUN=$((_TESTS_RUN + 1))
_bit13bnd=$(printf '%d' "0x$_bnd" 2>/dev/null || echo 0)
_bit13bnd=$(( (_bit13bnd >> 13) & 1 ))
if [ "$_bit13bnd" = "0" ]; then
    echo "OK: CAP_NET_RAW dropped from CapBnd"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: CapBnd still has CAP_NET_RAW: $_bnd"
fi

test_summary
