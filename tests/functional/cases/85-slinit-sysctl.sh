#!/bin/sh
# Test: slinit-sysctl applies /etc/sysctl.d/*.conf to /proc/sys/*.
# Validates: discover, parse (dotted+slashed keys, `-` best-effort,
# comments), and apply against a real procfs, plus --strict override.

wait_for_service "boot" "STARTED" 10

# Pick a tunable that's always present on Linux and has a benign
# default range. kernel.printk is a 4-integer log-level line —
# writing to it is safe (only affects future dmesg formatting) and
# the kernel exposes it as /proc/sys/kernel/printk on every distro.
BEFORE=$(cat /proc/sys/kernel/printk 2>/dev/null || echo unknown)
echo "kernel.printk before: $BEFORE"

# Fixture: three entries — a dotted key, a slashed key, and a
# best-effort key targeting a tunable that does not exist on this
# kernel. The first two must apply cleanly; the third must be
# recorded as ignored, not as an error.
mkdir -p /etc/sysctl.d
cat >/etc/sysctl.d/99-slinittest.conf <<'EOF'
# End-to-end fixture for slinit-sysctl.
kernel.printk = 3 3 1 6
kernel/threads-max = 100000
-vm.this.tunable.does.not.exist = 42
EOF

# Pass the fixture path explicitly so Alpine's own /etc/sysctl.d
# entries don't inflate the counts and drown out the assertion.
output=$(slinit-sysctl --verbose /etc/sysctl.d/99-slinittest.conf 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$output" in
    *"applied=2 ignored=1 errors=0"*)
        echo "OK: verbose summary matches expectations"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: summary line: $output"
        ;;
esac

# Verify the printk value actually changed. The kernel formats it
# with tabs, so we compare after normalising whitespace.
after=$(cat /proc/sys/kernel/printk | tr -s '[:space:]' ' ' | sed 's/ $//')
assert_eq "$after" "3 3 1 6" "kernel.printk was written verbatim"

# Verify the slashed-key form also landed. threads-max writes a
# single integer.
current=$(cat /proc/sys/kernel/threads-max)
assert_eq "$current" "100000" "slashed key form applied"

# --strict must promote the best-effort miss to a hard error.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-sysctl --strict --verbose /etc/sysctl.d/99-slinittest.conf \
        >/tmp/sysctl.out 2>/tmp/sysctl.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --strict should have failed on the '-' key"
else
    err=$(cat /tmp/sysctl.err)
    case "$err" in
        *"does.not.exist"*|*"this/tunable"*)
            echo "OK: --strict reported the missing tunable"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: --strict stderr = $err"
            ;;
    esac
fi

# Malformed config (no '=') triggers a parse error with file+line.
cat >/etc/sysctl.d/99-slinittest.conf <<'EOF'
kernel.printk = 4 4 1 7
this line has no equals
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-sysctl /etc/sysctl.d/99-slinittest.conf \
        >/tmp/sysctl.out 2>/tmp/sysctl.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: malformed line should have failed the pass"
else
    err=$(cat /tmp/sysctl.err)
    case "$err" in
        *"99-slinittest.conf:2"*)
            echo "OK: parse error names the file and line"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: parse error missing file:line — $err"
            ;;
    esac
fi

# Cleanup so the fixture doesn't linger for other tests reusing the
# base VM (a stale /etc/sysctl.d entry would be a source of hard-to-
# debug flakes).
rm -f /etc/sysctl.d/99-slinittest.conf

test_summary
