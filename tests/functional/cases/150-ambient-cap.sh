#!/bin/sh
# Test: capabilities = cap_net_bind_service sets bit 10 (0x400) in
# the child's ambient capability set. Validated by parsing
# /proc/PID/status CapAmb: (hex) and testing bit 10.
#
# Cross-check: CapEff and CapPrm must also contain the bit — the
# runner raises ambient after PR_SET_KEEPCAPS + setuid, and ambient
# implicitly requires the cap in permitted+inheritable. If CapEff
# lost the bit, the process couldn't actually use it.

wait_for_service "cap-svc" "STARTED" 10
assert_service_state "cap-svc" "STARTED" "cap-svc reached STARTED"

_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status cap-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve PID for cap-svc"
    test_summary
    return
fi
echo "OK: cap-svc pid=$_pid"

# Guard: child must actually be UID 65534 — otherwise the ambient
# raise wouldn't have been the code path exercised (root inherits
# every cap for free and the test wouldn't prove anything).
_uid=$(awk '/^Uid:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)
assert_eq "$_uid" "65534" "child real UID = 65534 (nobody)"

_cap_amb=$(awk '/^CapAmb:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)
_cap_eff=$(awk '/^CapEff:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)
_cap_prm=$(awk '/^CapPrm:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)
echo "OK: CapAmb=$_cap_amb CapEff=$_cap_eff CapPrm=$_cap_prm"

# CAP_NET_BIND_SERVICE = bit 10 → 0x400. POSIX $(( )) accepts a
# leading 0x on 64-bit busybox sh, but 16-char hex masks can push
# into signed-overflow territory (0xFFFF... reads as negative in
# some sh implementations). Guard by masking off the low 16 bits
# via the hex string tail — bit 10 is in the last 3 nibbles.
_has_bit() {
    _mask=$1
    # Take last 4 hex digits — bit 10 (0x400) fits comfortably.
    _tail=$(printf '%s' "$_mask" | tail -c 4)
    _val=$((0x$_tail))
    echo $(( (_val / 1024) % 2 ))
}

assert_eq "$(_has_bit "$_cap_amb")" "1" "CapAmb has bit 10 (CAP_NET_BIND_SERVICE)"
assert_eq "$(_has_bit "$_cap_eff")" "1" "CapEff has bit 10 (implied by ambient)"
assert_eq "$(_has_bit "$_cap_prm")" "1" "CapPrm has bit 10 (implied by ambient)"

test_summary
