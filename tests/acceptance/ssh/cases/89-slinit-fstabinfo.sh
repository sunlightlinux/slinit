#!/bin/sh
# 89-slinit-fstabinfo — fixture-driven query of /etc/fstab clone.

FSTAB="/tmp/acceptance-fstabinfo-fixture"
cleanup() {
    rm -f "$FSTAB"
}
trap cleanup EXIT INT TERM
cleanup

cat > "$FSTAB" <<'EOF'
# fixture
/dev/sda1  /       ext4  defaults,noatime  0 1
/dev/sda2  none    swap  sw                0 0
UUID=abc   /home   ext4  rw,relatime       0 2
UUID=def   /boot   vfat  defaults          0 2
tmpfs      /tmp    tmpfs mode=1777         0 0
EOF

# Default: lists every mountpoint.
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" 2>&1)
assert_contains() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$1" in
        *"$2"*) echo "OK: $3" ;;
        *) _TESTS_FAILED=$((_TESTS_FAILED + 1))
           echo "FAIL: $3 — '$2' not in output" ;;
    esac
}
assert_contains "$out" "/" "default lists root"
assert_contains "$out" "/home" "default lists /home"
assert_contains "$out" "/tmp" "default lists /tmp"

# --blockdevice for /home
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" --blockdevice /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "UUID=abc" "--blockdevice returns UUID"

# --options
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" --options / 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "defaults,noatime" "--options returns raw opts"

# --mountargs
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" --mountargs /tmp 2>&1)
assert_contains "$out" "-o mode=1777" "--mountargs preserves options"
assert_contains "$out" "-t tmpfs" "--mountargs includes type"

# --fstype filter
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" --fstype ext4 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"/tmp"*|*"/boot"*|*none*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --fstype ext4 leaked non-ext4: $out"
        ;;
    *"/"*)
        echo "OK: --fstype ext4 filters correctly"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --fstype ext4 empty"
        ;;
esac

# --passno =2 keeps /home + /boot
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" --passno =2 2>&1)
assert_contains "$out" "/home" "--passno =2 keeps /home"
assert_contains "$out" "/boot" "--passno =2 keeps /boot"

# Plain --passno <mountpoint> is a NAME, not a filter — OpenRC's
# fstabinfo.c adds the mountpoint to the same list positional arguments
# go into, and its `else if (!filtered)` branch then appends every fstab
# entry because the plain form does not set `filtered`. So the real tool
# prints /home's passno and then each entry's, and slinit matches it.
#
# This case used to assert "2" alone, from before slinit-fstabinfo was
# rewritten to follow OpenRC's union semantics. The functional suite had
# the same stale assertion and was corrected first; this copy was missed,
# which is what a test duplicated across two suites costs when only one
# of them is in front of you.
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" --passno /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "210220" \
    "--passno /home prints /home's passno then every entry's (OpenRC)"

# Naming the mountpoint positionally as well is the documented way to
# get just one passno: the positional argument narrows the list.
out=$(/usr/bin/slinit-fstabinfo --file "$FSTAB" --passno /home /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "2" \
    "--passno /home /home prints only /home's passno"

# Missing mountpoint → non-zero.
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-fstabinfo --file "$FSTAB" --blockdevice /nowhere >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing mountpoint should exit non-zero"
else
    echo "OK: missing mountpoint yields non-zero exit"
fi

# EINFO_QUIET suppresses output.
out=$(EINFO_QUIET=yes /usr/bin/slinit-fstabinfo --file "$FSTAB" --blockdevice /home 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: EINFO_QUIET suppresses stdout"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: EINFO_QUIET leaked: $out"
fi

test_summary
