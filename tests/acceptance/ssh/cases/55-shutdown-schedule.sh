#!/bin/sh
# 55-shutdown-schedule — exercise the scheduled-shutdown control path
# WITHOUT actually shutting the box down.
#
# slinitctl shutdown takes [type] [time]:
#   type ∈ {halt, poweroff, reboot, kexec, softreboot}
#   time ∈ {now, +N minutes, HH:MM}
#
# With a non-immediate `time`, slinitctl sends CmdScheduleShutdown
# (protocol.go:85) and the daemon queues but does NOT execute. `-c`
# cancels, `--status` queries. We schedule far enough in the future
# that any test-runner stall can't accidentally fire it, then cancel.

# --- Probe 1: schedule reboot +60min, expect ACK + status report -----
_out=$(slinitctl --system shutdown reboot +60 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"scheduled in"*)
        echo "OK: shutdown reboot +60 scheduled (got: $_out)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: scheduling reported error: $_out"
        # try a cancel just in case to not poison the suite
        slinitctl --system shutdown -c >/dev/null 2>&1 || true
        test_summary
        exit 1
        ;;
esac

# --- Probe 2: --status reports the pending shutdown -----------------
_status=$(slinitctl --system shutdown --status 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_status" in
    *"Shutdown"*"reboot"*"scheduled in"*)
        echo "OK: --status echoes 'Shutdown (reboot) scheduled in ...' ($_status)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --status didn't echo a pending reboot: $_status"
        ;;
esac

# --- Probe 3: -c cancels the pending shutdown ----------------------
_cancel=$(slinitctl --system shutdown -c 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_cancel" in
    *"cancelled"*|*"canceled"*)
        echo "OK: cancel reported ($_cancel)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: cancel didn't report success: $_cancel"
        ;;
esac

# --- Probe 4: --status now says nothing pending --------------------
_status2=$(slinitctl --system shutdown --status 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_status2" in
    *"No shutdown is scheduled"*)
        echo "OK: --status confirms nothing pending after cancel"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --status didn't confirm cancel: $_status2"
        ;;
esac

# --- Probe 5: schedule each non-default type, cancel each ----------
# Round-trip every shutdown type the parser knows so a future regression
# (e.g. one type silently dropped) is caught.
for t in halt poweroff reboot kexec softreboot; do
    _o=$(slinitctl --system shutdown "$t" +120 2>&1)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$_o" in
        *"scheduled in"*)
            echo "OK: shutdown type '$t' accepted ($_o)"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: shutdown type '$t' rejected: $_o"
            ;;
    esac
    slinitctl --system shutdown -c >/dev/null 2>&1 || true
done

# --- Probe 6: unknown type rejected --------------------------------
# Trying to set a bogus type must error out (and not partially queue
# anything). Then verify --status still clean.
_o=$(slinitctl --system shutdown badtype +5 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ] && echo "$_o" | grep -qi "unknown shutdown type"; then
    echo "OK: unknown shutdown type rejected (exit=$_rc, output: $_o)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 'badtype' was not rejected: exit=$_rc out='$_o'"
    # be paranoid: cancel anything that may have leaked through
    slinitctl --system shutdown -c >/dev/null 2>&1 || true
fi

# Final paranoia: ensure nothing is queued.
_final=$(slinitctl --system shutdown --status 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_final" in
    *"No shutdown is scheduled"*)
        echo "OK: final state — no shutdown queued"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: shutdown is queued at end-of-test: $_final"
        slinitctl --system shutdown -c >/dev/null 2>&1 || true
        ;;
esac

test_summary
