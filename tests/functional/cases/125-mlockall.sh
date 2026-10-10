#!/bin/sh
# Test: mlockall = current+future locks the service's memory (VmLck > 0,
# via the libslinit-mlock.so preload) and raises RLIMIT_MEMLOCK to
# unlimited so the service may lock more itself.

SVC="test-mlk"

# The preload library has to be built for the rootfs's libc (musl here),
# so build-vm.sh needs a musl compiler and installs nothing when it has
# none. Without the library the runner refuses to start the service — it
# will not pretend to honour mlockall — so there is nothing to measure.
if [ ! -f /lib/slinit/libslinit-mlock.so ]; then
    skip_case "libslinit-mlock.so not in the image (no musl compiler on the build host)"
fi

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
mlockall = current+future
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10

# Diagnose before asserting. When the preload library loads and its
# mlockall(2) fails, it writes "slinit-mlock: mlockall: <errno>" to the
# service's stderr and _exit(127)s — by design, since a service that
# asked for locked memory and did not get it is the failure the setting
# exists to prevent. That message goes to the service's log, not the
# console, so the case used to report only "got 'STOPPED'" and left the
# reason in the VM. Everything needed to tell the three candidate causes
# apart — library absent, library present but unloadable, library loaded
# and the syscall refused — is printed here.
if [ "$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^ *State:/ { print $2; exit }')" != "STARTED" ]; then
    echo "INFO: $SVC is not STARTED — gathering why"
    echo "INFO:   runner:  $(command -v slinit-runner || echo ABSENT)"
    if [ -f /lib/slinit/libslinit-mlock.so ]; then
        echo "INFO:   library: present ($(wc -c < /lib/slinit/libslinit-mlock.so) bytes)"
        echo "INFO:   NEEDED:  $(strings /lib/slinit/libslinit-mlock.so 2>/dev/null | grep -m3 '^libc' | tr '\n' ' ')"
    else
        echo "INFO:   library: ABSENT from /lib/slinit"
    fi
    echo "INFO:   service log:"
    slinitctl --system catlog "$SVC" 2>/dev/null | tail -5 | sed 's/^/INFO:     /'
    slinit-journalctl -u "$SVC" -n 5 --no-pager 2>/dev/null | sed 's/^/INFO:     /'
fi

assert_service_state "$SVC" "STARTED" "service reached STARTED"

# Wait off the clock, not off an iteration count. This loop used to try
# five times at 0.2s — a one-second budget — which is enough with KVM and
# not with the TCG emulation CI runs under, where locking a service's
# memory is slow. The case then read `/proc//limits`, found nothing, and
# reported "RLIMIT_MEMLOCK not raised — line=''": a timing failure
# wearing the words of a broken feature. An empty value and a zero mean
# different things, and the message now cannot confuse them.
_pid=""
_deadline=$(( $(date +%s) + 15 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.5
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no PID for $SVC within 15s — the assertions below would read" \
         "/proc//… and report the feature broken when the service simply had" \
         "not been seen running yet"
    test_summary
    exit 1
fi
echo "OK: $SVC has PID $_pid"

_line=$(awk '/^Max locked memory/' "/proc/$_pid/limits" 2>/dev/null)
_soft=$(printf '%s' "$_line" | awk '{ print $(NF-2) }')
_hard=$(printf '%s' "$_line" | awk '{ print $(NF-1) }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_soft" = "unlimited" ] && [ "$_hard" = "unlimited" ]; then
    echo "OK: RLIMIT_MEMLOCK soft=unlimited hard=unlimited"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: RLIMIT_MEMLOCK not raised — line='$_line' (soft='$_soft' hard='$_hard')"
fi

# The lock itself: slinit-runner preloads libslinit-mlock.so, which calls
# mlockall(2) inside the service's main process, so VmLck is non-zero.
_lck=$(awk '/^VmLck:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_lck" ] && [ "$_lck" -gt 0 ]; then
    echo "OK: service memory locked (VmLck=${_lck} kB)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: service memory not locked (VmLck='${_lck}')"
fi

test_summary
