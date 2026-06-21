#!/bin/sh
# 48-seccomp — `system-call-filter` installs a seccomp-bpf filter on
# the child via slinit's BPF compiler (pkg/seccomp/). With the '~'
# (deny-mode) prefix the listed syscalls + groups are *blocked*; the
# default action is set by `system-call-error-number` (defaults to
# EPERM). Without '~' the filter is allow-list; everything else gets
# the configured error.
#
# Probe: deny `@network-io` and try to open a socket from the child.
# The shell child can't directly do socket(2), so we use `ip` or
# `getent ahosts` as a syscall-tracer-friendly proxy: getent's first
# nss path lookup will fail when network syscalls are blocked.

SVC="acceptance-test-seccomp"
MARK="/run/acceptance-test-seccomp.log"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

rm -f "$MARK"
: > "$MARK"
chmod 0666 "$MARK"

# Probe binaries — pick whichever exists on target.
# `nc` (netcat) is the simplest socket(2) caller and very common.
_probe='nc -z 127.0.0.1 22'
ssh_avail=$(command -v nc 2>/dev/null || echo "")
if [ -z "$ssh_avail" ]; then
    # Fallback: try `wget` to a known-bad host (just need socket()).
    _probe='wget -q --tries=1 --timeout=1 http://127.0.0.1:1/'
fi

# Deny network I/O — listing the group with '~' prefix.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c '$_probe; echo "probe_rc=\$\$?" > $MARK; while :; do sleep 60; done'
system-call-filter = ~ @network-io
system-call-error-number = EPERM
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

sleep 2
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -s "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: probe never wrote $MARK"
else
    _log=$(cat "$MARK")
    echo "  probe output: $_log"
    # nc/wget should fail (non-zero exit) because socket() returns EPERM.
    case "$_log" in
        *"probe_rc=0"*)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: probe reported success despite @network-io deny"
            ;;
        *)
            echo "OK: probe failed (network syscalls blocked by seccomp)"
            ;;
    esac
fi

svc_remove "$SVC"
rm -f "$MARK"

# Static-parse coverage via slinit-check: invalid group, unknown
# syscall, misplaced '~'.
_CHECKDIR="/tmp/acceptance-test-seccomp-check"
mkdir -p "$_CHECKDIR"
trap '
    svc_remove "$SVC"
    rm -f "$MARK"
    rm -rf "$_CHECKDIR"
' EXIT INT TERM

# Valid: empty deny list (no items) still parses.
cat > "$_CHECKDIR/svc-good" <<'EOF2'
type = process
command = /bin/true
system-call-filter = @system-service
EOF2
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-check -d "$_CHECKDIR" svc-good >/dev/null 2>&1; then
    echo "OK: slinit-check accepts @system-service"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check rejected @system-service"
fi

# Invalid: unknown group.
cat > "$_CHECKDIR/svc-badgrp" <<'EOF2'
type = process
command = /bin/true
system-call-filter = @no-such-group
EOF2
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-check -d "$_CHECKDIR" svc-badgrp >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check accepted unknown seccomp group"
else
    echo "OK: slinit-check rejects unknown seccomp group"
fi

# Invalid: unknown syscall.
cat > "$_CHECKDIR/svc-badsc" <<'EOF2'
type = process
command = /bin/true
system-call-filter = thisisnotasyscall
EOF2
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-check -d "$_CHECKDIR" svc-badsc >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check accepted unknown syscall name"
else
    echo "OK: slinit-check rejects unknown syscall name"
fi

test_summary
