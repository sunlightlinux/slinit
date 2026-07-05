#!/bin/sh
# 85-slinit-sysctl — apply /etc/sysctl.d/*.conf entries to /proc/sys/*.
#
# Safe target: kernel.printk is a low-risk 4-integer knob that only
# affects future dmesg formatting. We capture its current value,
# write a fixture that pins it, apply, then restore.

CONF="/etc/sysctl.d/99-acceptance-test-slinit.conf"
BEFORE=$(cat /proc/sys/kernel/printk | tr -s '[:space:]' ' ' | sed 's/ $//')

cleanup() {
    rm -f "$CONF" /tmp/sysctl.err /tmp/sysctl.out
    # Restore printk to its pre-test value so a re-run and any other
    # cases behave identically.
    if [ -n "$BEFORE" ]; then
        printf '%s\n' "$BEFORE" >/proc/sys/kernel/printk 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM
cleanup

mkdir -p /etc/sysctl.d
cat > "$CONF" <<'EOF'
# 85-slinit-sysctl acceptance fixture — safe to apply.
kernel.printk = 3 3 1 6
kernel/threads-max = 100000
-vm.this.tunable.does.not.exist = 42
EOF

# Apply exactly this file (avoid summing distro entries into the total).
out=$(/usr/bin/slinit-sysctl --verbose "$CONF" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"applied=2 ignored=1 errors=0"*)
        echo "OK: summary reports applied=2 ignored=1 errors=0"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: summary: $out"
        ;;
esac

after=$(cat /proc/sys/kernel/printk | tr -s '[:space:]' ' ' | sed 's/ $//')
assert_eq "$after" "3 3 1 6" "kernel.printk was written verbatim"

current=$(cat /proc/sys/kernel/threads-max)
assert_eq "$current" "100000" "slashed-key form applied (kernel/threads-max)"

# --strict promotes the '-' key miss to a hard error.
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-sysctl --strict --verbose "$CONF" \
        >/tmp/sysctl.out 2>/tmp/sysctl.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --strict should have failed on the '-' key"
else
    err=$(cat /tmp/sysctl.err)
    case "$err" in
        *does.not.exist*|*this/tunable*)
            echo "OK: --strict reported the missing tunable"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: --strict stderr: $err"
            ;;
    esac
fi

# Malformed config triggers a file:line error.
cat > "$CONF" <<'EOF'
kernel.printk = 4 4 1 7
this line has no equals
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-sysctl "$CONF" >/tmp/sysctl.out 2>/tmp/sysctl.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: malformed config should have failed"
else
    err=$(cat /tmp/sysctl.err)
    case "$err" in
        *"99-acceptance-test-slinit.conf:2"*)
            echo "OK: parse error names file+line"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: parse error missing file+line: $err"
            ;;
    esac
fi

test_summary
