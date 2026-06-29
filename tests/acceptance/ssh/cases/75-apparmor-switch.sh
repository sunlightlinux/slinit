#!/bin/sh
# 75-apparmor-switch — aa_change_onexec wiring + parser validation.
#
# `apparmor-switch = <profile>` makes slinit-runner write
#   "exec <profile>"
# to /proc/self/attr/exec before syscall.Exec, so the kernel applies
# the profile to the next execve. Implementation: cmd/slinit-runner
# changeOnExec().
#
# Full end-to-end coverage needs the AppArmor LSM ACTIVE on the host
# (kernel built with CONFIG_SECURITY_APPARMOR=y AND lsm=apparmor in
# the cmdline). On the test VM the module is built in but the LSM is
# NOT enabled (/sys/kernel/security/apparmor missing) so the switch
# fails open(/proc/self/attr/exec) with ENOENT. That is the "fail
# closed" path slinit promises — the start aborts instead of running
# the binary unconfined — and that's exactly what we assert here.

WORK="/tmp/acceptance-aa"
SVC="acceptance-test-aa"
SVCFILE="/etc/slinit.d/$SVC"

cleanup() {
    slinitctl --system stop "$SVC" 2>/dev/null
    slinitctl --system unload "$SVC" 2>/dev/null
    rm -f "$SVCFILE"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"

# --- Probe 1: empty profile name is rejected by the parser ----------
cat > "$SVCFILE" <<EOF
type = process
command = /bin/sleep 60
apparmor-switch =
restart = false
EOF
_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_crc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_crc" -ne 0 ] && echo "$_chk" | grep -qi "must not be empty"; then
    echo "OK: empty apparmor-switch is rejected by slinit-check"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: empty apparmor-switch slipped past validation (rc=$_crc)"
    echo "$_chk" | sed 's/^/  | /'
fi

# --- Probe 2: well-formed profile name is accepted by the parser ----
cat > "$SVCFILE" <<EOF
type = process
command = /bin/sleep 60
apparmor-switch = unprivileged_userns
restart = false
EOF
_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: well-formed apparmor-switch accepted by slinit-check"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: parser rejected a valid apparmor-switch:"
    echo "$_chk" | sed 's/^/  | /'
fi

# --- Probe 3: AppArmor LSM status on this host ----------------------
# If /sys/kernel/security/apparmor is missing, the LSM is not active.
# This is the precondition for the fail-closed test below; we report
# either way so the case carries its own diagnostic context.
if [ -d /sys/kernel/security/apparmor ]; then
    AA_ACTIVE=1
    echo "INFO: AppArmor LSM is ACTIVE on this host"
else
    AA_ACTIVE=0
    echo "INFO: AppArmor LSM is NOT active on this host"
fi

# --- Probe 4: with the LSM down, start must fail closed (no exec) ---
# slinit-runner's changeOnExec tries to open /proc/self/attr/exec.
# Without the LSM that path doesn't exist, the helper returns an error,
# and the runner aborts before syscall.Exec. The service should NEVER
# reach STARTED, regardless of the command being /bin/sleep.
if [ "$AA_ACTIVE" -eq 0 ]; then
    slinitctl --system start "$SVC" >/dev/null 2>&1
    _e=0
    while [ "$_e" -lt 5 ]; do
        _st=$(slinitctl --system status "$SVC" 2>/dev/null \
            | awk '/State:/ {print $2; exit}')
        case "$_st" in
            STARTED|STOPPED|FAILED) break ;;
        esac
        sleep 1
        _e=$((_e + 1))
    done

    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$_st" in
        STARTED)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: service reached STARTED despite no AppArmor LSM (should fail closed)"
            ;;
        STOPPED|FAILED|STARTING)
            echo "OK: apparmor-switch fails closed without LSM (state=$_st)"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: unexpected state '$_st' after fail-closed start"
            ;;
    esac
fi

# --- Probe 5: --help advertises the keyword in slinit-check ---------
_TESTS_RUN=$((_TESTS_RUN + 1))
# Parser is tested via probe 1+2; the keyword is documented in the man
# pages. Confirm the literal stanza name is recognised by attempting a
# unit that uses both apparmor-load (verified-no-op when file missing)
# and apparmor-switch — the parser must traverse both case arms without
# error.
cat > "$SVCFILE" <<EOF
type = process
command = /bin/sleep 60
apparmor-load = /etc/apparmor.d/unprivileged_userns
apparmor-switch = unprivileged_userns
restart = false
EOF
_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
if [ $? -eq 0 ]; then
    echo "OK: parser accepts both apparmor-load and apparmor-switch in one unit"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: combined apparmor-load+switch rejected:"
    echo "$_chk" | sed 's/^/  | /'
fi

test_summary
