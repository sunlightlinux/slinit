#!/bin/sh
# 49-process-attrs — omnibus probe for the per-process scalar knobs:
#
#   umask = 027              — verified by stat'ing a file the child
#                              touches with default 0666 (kernel
#                              applies umask → 0640).
#   nice = 5                 — /proc/PID/stat field 19.
#   oom-score-adj = 500      — /proc/PID/oom_score_adj.
#   rlimit-nofile = 1024     — /proc/PID/limits "Max open files".
#   ioprio = best-effort:7   — `ionice -p PID`.
#
# All values are read host-side from /proc to keep the inline command
# simple: slinit's `command =` value is squashed into a single line and
# every printf format string fights the parser's `\` escape rules.

SVC="acceptance-test-attrs"
PROBE="/run/acceptance-test-attr-probe"

cleanup() {
    svc_remove "$SVC"
    rm -f "$PROBE"
}
trap cleanup EXIT INT TERM

rm -f "$PROBE"

# Child just creates the umask-probe file and idles. Everything else
# is observable from /proc/<pid>/.
svc_deploy "$SVC" <<EOF
type = process
umask = 027
nice = 5
oom-score-adj = 500
rlimit-nofile = 1024
ioprio = best-effort:7
command = /bin/sh -c 'touch $PROBE; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $NF; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pid" in
    *[!0-9]*|"")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: couldn't read PID: '$_pid'"
        test_summary
        exit 1
        ;;
    *)
        echo "OK: child PID is $_pid"
        ;;
esac

sleep 1

# umask: 0666 & ~027 = 0640.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$PROBE" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: child never touched $PROBE"
else
    _mode=$(stat -c %a "$PROBE")
    if [ "$_mode" = "640" ]; then
        echo "OK: newly-created file mode = 640 (umask 027 applied)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: file mode = $_mode (expected 640 with umask 027)"
    fi
fi

# nice: /proc/PID/stat field 19. Our child's comm is "sh" — no
# embedded space — so a plain $19 works without juggling the comm
# parens.
_nice=$(awk '{print $19}' /proc/$_pid/stat 2>/dev/null)
assert_eq "$_nice" "5" "/proc/$_pid/stat nice = 5"

# oom_score_adj: direct read.
_oom=$(cat /proc/$_pid/oom_score_adj 2>/dev/null)
assert_eq "$_oom" "500" "/proc/$_pid/oom_score_adj = 500"

# rlimit-nofile: 4th column on the "Max open files" line is the soft
# limit (5th is hard).
_nofile=$(awk '/Max open files/ {print $4}' /proc/$_pid/limits 2>/dev/null)
assert_eq "$_nofile" "1024" "/proc/$_pid/limits Max open files soft = 1024"

# ioprio: best-effort class, prio 7. ionice -p prints e.g.
# "best-effort: prio 7".
if command -v ionice >/dev/null 2>&1; then
    _ioprio=$(ionice -p "$_pid" 2>&1)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$_ioprio" in
        *"best-effort"*7*)
            echo "OK: ioprio = best-effort:7 ($_ioprio)"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: ioprio mismatch: got '$_ioprio'"
            ;;
    esac
else
    echo "SKIP: ionice not on target; ioprio behavioural check omitted"
fi

test_summary
