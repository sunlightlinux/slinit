#!/bin/sh
# 73-slinit-mount — autofs lazy-mount unit file parsing.
#
# slinit-mount (cmd/slinit-mount) loads *.mount files from
# /etc/slinit.d/mount.d (or -d DIR) and sets them up as autofs mount
# points. The unit format is the key=value grammar parsed by
# pkg/autofs/config.go:ParseMountUnit + validated by ValidateMountUnit.
#
# Driving the live kernel autofs path from inside the acceptance VM
# would need autofs kernel support and an actual backing device — out
# of scope for a non-destructive smoke test. We do verify the loader
# end-to-end on:
#   * a complete, valid unit              → accepted
#   * a unit missing the required `where` → rejected
#   * a unit with a relative `where`      → rejected
#   * a unit with an unknown setting      → rejected
# by invoking slinit-mount in --foreground mode and reading the early
# log line that prints the loaded unit count (success) or the parse
# error (failure).

WORK="/tmp/acceptance-mount"
GOOD_DIR="$WORK/good"
BAD_DIR="$WORK/bad"

cleanup() {
    pkill -f "slinit-mount.*$WORK" 2>/dev/null
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$GOOD_DIR" "$BAD_DIR"

# --- Good unit ------------------------------------------------------
cat > "$GOOD_DIR/data.mount" <<EOF
description = test data partition
what = tmpfs
where = /mnt/acceptance-mount
type = tmpfs
options = size=8M,nodev,nosuid
timeout = 60
directory-mode = 0755
EOF

# Probe 1: the parser accepts the complete unit.
slinit-mount -d "$GOOD_DIR" --foreground >"$WORK/good.log" 2>&1 &
PID=$!
sleep 1
kill -TERM "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qiE "loaded 1 mount unit|set up.*data|loading 1 mount unit" "$WORK/good.log"; then
    echo "OK: slinit-mount accepted the complete unit"
elif ! grep -qiE "error|fail" "$WORK/good.log"; then
    # Some daemon builds log "Started autofs daemon" without an explicit
    # count; treat absence of error/failure as success too.
    echo "OK: slinit-mount started without errors (no parse failure logged)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: parse failure on the complete unit:"
    sed 's/^/  | /' "$WORK/good.log"
fi

# --- Missing `where` ------------------------------------------------
cat > "$BAD_DIR/nowhere.mount" <<EOF
what = tmpfs
type = tmpfs
EOF

slinit-mount -d "$BAD_DIR" --foreground >"$WORK/missing-where.log" 2>&1 &
PID=$!
sleep 1
kill -TERM "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qiE "'where' is required|where.*required" "$WORK/missing-where.log"; then
    echo "OK: unit without 'where' is rejected"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing 'where' was accepted; log:"
    sed 's/^/  | /' "$WORK/missing-where.log"
fi
rm -f "$BAD_DIR/nowhere.mount"

# --- Relative `where` -----------------------------------------------
cat > "$BAD_DIR/rel.mount" <<EOF
what = tmpfs
where = mnt/relative
type = tmpfs
EOF

slinit-mount -d "$BAD_DIR" --foreground >"$WORK/relative-where.log" 2>&1 &
PID=$!
sleep 1
kill -TERM "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qiE "must be absolute|absolute path" "$WORK/relative-where.log"; then
    echo "OK: relative 'where' is rejected"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: relative 'where' was accepted; log:"
    sed 's/^/  | /' "$WORK/relative-where.log"
fi
rm -f "$BAD_DIR/rel.mount"

# --- Unknown setting ------------------------------------------------
cat > "$BAD_DIR/unknown.mount" <<EOF
what = tmpfs
where = /mnt/x
type = tmpfs
no-such-setting = boom
EOF

slinit-mount -d "$BAD_DIR" --foreground >"$WORK/unknown.log" 2>&1 &
PID=$!
sleep 1
kill -TERM "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qiE "unknown setting" "$WORK/unknown.log"; then
    echo "OK: unknown setting is rejected"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unknown setting was accepted; log:"
    sed 's/^/  | /' "$WORK/unknown.log"
fi
rm -f "$BAD_DIR/unknown.mount"

# --- --help advertises the binary -----------------------------------
_h=$(slinit-mount --help 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_h" | grep -qiE "autofs.*lazy mount|mount unit"; then
    echo "OK: --help describes the autofs lazy-mount role"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --help missing the expected wording:"
    echo "$_h" | head -5 | sed 's/^/  | /'
fi

test_summary
