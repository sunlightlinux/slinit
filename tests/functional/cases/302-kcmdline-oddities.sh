#!/bin/sh
# Test: odd kernel command-line words do not stop PID 1 or the boot.
#
# .d/kernel-args appends: sysvinit's `-z junk`, an unknown long option,
# a naked unknown word, `-F abc` (a value that does not parse),
# sysvinit's `-s`, `-t no-such-svc -t boot`, an unknown slinit.*
# selector, an invalid slinit.log-level, a KEY=VALUE word, a trailing
# `-b` left without its value, and ~1.5 KB of filler. The documented
# contract (slinit(8) KERNEL COMMAND LINE): unknown options are
# reported and ignored, a naked word is a service name only if it is
# `single`, `-t` is always honoured, and KEY=VALUE words land in the
# environment. A `-t` service that does not exist fails its own load;
# boot, named next to it, still starts.
#
# `-t` after a stray word used to be lost: flag parsing stopped at the
# first non-option, so `junk` hid every option after it, and boot was
# started only because it is the default. A bad `-F` value used to end
# the scan too; it is reported and skipped now, as an unknown option is.

CATCH_ALL=/run/slinit/catch-all.log
_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

echo "cmdline length: $(wc -c < /proc/cmdline)"
echo "PID 1 argv: $(tr "\0" " " < /proc/1/cmdline | cut -c1-200)"
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q 'pad9=' /proc/cmdline; then
    echo "OK: the long command line reached the kernel whole"
else
    echo "FAIL: the kernel truncated the command line; the case tests less than it says"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

wait_for_service boot STARTED 10
assert_eq "$(_state boot)" "STARTED" "boot started despite the odd words"
_out=$(timeout 5 slinitctl --system list 2>&1)
assert_not_contains "$_out" "some-unknown-word" "a naked unknown word is not taken as a service"

_log=$(cat "$CATCH_ALL" 2>/dev/null)
echo "--- catch-all:"; echo "$_log" | head -20 | cut -c1-200; echo "---"
# The "ignoring unrecognized option" notes go to the console before
# the catch-all log exists, so they cannot be read back here; what can
# is that options after the stray words still took effect: the -t
# no-such-svc that comes after `junk` and `some-unknown-word` was
# honoured, and so was -t boot.
assert_contains "$_log" "Failed to load 'no-such-svc'" "a -t after stray words is honoured (no-such-svc load fails)"
assert_contains "$_log" "Boot service 'boot' activation requested" "-t boot is honoured too"

# KEY=VALUE words from the command line are in the service environment.
_env=$(env)
assert_contains "$_env" "SLINIT_TEST_KV=kept" "a KEY=VALUE word reaches the service environment"

test_summary
