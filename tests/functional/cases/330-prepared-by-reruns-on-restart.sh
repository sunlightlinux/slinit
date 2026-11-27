#!/bin/sh
# Test: prepared-by runs its preparation again before every automatic
# restart, and only then the service.
#
# slinit-service(5): "each time this service restarts, *service* is
# restarted first". No case had used prepared-by. app crashes once and
# comes back with restart = yes; prep (scripted) must run before each of
# app's two runs, in that order, and app must stay up after the second;
# and again before the run a `slinitctl restart` starts.
# A plain depends-on would run prep once: the log would read prep, app,
# app.

LOG=/run/330.log
rm -f "$LOG" /run/330.crashed

printf '%s\n' 'type = scripted' \
    "command = /bin/sh -c \"echo prep >> $LOG\"" > /etc/slinit.d/test-330-prep
printf '%s\n' 'type = process' \
    "command = /bin/sh -c \"echo app >> $LOG; [ -e /run/330.crashed ] && exec sleep 1000; touch /run/330.crashed; sleep 0.5; exit 1\"" \
    'restart = yes' 'restart-delay = 0.5' \
    'prepared-by: test-330-prep' > /etc/slinit.d/test-330-app

slinitctl --system start test-330-app >/dev/null 2>&1

_deadline=$(( $(date +%s) + 15 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    [ "$(grep -c app "$LOG" 2>/dev/null)" -ge 2 ] && break
    sleep 0.2
done
sleep 1

_seq=$(tr '\n' ' ' < "$LOG" 2>/dev/null)
echo "log: $_seq"
assert_eq "$_seq" "prep app prep app " "prep ran before each of app's runs, the restart included"
assert_service_state test-330-app "STARTED" "app is up after its restart"
assert_service_state test-330-prep "STARTED" "prep is STARTED again"

# An operator's restart is a restart too.
slinitctl --system restart test-330-app >/dev/null 2>&1
sleep 1
_seq=$(tr '\n' ' ' < "$LOG" 2>/dev/null)
assert_eq "$_seq" "prep app prep app prep app " "slinitctl restart runs prep again first as well"
assert_service_state test-330-app "STARTED" "app is up after slinitctl restart"
slinitctl --system stop test-330-app >/dev/null 2>&1
test_summary
