#!/bin/sh
# Test: four more paths where a FIFO used to freeze PID 1.
#
# Up to v4.0.3 slinit opened these with a plain blocking open while
# holding its service lock (or its loader's lock), so a FIFO with nobody
# on the other end stopped every control request for good. 309 covers
# env-file and log-file; this covers:
#  - lock-file at a FIFO (df16816);
#  - condition-file-value reading a FIFO (c7dbdb2);
#  - load-credential from a FIFO (f7d394b);
#  - the service description file itself a FIFO (347bd81).
# Each is tried in turn, and PID 1 must still answer after it.

D=/run/329
rm -rf "$D"; mkdir -p "$D"
alive() { [ "$(svc_state boot)" = STARTED ]; }
alive || skip_case "PID 1 not answering before the test"

unfreeze() { # let a frozen PID 1 go, so the rest of the case can report
    for f in "$D"/*.fifo /etc/slinit.d/test-329-desc; do
        [ -p "$f" ] || continue
        timeout 1 sh -c ": > $f" 2>/dev/null
        timeout 1 cat "$f" >/dev/null 2>&1 &
    done
    sleep 1
}

try() { # name what
    timeout 5 slinitctl --system --no-wait start "$1" >/dev/null 2>&1
    sleep 2
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if alive; then
        echo "OK: PID 1 still answers after $2"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: PID 1 stopped answering after $2"
        unfreeze
    fi
}

mkfifo "$D/lock.fifo" "$D/value.fifo" "$D/cred.fifo"
printf '%s\n' 'type = process' 'command = /bin/sleep 3291' 'restart = false' \
    "lock-file = $D/lock.fifo" > /etc/slinit.d/test-329-lock
printf '%s\n' 'type = process' 'command = /bin/sleep 3292' 'restart = false' \
    "condition-file-value = $D/value.fifo:1" > /etc/slinit.d/test-329-cond
printf '%s\n' 'type = process' 'command = /bin/sleep 3293' 'restart = false' \
    "load-credential = c:$D/cred.fifo" > /etc/slinit.d/test-329-cred
mkfifo /etc/slinit.d/test-329-desc

try test-329-lock "starting a service whose lock-file is a FIFO"
try test-329-cond "starting a service whose condition-file-value reads a FIFO"
try test-329-cred "starting a service with a load-credential FIFO"
try test-329-desc "loading a service description that is a FIFO"

# None of them may have started on the FIFO.
for s in lock cred desc; do
    _st=$(svc_state "test-329-$s")
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$_st" != STARTED ]; then
        echo "OK: test-329-$s did not start on its FIFO (state '${_st:-not loaded}')"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: test-329-$s started on its FIFO"
    fi
done

rm -f /etc/slinit.d/test-329-desc
test_summary
