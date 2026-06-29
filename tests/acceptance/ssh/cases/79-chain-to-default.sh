#!/bin/sh
# 79-chain-to-default — default chain semantics: fire on clean exit only.
#
# `chain-to = <svc>` registers a downstream activation that fires when
# the producer exits. By default the chain fires ONLY when the exit is
# clean (rc=0 OR the exit code is listed under `normal-exit`).
# `options = always-chain` reverses that — see case 61.
#
# This case is the missing default-semantics half of 61:
#   * clean exit  → chain fires, downstream marker present
#   * non-clean   → chain skipped, downstream marker absent
#
# Two A/B service pairs run back-to-back so the test stays
# deterministic and self-cleaning.

CLEAN_A="acceptance-test-chain-clean-a"
CLEAN_B="acceptance-test-chain-clean-b"
FAIL_A="acceptance-test-chain-fail-a"
FAIL_B="acceptance-test-chain-fail-b"
CLEAN_MARK="/tmp/acceptance-chain-clean.mark"
FAIL_MARK="/tmp/acceptance-chain-fail.mark"

cleanup() {
    for s in "$CLEAN_A" "$CLEAN_B" "$FAIL_A" "$FAIL_B"; do
        slinitctl --system stop "$s" 2>/dev/null
        slinitctl --system unload "$s" 2>/dev/null
        rm -f "/etc/slinit.d/$s"
    done
    rm -f "$CLEAN_MARK" "$FAIL_MARK"
}
trap cleanup EXIT INT TERM
cleanup

# --- Clean pair: producer exits 0; default chain → B fires ----------
# Producer is `type=process` (not scripted) so the natural process exit
# routes through stopReason.DidFinish() — the exact branch record.go:2045
# uses to decide whether chain-to fires. A scripted `command` whose
# script returns 0 keeps the service in STARTED forever and never
# triggers the chain.
cat > "/etc/slinit.d/$CLEAN_A" <<EOF
type = process
command = /bin/sh -c 'exit 0'
chain-to = $CLEAN_B
restart = false
EOF

cat > "/etc/slinit.d/$CLEAN_B" <<EOF
type = process
command = /bin/sh -c 'touch $CLEAN_MARK; exit 0'
restart = false
EOF

slinitctl --system --no-wait start "$CLEAN_A" >/dev/null 2>&1
# Wait up to 5s for the chain trigger.
_e=0
while [ "$_e" -lt 5 ]; do
    [ -e "$CLEAN_MARK" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$CLEAN_MARK" ]; then
    echo "OK: default chain-to fired after clean exit (marker present)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: default chain didn't fire after rc=0:"
    slinitctl --system status "$CLEAN_B" 2>/dev/null | sed 's/^/  | /'
fi

# B exits 0 right after touching the marker, so by the time we look
# it has already returned to STOPPED. The marker is the durable
# signal; checking state would race the test runner.

# --- Non-clean pair: producer exits 1; default chain must SKIP ------
cat > "/etc/slinit.d/$FAIL_A" <<EOF
type = process
command = /bin/sh -c 'exit 1'
chain-to = $FAIL_B
restart = false
EOF

cat > "/etc/slinit.d/$FAIL_B" <<EOF
type = process
command = /bin/sh -c 'touch $FAIL_MARK; exit 0'
restart = false
EOF

slinitctl --system --no-wait start "$FAIL_A" >/dev/null 2>&1
# Give the (skipped) chain a window equal to the clean test's, then
# assert the marker is absent.
sleep 3

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$FAIL_MARK" ]; then
    echo "OK: default chain-to skipped after non-clean exit"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $FAIL_B marker appeared after rc=1 (always-chain semantics?)"
fi

test_summary
