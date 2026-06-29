#!/bin/sh
# 82-service-dirs-abs — daemon resolves --services-dir paths to
# absolute before returning them over the control protocol.
#
# Dinit-parity (upstream 044b950 + 1300c63): when slinitctl asks the
# daemon for its service directories (used by `slinitctl service-dirs`
# and by the offline-mode CLI fallback), the wire reply must carry
# absolute paths. Before the fix, a daemon launched with
# `--services-dir ./relative/path` would echo the literal string back,
# and clients that joined that against their own cwd would silently
# look at the wrong tree.
#
# pkg/control/connection.go handleQueryServiceDscDir now runs every
# entry through filepath.Abs() before encoding the reply, so any
# starts-with-'/' assertion on the client side now holds.
#
# We exercise it with a nested slinit launched from a temp working
# directory, pointed at a relative service dir; then call `slinitctl
# service-dirs` over that daemon's control socket and verify each
# returned path is absolute and resolves back to the dir the test set
# up.

WORK="/tmp/acceptance-svcdirs-abs"
SOCKET="$WORK/slinit.sock"
LOG="$WORK/slinit.log"
SVCDIR_NAME="svc.d"
ABS_SVCDIR="$WORK/$SVCDIR_NAME"
SLINIT_PID=""

cleanup() {
    if [ -n "$SLINIT_PID" ]; then
        kill -TERM "$SLINIT_PID" 2>/dev/null
        for _ in 1 2 3; do
            kill -0 "$SLINIT_PID" 2>/dev/null || break
            sleep 1
        done
        kill -KILL "$SLINIT_PID" 2>/dev/null
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$ABS_SVCDIR"

cat > "$ABS_SVCDIR/boot" <<EOF
type = internal
EOF

# Launch nested slinit from $WORK with a *relative* --services-dir.
# The bare path "svc.d" is what would have leaked through to the
# client before the parity fix.
(cd "$WORK" && nohup slinit -o -m -p "$SOCKET" \
    -d "$SVCDIR_NAME" -t boot >"$LOG" 2>&1) &
SLINIT_PID=$!

_e=0
while [ "$_e" -lt 6 ]; do
    [ -S "$SOCKET" ] && break
    sleep 1
    _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S "$SOCKET" ]; then
    echo "OK: nested slinit booted with relative --services-dir"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: socket never appeared:"
    tail -15 "$LOG" 2>/dev/null | sed 's/^/  | /'
    test_summary
    exit 1
fi

# --- Probe: every returned path starts with '/' --------------------
_dirs=$(slinitctl --socket-path "$SOCKET" service-dirs 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_dirs" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 'service-dirs' returned no output"
    test_summary
    exit 1
fi

# Filter to actual path lines (drop blank lines / headers). Then
# check each one starts with '/' AND the relative bare-name 'svc.d'
# never appears as a standalone token at the start of any line.
_rel_count=0
_abs_count=0
_first_path=""
while IFS= read -r line; do
    case "$line" in
        /*)
            _abs_count=$((_abs_count + 1))
            [ -z "$_first_path" ] && _first_path="$line"
            ;;
        "$SVCDIR_NAME"|"$SVCDIR_NAME"/*)
            _rel_count=$((_rel_count + 1))
            ;;
    esac
done <<EOF
$_dirs
EOF

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_abs_count" -ge 1 ] && [ "$_rel_count" -eq 0 ]; then
    echo "OK: every service-dirs entry is absolute (count=$_abs_count)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: abs=$_abs_count rel=$_rel_count; output was:"
    echo "$_dirs" | sed 's/^/  | /'
fi

# --- Probe: the absolute path actually resolves to our svc dir ------
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_first_path" = "$ABS_SVCDIR" ]; then
    echo "OK: returned path matches the expected absolute target"
elif [ -d "$_first_path" ] && [ -e "$_first_path/boot" ]; then
    # Symlink chains / different cwd resolution may give an equivalent
    # but not identical string; the test only insists on absoluteness
    # AND content match (the boot file is unique to our setup).
    echo "OK: returned path '$_first_path' contains our boot file"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: '$_first_path' doesn't point at our svc dir ($ABS_SVCDIR)"
fi

test_summary
