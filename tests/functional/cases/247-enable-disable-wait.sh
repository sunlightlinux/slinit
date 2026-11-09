#!/bin/sh
# 247-enable-disable-wait: enable waits for the start and disable for the stop,
# as dinitctl does, and disable leaves up what something else requires.

# From /proc/uptime: the VM's busybox date has no %N, which made every elapsed
# time 0, so the checks could not tell a wait from none.
now_ms() { read -r _u _ < /proc/uptime; echo $(( ${_u%.*}${_u#*.} * 10 )); }
ms_since() { echo $(( $(now_ms) - $1 )); }
check() { # desc, condition result
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$2" = yes ]; then
        echo "OK: $1"
    else
        echo "FAIL: $1"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
    fi
}

# --- enable waits for a slow start ---
t0=$(now_ms)
out=$(slinitctl --system enable slowstart 2>&1)
rc=$?
ms=$(ms_since "$t0")
assert_eq "$rc" "0" "enable of a slow start succeeds"
assert_contains "$out" "started" "enable says it started"
check "enable waited for the start ($ms ms)" "$([ "$ms" -ge 1500 ] && echo yes)"
assert_eq "$(svc_state slowstart)" "STARTED" "started when enable returns"

# --- enable reports a failed start ---
out=$(slinitctl --system enable badstart 2>&1)
rc=$?
check "enable of a failing service exits non-zero (rc=$rc)" "$([ "$rc" -ne 0 ] && echo yes)"
assert_contains "$out" "enabled" "the edge was still added"

# --- disable waits for a slow stop ---
slinitctl --system enable slowstop > /dev/null 2>&1
t0=$(now_ms)
out=$(slinitctl --system disable slowstop 2>&1)
rc=$?
ms=$(ms_since "$t0")
assert_eq "$rc" "0" "disable of a slow stop succeeds"
assert_contains "$out" "stopped" "disable says it stopped"
check "disable waited for the stop ($ms ms)" "$([ "$ms" -ge 1500 ] && echo yes)"
assert_eq "$(svc_state slowstop)" "STOPPED" "stopped when disable returns"

# --- no-wait keeps the old timing ---
slinitctl --system enable slowstop > /dev/null 2>&1
t0=$(now_ms)
slinitctl --system --no-wait disable slowstop > /dev/null 2>&1
ms=$(ms_since "$t0")
check "--no-wait returned at once ($ms ms)" "$([ "$ms" -lt 1000 ] && echo yes)"
wait_for_service "slowstop" "STOPPED" 5

# --- disable leaves up what something else requires ---
slinitctl --system start holder > /dev/null 2>&1
slinitctl --system enable required > /dev/null 2>&1
out=$(slinitctl --system disable required 2>&1)
rc=$?
assert_eq "$rc" "0" "disable of a required service exits 0"
assert_contains "$out" "not stopping" "and says it is not stopping"
assert_eq "$(svc_state required)" "STARTED" "the required service stays up"
assert_eq "$(svc_state holder)" "STARTED" "and so does what requires it"
slinitctl --system stop holder > /dev/null 2>&1
slinitctl --system stop required > /dev/null 2>&1

# --- disable does not end an explicit start ---
slinitctl --system start explicit > /dev/null 2>&1
slinitctl --system enable explicit > /dev/null 2>&1
out=$(slinitctl --system disable explicit 2>&1)
assert_contains "$out" "not stopping" "disable says an explicit start stays"
assert_eq "$(svc_state explicit)" "STARTED" "explicit start survives disable"
slinitctl --system stop explicit > /dev/null 2>&1

# --- a start of what is already up counts as well ---
slinitctl --system enable explicit > /dev/null 2>&1
out=$(slinitctl --system start explicit 2>&1)
assert_contains "$out" "already started" "start answers as before"
slinitctl --system disable explicit > /dev/null 2>&1
assert_eq "$(svc_state explicit)" "STARTED" "that start survives disable too"
slinitctl --system stop explicit > /dev/null 2>&1

# --- enabling what was stopped on purpose leaves it stopped ---
slinitctl --system enable stoppedsvc > /dev/null 2>&1
slinitctl --system stop stoppedsvc > /dev/null 2>&1
out=$(slinitctl --system enable stoppedsvc 2>&1)
assert_contains "$out" "already enabled" "enable says it was already enabled"
assert_eq "$(svc_state stoppedsvc)" "STOPPED" "and leaves it stopped"

test_summary
