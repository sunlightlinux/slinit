#!/bin/sh
# Test: chroot combined with a runner feature (here no-new-privs).
#
# The runner wraps the command, and chroot is applied by the kernel at
# fork, before the runner is exec'd. The runner path was then looked up
# INSIDE the jail: the service failed to start, or ran whatever the jail
# had at that path, as root, before credentials were dropped.

J=/srv/269-jail
mkdir -p $J/bin $J/lib $J/sbin $J/tmp $J/work
cp /bin/busybox $J/bin/busybox
for l in /lib/ld-musl-*.so.1; do cp "$l" $J/lib/; done
for a in sh id touch grep sleep pwd; do ln -sf busybox $J/bin/$a; done

# A planted "runner" inside the jail. It must never run.
printf '%s\n' '#!/bin/sh' 'touch /tmp/planted-runner-ran' \
    'while [ "${1#--}" != "$1" ]; do shift; done' 'exec "$@"' \
    > $J/sbin/slinit-runner
chmod 755 $J/sbin/slinit-runner

printf '%s\n' 'type = process' "chroot = $J" 'options = no-new-privs' \
    'working-dir = /work' \
    'command = /bin/sh -c "id -u > /tmp/ran; pwd > /tmp/cwd; grep NoNewPrivs /proc/self/status > /tmp/nnp 2>&1; exec sleep 60"' \
    'restart = false' > /etc/slinit.d/test-chroot-runner

slinitctl --system start test-chroot-runner >/dev/null 2>&1
wait_for_service test-chroot-runner STARTED 10 || true
sleep 1
assert_service_state test-chroot-runner STARTED "service with chroot + no-new-privs starts"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e $J/tmp/planted-runner-ran ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the runner planted inside the chroot was executed"
else
    echo "OK: the planted runner did not run"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -s $J/tmp/ran ]; then
    echo "OK: the command ran inside the jail (uid $(cat $J/tmp/ran))"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the command never ran inside the jail"
fi

# The jail has no /proc, so NoNewPrivs is read from outside.
_pid=$(slinitctl --system status test-chroot-runner 2>/dev/null | awk '/PID:/ { print $2; exit }')
assert_eq "$(awk '/^NoNewPrivs/ { print $2 }' /proc/$_pid/status 2>/dev/null)" "1" \
    "no-new-privs applied to the jailed service"
assert_eq "$(cat $J/tmp/cwd 2>/dev/null)" "/work" "working-dir is resolved inside the jail"

slinitctl --system stop test-chroot-runner >/dev/null 2>&1
test_summary
