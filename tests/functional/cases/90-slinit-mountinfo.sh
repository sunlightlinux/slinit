#!/bin/sh
# Test: slinit-mountinfo queries the running kernel's /proc/mounts
# with regex filters, output selectors, and the netdev-via-fstab
# lookup. Exercises the real /proc, plus a fixture --proc-mounts
# for deterministic filtering assertions.

wait_for_service "boot" "STARTED" 10

# --- Real /proc/mounts sanity ---

# The VM always has /proc mounted (proc pseudo-fs). Default mode
# lists mountpoints; /proc must be somewhere in the output.
out=$(slinit-mountinfo 2>&1)
assert_contains "$out" "/proc" "real /proc/mounts includes /proc"

# --fstype selector prints just the type. In practice /proc can be
# mounted twice (initramfs + system-init both do it), so take the
# first line — the type must still be "proc".
out=$(slinit-mountinfo --fstype /proc 2>&1 | head -1)
assert_eq "$out" "proc" "real /proc is fstype=proc"

# rootfs must NOT appear — the tool always skips that pseudo entry.
_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(slinit-mountinfo --fstype-regex 'rootfs' 2>&1)
if [ -z "$out" ]; then
    echo "OK: rootfs entry is skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rootfs leaked: $out"
fi

# --- Fixture-driven filter assertions ---

FIXTURE=/tmp/mounts-fixture
cat >"$FIXTURE" <<'EOF'
rootfs / rootfs rw 0 0
/dev/sda1 / ext4 rw,relatime 0 0
proc /proc proc rw 0 0
sysfs /sys sysfs rw,nosuid,nodev,noexec 0 0
tmpfs /run tmpfs rw,nosuid,mode=755 0 0
/dev/sda2 /home ext4 rw,noatime 0 0
//nfs.example/share /mnt/nfs nfs rw,vers=4 0 0
tmpfs /tmp tmpfs rw,mode=1777 0 0
EOF

# Default mountpoint output in reverse order — /tmp comes first,
# rootfs is skipped, / comes last.
out=$(slinit-mountinfo --proc-mounts "$FIXTURE" 2>&1)
first=$(echo "$out" | head -1)
last=$(echo "$out" | tail -1)
assert_eq "$first" "/tmp" "reverse order: /tmp first"
assert_eq "$last" "/" "reverse order: / last"

# --fstype-regex keeps every match.
out=$(slinit-mountinfo --proc-mounts "$FIXTURE" --fstype-regex '^ext4$' 2>&1)
assert_contains "$out" "/home" "ext4 filter includes /home"
assert_contains "$out" "/" "ext4 filter includes /"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"/tmp"*|*"/proc"*|*"/mnt/nfs"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ext4 filter leaked non-ext4: $out"
        ;;
    *)
        echo "OK: ext4 filter excludes non-ext4"
        ;;
esac

# --skip-fstype-regex drops matching types.
out=$(slinit-mountinfo --proc-mounts "$FIXTURE" \
    --skip-fstype-regex 'tmpfs|proc|sysfs' 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *tmpfs*|*/proc*|*/sys*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: skip regex leaked: $out"
        ;;
    *)
        echo "OK: skip regex excludes matching types"
        ;;
esac

# Output selectors.
out=$(slinit-mountinfo --proc-mounts "$FIXTURE" --node /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "/dev/sda2" "--node prints the device"

out=$(slinit-mountinfo --proc-mounts "$FIXTURE" --options /home 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "rw,noatime" "--options prints raw mount opts"

# --point-regex narrows by mountpoint pattern.
out=$(slinit-mountinfo --proc-mounts "$FIXTURE" --point-regex '^/(tmp|run)$' 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
lines=$(echo "$out" | wc -l)
if [ "$lines" = "2" ]; then
    echo "OK: --point-regex kept exactly 2 entries"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --point-regex kept $lines entries: $out"
fi

# --- Netdev filter (via fstab lookup) ---

FSTAB=/tmp/mountinfo-fstab
cat >"$FSTAB" <<'EOF'
/dev/sda1 / ext4 defaults 0 1
/dev/sda2 /home ext4 defaults 0 2
//nfs.example/share /mnt/nfs nfs _netdev,ro 0 0
EOF

# --netdev keeps only fstab entries with _netdev.
out=$(slinit-mountinfo --proc-mounts "$FIXTURE" --fstab "$FSTAB" --netdev 2>&1)
assert_eq "$(echo "$out" | tr -d '\n')" "/mnt/nfs" "--netdev keeps _netdev-tagged only"

# --nonetdev keeps fstab entries without _netdev; /mnt/nfs is out.
out=$(slinit-mountinfo --proc-mounts "$FIXTURE" --fstab "$FSTAB" --nonetdev 2>&1)
assert_contains "$out" "/home" "--nonetdev keeps /home"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    */mnt/nfs*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --nonetdev leaked _netdev entry: $out"
        ;;
    *)
        echo "OK: --nonetdev excludes _netdev entries"
        ;;
esac

# Non-absolute positional path is a usage error.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-mountinfo --proc-mounts "$FIXTURE" foo >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: relative positional should have errored"
else
    echo "OK: relative positional path rejected"
fi

rm -f "$FIXTURE" "$FSTAB"

test_summary
