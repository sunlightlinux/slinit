#!/bin/sh
# 163-condition-security-measured-os — `condition-security = measured-os`
# against the target's real TPM and boot path.
#
# Since 4.1 measured-os is systemd's efi_measured_os: the
# systemd.tpm2_measured_os= kernel argument when given, otherwise
# measured-uki (sd-stub's StubPcrKernelImage EFI variable and a
# non-empty TPM log). Before, any non-empty TPM log satisfied it, so on
# ceres (a real TPM, booted without a UKI) this case expected true and
# now expects false. The case works out the expected answer from the
# same inputs and checks both forms against it, so it holds on a host
# of either kind. ($SYSTEMD_FORCE_MEASURE, which comes first, is not
# looked at: slinit's environment does not carry it on these targets.)

SVC_YES="acceptance-test-cond-tpm-yes"
SVC_NEG="acceptance-test-cond-tpm-neg"
MARK_YES="/run/acceptance-cond-tpm-yes.mark"
MARK_NEG="/run/acceptance-cond-tpm-neg.mark"
TPM_LOG="/sys/kernel/security/tpm0/binary_bios_measurements"
STUB_VAR="/sys/firmware/efi/efivars/StubPcrKernelImage-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

cleanup() {
    svc_remove "$SVC_YES" "$SVC_NEG"
    rm -f "$MARK_YES" "$MARK_NEG"
}
trap cleanup EXIT INT TERM

# The last systemd.tpm2_measured_os= word decides; a bare key is true.
EXPECT=""
for w in $(cat /proc/cmdline); do
    case "$w" in
        systemd.tpm2_measured_os|systemd.tpm2-measured-os) EXPECT=yes ;;
        systemd.tpm2_measured_os=*|systemd.tpm2-measured-os=*)
            case "${w#*=}" in
                1|yes|y|true|t|on) EXPECT=yes ;;
                0|no|n|false|f|off) EXPECT=no ;;
            esac ;;
    esac
done
if [ -z "$EXPECT" ]; then
    if [ -e "$STUB_VAR" ] && [ -s "$TPM_LOG" ]; then
        EXPECT=yes
    else
        EXPECT=no
    fi
fi
echo "INFO: measured-os expected $EXPECT (TPM log $( [ -s "$TPM_LOG" ] && echo present || echo absent ), stub variable $( [ -e "$STUB_VAR" ] && echo present || echo absent ))"

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
assert_service_state "$SVC_NEG" "STARTED" "negated measured-os still reaches STARTED"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$EXPECT" = yes ] && [ -e "$MARK_YES" ] && [ ! -e "$MARK_NEG" ]; then
    echo "OK: measured-os held: the plain form ran, the negated one was skipped"
elif [ "$EXPECT" = no ] && [ ! -e "$MARK_YES" ] && [ -e "$MARK_NEG" ]; then
    echo "OK: measured-os did not hold: the plain form was skipped, the negated one ran"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected measured-os=$EXPECT; ran: plain=$( [ -e "$MARK_YES" ] && echo yes || echo no ) negated=$( [ -e "$MARK_NEG" ] && echo yes || echo no )"
fi

test_summary
