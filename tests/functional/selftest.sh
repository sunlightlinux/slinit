#!/bin/bash
# selftest.sh - Check that the functional harness can still tell a passing
# case from a failing one.
#
# Why this exists. Every case under cases/ is written to pass, so the suite
# has no way to notice when the harness stops reporting failures — it just
# gets greener. That has now happened twice:
#
#   * e1673d4 (April) — `_output=$(...) || true` made $? always 0;
#     "causing all functional tests to report PASS even when assertions
#     failed".
#   * the console-streaming change — `(. "$TEST_SCRIPT") | tee /dev/console`
#     followed by `_rc=$?`, which reads tee's status, not the script's.
#     Same outcome, different mechanism, five months later.
#
# Both were found by accident. This finds them on purpose: it boots three
# cases whose verdicts are known in advance — one that must pass, one that
# must fail, one that must skip — and checks what the harness said about
# each. A harness that cannot fail a case fails here instead.
#
# Usage: ./tests/functional/selftest.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROBES="${SCRIPT_DIR}/selftest"

out=$(KEEP_BUILD="${KEEP_BUILD:-1}" "${SCRIPT_DIR}/run-tests.sh" \
    "${PROBES}/zz-selftest-pass.sh" \
    "${PROBES}/zz-selftest-fail.sh" \
    "${PROBES}/zz-selftest-skip.sh" 2>&1)
rc=$?

echo "${out}"
echo
echo "--- harness selftest ---"

bad=0
check() {
    # $1 = the line that must appear, $2 = what it proves
    if grep -qE "$1" <<<"${out}"; then
        echo "  ok   $2"
    else
        echo "  BAD  $2 — expected a line matching /$1/"
        bad=1
    fi
}

check '(PASS|^| +)[^ ]*zz-selftest-pass' "a passing case is reported"
check 'FAIL.*zz-selftest-fail'           "a FAILING case is reported as FAIL"
check 'SKIP.*zz-selftest-skip'           "a skipped case is reported as SKIP"
check 'Results: 1 passed, 1 failed, 1 skipped \(3 total\)' \
      "the tallies are one of each"

# The deliberate failure must also reach the exit status, since that is
# what CI reads. A harness that prints FAIL and exits 0 is still broken.
if [ "${rc}" -eq 0 ]; then
    echo "  BAD  run-tests.sh exited 0 despite a failing case"
    bad=1
else
    echo "  ok   the failing case makes run-tests.sh exit non-zero"
fi

echo
if [ "${bad}" -ne 0 ]; then
    echo "HARNESS SELFTEST FAILED — the suite's verdicts cannot be trusted"
    echo "until this passes, whatever cases/ reports."
    exit 1
fi
echo "harness selftest passed"
