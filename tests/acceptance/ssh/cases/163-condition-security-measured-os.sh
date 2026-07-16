#!/bin/sh
# 163-condition-security-measured-os — v261 `condition-security = measured-os`
# succeeds when firmware/kernel populated the TPM binary_bios_measurements
# log. This exercises the positive path — impossible on stock QEMU (no
# virtual TPM in the functional harness), so functional test 144 covers
# only the negative case. Ceres has a real TPM (/dev/tpm0 + /dev/tpmrm0),
# so the measurements log is populated and `measured-os` returns true.
#
# Probes: (a) positive marker via matched condition, (b) skipped marker
# via `!measured-os`. If the log is empty/absent on this target (e.g.
# a live-boot without TPM), the case skips gracefully with a note — this
# is a hardware-conditional test.

SVC_YES="acceptance-test-cond-tpm-yes"
SVC_NEG="acceptance-test-cond-tpm-neg"
MARK_YES="/run/acceptance-cond-tpm-yes.mark"
MARK_NEG="/run/acceptance-cond-tpm-neg.mark"
TPM_LOG="/sys/kernel/security/tpm0/binary_bios_measurements"

cleanup() {
    svc_remove "$SVC_YES" "$SVC_NEG"
    rm -f "$MARK_YES" "$MARK_NEG"
}
trap cleanup EXIT INT TERM

# Gate the whole case on TPM log presence. Report explicitly so a
# regression later (log disappears on a reboot) is visible rather
# than silently passing.
if [ ! -s "$TPM_LOG" ]; then
    _TESTS_RUN=$((_TESTS_RUN + 1))
    echo "SKIP: $TPM_LOG missing or empty; measured-os positive path not testable here"
    test_summary
    exit 0
fi

rm -f "$MARK_YES" "$MARK_NEG"

svc_deploy "$SVC_YES" <<EOF
type = scripted
condition-security = measured-os
command = /bin/sh -c 'touch $MARK_YES; exit 0'
restart = false
EOF

svc_deploy "$SVC_NEG" <<EOF
type = scripted
condition-security = !measured-os
command = /bin/sh -c 'touch $MARK_NEG; exit 0'
restart = false
EOF

slinitctl --system start "$SVC_YES" >/dev/null 2>&1
slinitctl --system start "$SVC_NEG" >/dev/null 2>&1
wait_for_service "$SVC_YES" "STARTED" 10 || true
wait_for_service "$SVC_NEG" "STARTED" 10 || true

assert_service_state "$SVC_YES" "STARTED" "measured-os condition reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_YES" ]; then
    echo "OK: command ran under condition-security=measured-os (TPM log present)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: measured-os condition true but command did not run"
fi

# Negated form: with the log populated, `!measured-os` is false → command
# must be skipped but the service still reaches STARTED.
assert_service_state "$SVC_NEG" "STARTED" "negated measured-os still reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_NEG" ]; then
    echo "OK: command skipped under condition-security=!measured-os"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: negated measured-os ran the command anyway"
fi

test_summary
