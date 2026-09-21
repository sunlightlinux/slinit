#!/bin/sh
# nosystemd checklist — systemd#6237.
#
# "systemd can't handle the process privilege that belongs to user name
# starts with number, such as 0day": systemd resolved a User= value by
# trying it as a UID first, so an account literally named `0day` was
# read as UID 0 and the service ran as root.
# https://github.com/systemd/systemd/issues/6237
#
# That is a privilege-escalation shape, not a cosmetic parsing bug: any
# site with a numeric-leading account name silently grants it root.
#
# slinit's resolveRunAs tries user.Lookup (by name) first and only falls
# back to LookupId. This test creates exactly the account from the
# upstream report and proves the service runs as that account's real
# UID, not as root.

_uid=4242
_gid=4242

# Create the account from the bug report. Name starts with a digit and
# is not a valid number, so a UID-first resolver would either fail or —
# worse, as in systemd — land on 0.
grep -q '^0day:' /etc/passwd 2>/dev/null || \
    echo "0day:x:${_uid}:${_gid}:numeric-leading test account:/tmp:/bin/sh" >> /etc/passwd
grep -q '^0day:' /etc/group 2>/dev/null || \
    echo "0day:x:${_gid}:" >> /etc/group

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q '^0day:' /etc/passwd; then
    echo "OK: account '0day' exists with uid $_uid"
else
    echo "FAIL: could not create the test account"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    test_summary
    return 0 2>/dev/null || exit 0
fi

cat > /etc/slinit.d/numeric-user-svc <<'SVC'
type = process
run-as = 0day
command = /bin/sh -c "while true; do sleep 1; done"
SVC

slinitctl start numeric-user-svc >/dev/null 2>&1
wait_for_service "numeric-user-svc" "STARTED" 10
assert_service_state "numeric-user-svc" "STARTED" "service with a numeric-leading run-as started"

_pid=$(slinitctl status numeric-user-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ]; then
    echo "FAIL: no PID for the service"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    test_summary
    return 0 2>/dev/null || exit 0
fi
echo "OK: service running as pid $_pid"

# /proc/PID/status uses tabs, so normalise before reading the field.
_actual=$(awk '/^Uid:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_actual" = "0" ]; then
    echo "FAIL: '0day' resolved to UID 0 — the service is running as root (systemd#6237)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
elif [ "$_actual" = "$_uid" ]; then
    echo "OK: '0day' resolved by name to UID $_uid, not as a number"
else
    echo "FAIL: '0day' resolved to UID $_actual, expected $_uid"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# The companion property: a genuinely numeric run-as must still work as
# a UID, or the name-first rule would have broken the normal case.
cat > /etc/slinit.d/numeric-uid-svc <<'SVC'
type = process
run-as = 4242
command = /bin/sh -c "while true; do sleep 1; done"
SVC
slinitctl start numeric-uid-svc >/dev/null 2>&1
wait_for_service "numeric-uid-svc" "STARTED" 10
_pid2=$(slinitctl status numeric-uid-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
_actual2=$(awk '/^Uid:/ { print $2; exit }' "/proc/$_pid2/status" 2>/dev/null)
assert_eq "$_actual2" "$_uid" "a numeric run-as still resolves as a UID"

slinitctl stop numeric-user-svc >/dev/null 2>&1
slinitctl stop numeric-uid-svc  >/dev/null 2>&1

test_summary
