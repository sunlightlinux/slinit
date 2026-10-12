#!/bin/sh
# 240-skipped-is-visible — a service skipped by an unmet condition-* must
# be distinguishable from one that ran.
#
# It reports State: STARTED, which is systemd's ConditionXxx semantics
# and is deliberate — an unmet condition means "nothing to do", not
# "failed". But until now nothing anywhere said "skipped": not the
# state, not `show`, and not the journal, so the only way to tell a
# skipped service from a trivially successful one was whether its
# command had run.

cat > /etc/slinit.d/test-skip <<'EOF'
type = scripted
command = /bin/sh -c 'echo ran > /run/skip-marker'
condition-path-exists = /definitely-absent-xyz
EOF
slinitctl --system start test-skip > /dev/null 2>&1
sleep 1

_state=$(slinitctl --system status test-skip 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
_ran=$([ -f /run/skip-marker ] && echo yes || echo no)
echo "INFO: state=$_state command-ran=$_ran"

# The premise: it really was skipped, and really does report STARTED.
assert_eq "$_state" "STARTED" "a skipped service still reports STARTED"
assert_eq "$_ran" "no" "the command did not run"

# status must say so.
_skipline=$(slinitctl --system status test-skip 2>/dev/null | grep -c "^ *Skipped: yes")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_skipline" -ge 1 ]; then
    echo "OK: status reports it as skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: status does not mention the skip:"
    slinitctl --system status test-skip 2>&1 | sed 's/^/      /'
fi

# And the reason has to be there, or the operator still has to guess
# which of several conditions it was.
_reason=$(slinitctl --system status test-skip 2>/dev/null | grep "Skipped:")
echo "INFO: $_reason"
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_reason" | grep -q "definitely-absent-xyz"; then
    echo "OK: the reason names the condition that skipped it"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no reason in [$_reason]"
fi

# show carries it too, for scripting.
_shown=$(slinitctl --system show test-skip 2>/dev/null | grep "^StartSkipped=")
assert_eq "$_shown" "StartSkipped=yes" "show renders StartSkipped"

slinitctl --system --ignore-unstarted stop test-skip > /dev/null 2>&1
slinitctl --system unload test-skip > /dev/null 2>&1
rm -f /etc/slinit.d/test-skip /run/skip-marker

# A service that genuinely ran must NOT be reported as skipped — without
# this the check above would pass against a line printed unconditionally.
cat > /etc/slinit.d/test-noskip <<'EOF'
type = scripted
command = /bin/true
EOF
slinitctl --system start test-noskip > /dev/null 2>&1
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system status test-noskip 2>/dev/null | grep -q "Skipped:"; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a service that ran was reported as skipped"
else
    echo "OK: a service that ran is not reported as skipped"
fi
slinitctl --system --ignore-unstarted stop test-noskip > /dev/null 2>&1
slinitctl --system unload test-noskip > /dev/null 2>&1
rm -f /etc/slinit.d/test-noskip

test_summary
