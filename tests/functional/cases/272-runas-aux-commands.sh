#!/bin/sh
# Test: the auxiliary commands of a service run with its run-as.
#
# cron-command, healthcheck-command and exec-condition ran as slinit
# itself, root under PID 1, whatever run-as said, while the main process
# ran as the user. A health check or cron job written for an
# unprivileged daemon then ran with full root.
#
# Each command records `id -u`; nobody is uid 65534 on Alpine. A command
# that never ran leaves no file, which fails too: "not root" alone would
# pass for a check that did not run at all.

D=/tmp/272
rm -rf $D
mkdir -p $D
chmod 1777 $D

_nobody=$(awk -F: '$1 == "nobody" { print $3 }' /etc/passwd)
[ -n "$_nobody" ] || skip_case "no nobody user in the image"

printf '%s\n' 'type = process' 'run-as = nobody' \
    'command = /bin/sh -c "id -u > /tmp/272/main; exec sleep 120"' \
    'exec-condition = id -u > /tmp/272/ec' \
    'cron-command = /bin/sh -c "id -u > /tmp/272/cron"' \
    'cron-interval = 1s' 'cron-delay = 1s' 'cron-on-error = continue' \
    'healthcheck-command = /bin/sh -c "id -u > /tmp/272/health"' \
    'healthcheck-interval = 1' 'healthcheck-delay = 1' \
    'restart = false' > /etc/slinit.d/test-runas-aux

slinitctl --system start test-runas-aux >/dev/null 2>&1
wait_for_service test-runas-aux STARTED 10 || true
assert_service_state test-runas-aux STARTED "service with run-as and aux commands starts"

# cron and health check fire after a 1s delay; give them a few rounds.
_i=0
while [ "$_i" -lt 10 ]; do
    [ -s $D/cron ] && [ -s $D/health ] && break
    sleep 1
    _i=$((_i + 1))
done

assert_eq "$(cat $D/main 2>/dev/null)" "$_nobody" "main process runs as nobody (control)"
assert_eq "$(cat $D/ec 2>/dev/null)" "$_nobody" "exec-condition runs as the run-as user"
assert_eq "$(cat $D/cron 2>/dev/null)" "$_nobody" "cron-command runs as the run-as user"
assert_eq "$(cat $D/health 2>/dev/null)" "$_nobody" "healthcheck-command runs as the run-as user"

slinitctl --system stop test-runas-aux >/dev/null 2>&1
test_summary
