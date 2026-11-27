#!/bin/sh
# Test: a run-as service cannot reach a root-owned pid file through a
# symlink in a directory it owns.
#
# 312 covers a pid file the service wrote itself. Here the file is
# root's own (/run/321v.pid, naming a root process, as /run/auditd.pid
# would), so judging the file's owner alone accepts it. The service's
# user owns /tmp/321a and has put `link -> /run` there; its pid-file is
# /tmp/321a/link/321v.pid. Up to v4.0.3 slinit adopted the root process
# and killed it on stop. Since a91c0d5 the owners of the directories on
# the path count as writers too (systemd's CHASE_SAFE).
#
# Control (21af77b): a root daemon whose pid file sits, with no symlink
# on the path, in a directory owned by its unprivileged user (nginx's
# /run/nginx on Alpine) must still be adopted.

id nobody >/dev/null 2>&1 || skip_case "no user nobody in the image"

setsid sleep 3210 </dev/null >/dev/null 2>&1 &
sleep 0.5
VICTIM=$(pgrep -x -f 'sleep 3210' | head -1)
[ -n "$VICTIM" ] || skip_case "victim did not start"
echo "$VICTIM" > /run/321v.pid       # root's file, in root's directory
rm -rf /tmp/321a; mkdir /tmp/321a; chown nobody /tmp/321a
ln -s /run /tmp/321a/link
chown -h nobody /tmp/321a/link

printf '%s\n' 'type = bgprocess' 'run-as = nobody' 'command = /bin/true' \
    'pid-file = /tmp/321a/link/321v.pid' 'start-timeout = 5' 'restart = no' \
    > /etc/slinit.d/test-321-link

timeout 10 slinitctl --system start test-321-link >/dev/null 2>&1
sleep 1
_st=$(svc_state test-321-link)
_pid=$(timeout 5 slinitctl --system status test-321-link 2>/dev/null | awk '/PID:/ {print $2}')
echo "through the symlink: state $_st pid '$_pid' (victim $VICTIM)"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_pid" != "$VICTIM" ]; then
    echo "OK: the root process behind the symlink was not adopted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: adopted root process $VICTIM through a symlink nobody owns"
fi
timeout 10 slinitctl --system stop test-321-link >/dev/null 2>&1
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$VICTIM" 2>/dev/null; then
    echo "OK: the root process survived the service's stop"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stopping the service killed root process $VICTIM"
fi

# Control: root daemon, pid file in a directory its user owns, no symlink.
rm -rf /run/321ng; mkdir /run/321ng; chown nobody /run/321ng
printf '%s\n' 'type = bgprocess' \
    'command = /bin/sh -c "sleep 3211 & echo $$! > /run/321ng/ng.pid"' \
    'pid-file = /run/321ng/ng.pid' 'start-timeout = 5' 'restart = no' \
    > /etc/slinit.d/test-321-ng
timeout 10 slinitctl --system start test-321-ng >/dev/null 2>&1
wait_for_service test-321-ng STARTED 10
assert_service_state test-321-ng STARTED "a root daemon's pid file in its user's directory is still accepted"
timeout 10 slinitctl --system stop test-321-ng >/dev/null 2>&1

kill "$VICTIM" 2>/dev/null
test_summary
