#!/bin/sh
# 212-journalctl-fss-verify — v2.1.0 a6371e1: Phase B binary
# format + FSS sealing. Full round-trip: mint a key, spin up a
# binary daemon with sealing, verify clean, tamper the on-disk
# file, verify FAIL. Fresh-boot version of acceptance case 218.

wait_for_service "boot" "STARTED" 15

KEY=/tmp/fss-key
DIR=/tmp/fss-journal
LOG=/tmp/fss-daemon.log
PID=/tmp/fss.pid
SOCK=/tmp/fss-events.sock
CTL=/tmp/fss.ctl

rm -f "$KEY" "$LOG" "$PID" "$SOCK" "$CTL"
rm -rf "$DIR"
mkdir -p "$DIR"

# Mint FSS key.
slinit-journalctl --setup-keys --fss-key="$KEY" --interval=1h >/dev/null 2>&1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$KEY" ]; then
    echo "OK: FSS key minted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: key not written"
    test_summary; return
fi

# Spin up sealed binary daemon; tag every entry so tag chain is
# dense (--fss-tag-every=1). Sleep enough for it to write at
# least the header + a few entries.
slinit-journald --format=binary --dir="$DIR" --fss-key="$KEY" \
    --pid-file="$PID" --socket="$SOCK" --admin-socket="$CTL" \
    --fss-tag-every=1 >/dev/null 2>"$LOG" &
sleep 2

JFILE=$(ls "$DIR"/*.journal 2>/dev/null | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$JFILE" ]; then
    echo "OK: binary journal file present ($JFILE)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no .journal file"
    tail "$LOG"
    test_summary; return
fi

# --verify clean.
_out=$(slinit-journalctl --verify --file="$JFILE" --fss-key="$KEY" 2>&1)
assert_contains "$_out" "OK" "sealed binary journal verifies clean"

# Stop daemon before tampering.
kill "$(cat "$PID")" 2>/dev/null
sleep 0.5

# Tamper: overwrite the tail + a mid-arena chunk with random
# bytes. Aggressive enough that either tag chain or object
# parse fails — both surface as non-OK.
_size=$(stat -c '%s' "$JFILE")
if [ "$_size" -gt 300 ]; then
    dd if=/dev/urandom of="$JFILE" bs=1 count=64 seek=$((_size - 64)) conv=notrunc 2>/dev/null
    dd if=/dev/urandom of="$JFILE" bs=1 count=32 seek=300 conv=notrunc 2>/dev/null
fi

_out=$(slinit-journalctl --verify --file="$JFILE" --fss-key="$KEY" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*"OK"*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: tampered journal verified as clean (FSS regression?)"
    echo "  --verify output: $_out"
    ;;
*)
    echo "OK: tampered journal surfaces non-OK ($_out)"
    ;;
esac

test_summary
