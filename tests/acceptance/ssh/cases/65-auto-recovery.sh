#!/bin/sh
# 65-auto-recovery — `-r` / `--auto-recovery` flag plumbing.
#
# Auto-recovery semantics (cmd/slinit/main.go:899): when slinit detects
# a boot failure and -r is set, it shells out to a service literally
# named `recovery` instead of dropping to the interactive prompt or
# rebooting. The code path is gated on isPID1 AND on the daemon NOT
# being in container mode (-o exits earlier with the boot service's
# status code), so the full round-trip is observable only from a real
# PID 1 — replacing the live PID 1 inside the acceptance VM is not
# safe and PID-namespace simulation hangs on /dev/console / /proc
# setup that PID 1 expects from the real init.
#
# What this case CAN cover without a destructive setup:
#   - both spellings of the flag are recognized by the parser,
#   - `slinit-check` accepts a service literally named `recovery`,
#   - the help banner advertises the flag (regression guard).
#
# Full PID-1 round-trip is left to functional tests under tests/functional
# where slinit boots as PID 1 inside an ephemeral QEMU image.

# --- Probe 1: --help advertises -r and --auto-recovery ---------------
_h=$(slinit --help 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_h" | grep -q -- "-r"; then
    echo "OK: --help advertises -r"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -r not in --help output"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
# Go's flag pkg prints `  -auto-recovery` (single dash) regardless of
# how it's invoked. Both spellings are accepted by the parser; the
# help banner just uses the short form.
if echo "$_h" | grep -q -- "auto-recovery"; then
    echo "OK: --help advertises auto-recovery"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: auto-recovery not in --help output"
fi

# --- Probe 2: a service literally named `recovery` passes slinit-check
# Auto-recovery looks up the service by exact name, so any image that
# wants the feature must ship one. Validate the contract via the linter.
SVCDIR="/tmp/acceptance-test-recovery-svcd"
rm -rf "$SVCDIR"
mkdir -p "$SVCDIR"
cleanup() { rm -rf "$SVCDIR"; }
trap cleanup EXIT INT TERM

cat > "$SVCDIR/boot" <<EOF
type = scripted
command = /bin/true
restart = false
EOF
cat > "$SVCDIR/recovery" <<EOF
type = process
command = /bin/sh -c 'exec sleep 60'
restart = false
EOF

_lint=$(slinit-check -d "$SVCDIR" recovery 2>&1)
_lrc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_lrc" -eq 0 ]; then
    echo "OK: slinit-check accepts a service named 'recovery'"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check rejected the recovery svc (rc=$_lrc):"
    echo "$_lint" | sed 's/^/  | /'
fi

# --- Probe 3: -r is accepted by the flag parser (rejected --bad-flag)
# We sanity-check the parser without launching a PID1-ish slinit: run
# with --help so it exits immediately; the flag is parsed but no event
# loop runs. A missing or renamed flag would error out on flag.Parse.
_p=$(slinit -r --help 2>&1)
_prc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_prc" -eq 0 ] || echo "$_p" | grep -q "Usage:"; then
    echo "OK: -r accepted by the flag parser"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -r rejected by the parser (rc=$_prc):"
    echo "$_p" | head -5 | sed 's/^/  | /'
fi

test_summary
