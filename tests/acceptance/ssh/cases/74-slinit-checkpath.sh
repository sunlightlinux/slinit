#!/bin/sh
# 74-slinit-checkpath — declarative file/dir/pipe creation with mode+owner.
#
# slinit-checkpath (cmd/slinit-checkpath) is the slinit answer to OpenRC's
# checkpath(8): a pre-start helper that ensures a path exists with a
# specified type, mode, and owner. Used heavily by services that need
# their runtime dirs in /run or /var before the main command fires.
#
# Flags exercised:
#   -d       directory  (mkdir if missing)
#   -D       directory  (mkdir AND empty)
#   -f       regular file (touch)
#   -F       regular file (truncate to zero)
#   -p       FIFO       (mkfifo)
#   -m MODE  apply chmod
#   -o USER:GROUP  apply chown
#   -W       success if already writable (idempotent fast path)

WORK="/tmp/acceptance-cpath"

cleanup() {
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"

# --- Probe 1: -d creates a missing directory with mode 0750 ----------
TGT="$WORK/svc-rundir"
slinit-checkpath -d -m 0750 "$TGT" >/tmp/cpath.out 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ] && [ -d "$TGT" ]; then
    _mode=$(stat -c '%a' "$TGT" 2>/dev/null)
    if [ "$_mode" = "750" ]; then
        echo "OK: -d created $TGT with mode 0750"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: dir created but mode=$_mode (want 750)"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -d failed (rc=$_rc):"
    sed 's/^/  | /' /tmp/cpath.out 2>/dev/null
fi

# --- Probe 2: -D truncates an existing dir to empty -----------------
TGT="$WORK/needs-truncate"
mkdir -p "$TGT"
: > "$TGT/leftover-file"
mkdir -p "$TGT/leftover-dir"
slinit-checkpath -D "$TGT" >/tmp/cpath.out 2>&1
_rc=$?
_count=$(ls -A "$TGT" 2>/dev/null | wc -l)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ] && [ "$_count" -eq 0 ]; then
    echo "OK: -D emptied an existing directory"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -D didn't empty $TGT (rc=$_rc, leftover=$_count):"
    ls -la "$TGT" 2>/dev/null | sed 's/^/  | /'
fi

# --- Probe 3: -f creates a missing regular file ---------------------
TGT="$WORK/marker.lock"
slinit-checkpath -f -m 0640 "$TGT" >/tmp/cpath.out 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ] && [ -f "$TGT" ]; then
    _mode=$(stat -c '%a' "$TGT" 2>/dev/null)
    if [ "$_mode" = "640" ]; then
        echo "OK: -f created regular file with mode 0640"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: file created but mode=$_mode (want 640)"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -f failed (rc=$_rc):"
    sed 's/^/  | /' /tmp/cpath.out 2>/dev/null
fi

# --- Probe 4: -F truncates an existing file -------------------------
TGT="$WORK/needs-trunc.log"
printf 'old contents\n' > "$TGT"
slinit-checkpath -F "$TGT" >/tmp/cpath.out 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ] && [ -f "$TGT" ] && [ ! -s "$TGT" ]; then
    echo "OK: -F truncated $TGT to zero bytes"
else
    _size=$(stat -c '%s' "$TGT" 2>/dev/null || echo "?")
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -F left $TGT at size=$_size (rc=$_rc)"
fi

# --- Probe 5: -p creates a FIFO -------------------------------------
TGT="$WORK/svc.pipe"
slinit-checkpath -p "$TGT" >/tmp/cpath.out 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ] && [ -p "$TGT" ]; then
    echo "OK: -p created a FIFO"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -p didn't produce a FIFO (rc=$_rc):"
    ls -la "$TGT" 2>/dev/null | sed 's/^/  | /'
fi

# --- Probe 6: -o sets ownership (nobody) ----------------------------
# Owner uid 65534 on Void; map by name to stay portable.
TGT="$WORK/owned"
slinit-checkpath -d -m 0755 -o nobody:nogroup "$TGT" >/tmp/cpath.out 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ] && [ -d "$TGT" ]; then
    _own=$(stat -c '%U:%G' "$TGT" 2>/dev/null)
    case "$_own" in
        nobody:nogroup|nobody:nobody)
            echo "OK: -o set owner to '$_own'"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: -o owner mismatch: '$_own'"
            ;;
    esac
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -o invocation failed (rc=$_rc):"
    sed 's/^/  | /' /tmp/cpath.out 2>/dev/null
fi

# --- Probe 7: multiple PATHs in one invocation ----------------------
A="$WORK/a"; B="$WORK/b"; C="$WORK/c"
slinit-checkpath -d "$A" "$B" "$C" >/tmp/cpath.out 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ] && [ -d "$A" ] && [ -d "$B" ] && [ -d "$C" ]; then
    echo "OK: multi-path invocation created a, b, c"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: multi-path failed (rc=$_rc, a=$([ -d "$A" ] && echo Y || echo N) b=$([ -d "$B" ] && echo Y || echo N) c=$([ -d "$C" ] && echo Y || echo N))"
fi

# --- Probe 8: error path on missing TYPE flag -----------------------
slinit-checkpath "$WORK/no-type" >/tmp/cpath.out 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: invocation without a TYPE flag fails ($_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: invocation without TYPE flag succeeded (should error)"
fi

rm -f /tmp/cpath.out

test_summary
