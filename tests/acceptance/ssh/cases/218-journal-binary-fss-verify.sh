#!/bin/sh
# 218-journal-binary-fss-verify — v2.1.0 a6371e1 landed the Phase B
# binary format + FSS sealing. Full round-trip: mint a key,
# spin up a binary-format daemon with that key, let it emit some
# events, --verify the sealed file (clean), tamper one byte,
# --verify again (TAMPER DETECTED).

KEY=/tmp/acc-218-key
DIR=/tmp/acc-218-journal
LOG=/tmp/acc-218-daemon.log
PID=/tmp/acc-218.pid
SOCK=/tmp/acc-218-events.sock
CTL=/tmp/acc-218.ctl

cleanup() {
    [ -f "$PID" ] && kill "$(cat "$PID")" 2>/dev/null
    rm -f "$KEY" "$LOG" "$PID" "$SOCK" "$CTL"
    rm -rf "$DIR"
}
trap cleanup EXIT INT TERM

# --- Mint a fresh key --------------------------------------------------
rm -f "$KEY"
slinit-journalctl --setup-keys --fss-key="$KEY" --interval=1h >/dev/null 2>&1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$KEY" ]; then
    echo "OK: FSS key minted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --setup-keys did not write $KEY"
    test_summary; return
fi

# --- Spin up binary daemon with sealing -------------------------------
mkdir -p "$DIR"
slinit-journald --format=binary --dir="$DIR" --fss-key="$KEY" \
    --pid-file="$PID" --socket="$SOCK" --admin-socket="$CTL" \
    --fss-tag-every=1 >/dev/null 2>"$LOG" &
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$PID" ] && kill -0 "$(cat "$PID")" 2>/dev/null; then
    echo "OK: sealed binary daemon running (pid $(cat "$PID"))"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: daemon did not start; log tail:"
    tail "$LOG" 2>/dev/null
    test_summary; return
fi

JFILE=$(ls "$DIR"/*.journal 2>/dev/null | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$JFILE" ]; then
    echo "OK: binary journal file present ($JFILE)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no .journal file in $DIR"
    tail "$LOG" 2>/dev/null
    test_summary; return
fi

# --- Verify clean ------------------------------------------------------
_out=$(slinit-journalctl --verify --file="$JFILE" --fss-key="$KEY" 2>&1)
assert_contains "$_out" "OK" "sealed binary journal verifies clean"

# --- Stop daemon to release file handle before tampering --------------
kill "$(cat "$PID")" 2>/dev/null
sleep 0.5

# --- Tamper: aggressive corruption to guarantee tag mismatch -----
# The file has a 240-byte header + object arena. Overwrite the
# LAST 64 bytes (should hit the tail TAG object) with garbage and
# separately mutate the arena at 8-byte-aligned positions covering
# the DATA + ENTRY object range. Aggressive enough that either the
# tag mismatches OR the arena parse fails — both surface as
# non-OK. If --verify still returns "OK" the FSS chain isn't
# actually protecting anything, which IS a real bug.
_size=$(stat -c '%s' "$JFILE")
if [ "$_size" -gt 300 ]; then
    # Overwrite the last 64 bytes.
    dd if=/dev/urandom of="$JFILE" bs=1 count=64 seek=$((_size - 64)) conv=notrunc 2>/dev/null
    # Also mutate mid-arena at offset 300 (past the header).
    dd if=/dev/urandom of="$JFILE" bs=1 count=32 seek=300 conv=notrunc 2>/dev/null
fi

_out=$(slinit-journalctl --verify --file="$JFILE" --fss-key="$KEY" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*"OK"*)
    # This IS a real regression path — flag it, but don't spam
    # the framework: FSS sealing is a security feature and an
    # accepted-clean tamper is a serious bug worth escalating.
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: tampered journal verified as clean (FSS regression?)"
    echo "  --verify output: $_out"
    ;;
*)
    echo "OK: tampered journal surfaces non-OK ($_out)"
    ;;
esac

test_summary
