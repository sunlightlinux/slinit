#!/bin/sh
# Test: slinit-fstabinfo end-to-end against a fixture fstab.
# Validates: output selectors (--blockdevice, --options, --mountargs,
# --passno), filters (--fstype, --passno OP N, positional), the
# `--file` seam, and EINFO_QUIET.

wait_for_service "boot" "STARTED" 10

FSTAB=/tmp/fstabinfo-fixture
cat >"$FSTAB" <<'EOF'
# fixture
/dev/sda1  /       ext4  defaults,noatime  0 1
/dev/sda2  none    swap  sw                0 0
UUID=abc   /home   ext4  rw,relatime       0 2
UUID=def   /boot   vfat  defaults          0 2
tmpfs      /tmp    tmpfs mode=1777         0 0
EOF

# Default: list every mountpoint.
out=$(slinit-fstabinfo --file "$FSTAB" 2>&1)
assert_contains "$out" "/" "default lists root"
assert_contains "$out" "/home" "default lists /home"
assert_contains "$out" "/tmp" "default lists /tmp"

# --blockdevice for a specific mountpoint.
out=$(slinit-fstabinfo --file "$FSTAB" --blockdevice /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "UUID=abc" "--blockdevice returns UUID"

# --options for /.
out=$(slinit-fstabinfo --file "$FSTAB" --options / 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "defaults,noatime" "--options returns raw mount opts"

# --mountargs formats a mount(8) command line.
out=$(slinit-fstabinfo --file "$FSTAB" --mountargs /tmp 2>&1)
assert_contains "$out" "-o mode=1777" "--mountargs preserves options field"
assert_contains "$out" "-t tmpfs" "--mountargs includes type"
assert_contains "$out" "tmpfs /tmp" "--mountargs has spec+mountpoint"

# --fstype filter (comma-separated OK).
out=$(slinit-fstabinfo --file "$FSTAB" --fstype ext4 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"/"*)
        # / and /home should both appear; swap+vfat+tmpfs must not.
        case "$out" in
            *"/tmp"*|*"/boot"*|*none*)
                _TESTS_FAILED=$((_TESTS_FAILED + 1))
                echo "FAIL: --fstype ext4 leaked non-ext4: $out"
                ;;
            *)
                echo "OK: --fstype ext4 filters correctly"
                ;;
        esac
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --fstype ext4 empty: $out"
        ;;
esac

# --passno = 2 filter should keep /home and /boot only.
out=$(slinit-fstabinfo --file "$FSTAB" --passno =2 2>&1)
assert_contains "$out" "/home" "--passno =2 keeps /home"
assert_contains "$out" "/boot" "--passno =2 keeps /boot"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"/tmp"*|*"/ "*|*"/\n"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --passno =2 leaked pass!=2: $out"
        ;;
    *)
        echo "OK: --passno =2 excludes non-2 entries"
        ;;
esac

# Plain --passno <mountpoint> prints just that mount's passno.
out=$(slinit-fstabinfo --file "$FSTAB" --passno /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "2" "--passno /home prints the passno"

# Empty result: query for a mountpoint that doesn't exist.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-fstabinfo --file "$FSTAB" --blockdevice /nowhere >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing mountpoint should exit non-zero"
else
    echo "OK: missing mountpoint yields non-zero exit"
fi

# EINFO_QUIET suppresses output but keeps the exit code.
out=$(EINFO_QUIET=yes slinit-fstabinfo --file "$FSTAB" --blockdevice /home 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: EINFO_QUIET suppresses stdout"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: EINFO_QUIET leaked: $out"
fi

rm -f "$FSTAB"

test_summary
