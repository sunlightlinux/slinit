#!/bin/sh
# Test: `debug = yes` raises SIGSTOP in slinit-runner before exec so a
# debugger can attach; the operator resumes with SIGCONT. We assert the
# service body has NOT run while stopped (marker absent), then send
# SIGCONT and confirm it exec's and runs.

sleep 2

# Pre-exec: slinit-runner is SIGSTOP'd, the real command never ran.
assert_eq "$(cat /tmp/debug-marker 2>/dev/null)" "" "service body suppressed while debug-stopped"

pid=$(pgrep -f 'slinit-runner.*--debug' | head -1)
assert_not_contains "${pid:-EMPTY}" "EMPTY" "found the stopped slinit-runner pid"

# Confirm the process really is in the stopped (T) state.
st=$(awk '{print $3}' /proc/$pid/stat 2>/dev/null)
assert_eq "$st" "T" "slinit-runner is in stopped state"

# Resume it: the runner now exec's into the real service command.
kill -CONT "$pid"

wait_for_file() {
	i=0
	while [ $i -lt 20 ]; do
		[ "$(cat /tmp/debug-marker 2>/dev/null)" = "running" ] && return 0
		i=$((i + 1))
		sleep 0.5
	done
	return 1
}
if wait_for_file; then
	assert_eq "$(cat /tmp/debug-marker 2>/dev/null)" "running" "service ran after SIGCONT"
else
	assert_eq "timeout" "running" "service ran after SIGCONT"
fi

test_summary
