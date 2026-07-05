#!/bin/sh
# 90-slinit-mountinfo — query real /proc/mounts + fixture-driven
# regex/netdev filters.

FIXTURE="/tmp/acceptance-mountinfo-mounts"
FSTAB_FIX="/tmp/acceptance-mountinfo-fstab"
cleanup() {
    rm -f "$FIXTURE" "$FSTAB_FIX"
}
trap cleanup EXIT INT TERM
cleanup

assert_contains() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$1" in
        *"$2"*) echo "OK: $3" ;;
        *) _TESTS_FAILED=$((_TESTS_FAILED + 1))
           echo "FAIL: $3 — '$2' not in output" ;;
    esac
}

# --- Real /proc/mounts sanity ---
out=$(/usr/bin/slinit-mountinfo 2>&1)
assert_contains "$out" "/proc" "real /proc/mounts includes /proc"

out=$(/usr/bin/slinit-mountinfo --fstype /proc 2>&1 | head -1)
assert_eq "$out" "proc" "real /proc is fstype=proc"

_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(/usr/bin/slinit-mountinfo --fstype-regex 'rootfs' 2>&1)
if [ -z "$out" ]; then
    echo "OK: rootfs entry is skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rootfs leaked"
fi

# --- Fixture-driven ---
cat > "$FIXTURE" <<'EOF'
rootfs / rootfs rw 0 0
/dev/sda1 / ext4 rw,relatime 0 0
proc /proc proc rw 0 0
sysfs /sys sysfs rw,nosuid,nodev,noexec 0 0
tmpfs /run tmpfs rw,nosuid,mode=755 0 0
/dev/sda2 /home ext4 rw,noatime 0 0
//nfs.example/share /mnt/nfs nfs rw,vers=4 0 0
tmpfs /tmp tmpfs rw,mode=1777 0 0
EOF

out=$(/usr/bin/slinit-mountinfo --proc-mounts "$FIXTURE" 2>&1)
first=$(echo "$out" | head -1)
last=$(echo "$out" | tail -1)
assert_eq "$first" "/tmp" "reverse order: /tmp first"
assert_eq "$last" "/" "reverse order: / last"

out=$(/usr/bin/slinit-mountinfo --proc-mounts "$FIXTURE" --fstype-regex '^ext4$' 2>&1)
assert_contains "$out" "/home" "ext4 filter includes /home"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"/tmp"*|*"/proc"*|*"/mnt/nfs"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ext4 filter leaked"
        ;;
    *)
        echo "OK: ext4 filter excludes non-ext4"
        ;;
esac

out=$(/usr/bin/slinit-mountinfo --proc-mounts "$FIXTURE" --node /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "/dev/sda2" "--node prints the device"

out=$(/usr/bin/slinit-mountinfo --proc-mounts "$FIXTURE" --options /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "rw,noatime" "--options prints raw opts"

# --netdev / --nonetdev via fstab lookup
cat > "$FSTAB_FIX" <<'EOF'
/dev/sda1 / ext4 defaults 0 1
/dev/sda2 /home ext4 defaults 0 2
//nfs.example/share /mnt/nfs nfs _netdev,ro 0 0
EOF

out=$(/usr/bin/slinit-mountinfo --proc-mounts "$FIXTURE" --fstab "$FSTAB_FIX" --netdev 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "/mnt/nfs" "--netdev keeps _netdev only"

out=$(/usr/bin/slinit-mountinfo --proc-mounts "$FIXTURE" --fstab "$FSTAB_FIX" --nonetdev 2>&1)
assert_contains "$out" "/home" "--nonetdev keeps /home"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    */mnt/nfs*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --nonetdev leaked _netdev entry"
        ;;
    *)
        echo "OK: --nonetdev excludes _netdev entries"
        ;;
esac

# Non-absolute positional rejected.
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-mountinfo --proc-mounts "$FIXTURE" foo >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: relative positional should have errored"
else
    echo "OK: relative positional path rejected"
fi

test_summary
