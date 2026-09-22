#!/bin/sh
# lib.sh — shared helpers for the container-mode suite.
#
# Every case runs slinit as PID 1 of a real container (`slinit -o`), which
# is what a Docker/Podman user gets and what the acceptance suite does not
# exercise: acceptance case 69 drives `slinit -o` as a child of a shell,
# so none of the isPID1 && containerMode paths run there.
#
# Only Docker is tested. CONTAINER_RUNTIME=podman is accepted because the
# commands used here are CLI-compatible, but nothing has been run under it.

RUNTIME="${CONTAINER_RUNTIME:-docker}"
IMAGE="${SLINIT_TEST_IMAGE:-slinit-container-test:local}"
BASE_IMAGE="${BASE_IMAGE:-busybox:1.37-musl}"

# run.sh and soak.sh export CT_DIR; $0 would point into cases/ otherwise.
CT_DIR="${CT_DIR:-$(cd "$(dirname "$0")" && pwd)}"
REPO_DIR="$(cd "$CT_DIR/../.." && pwd)"
BUILD_DIR="$CT_DIR/_build"

_TESTS_RUN=0
_TESTS_FAILED=0

pass() { _TESTS_RUN=$((_TESTS_RUN + 1)); echo "OK: $*"; }
fail() { _TESTS_RUN=$((_TESTS_RUN + 1)); _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: $*"; }

# check COND-EXIT-STATUS MESSAGE — pass or fail on the previous command.
check() {
    if [ "$1" -eq 0 ]; then pass "$2"; else fail "$2"; fi
}

summary() {
    echo "--- $_TESTS_RUN checks, $_TESTS_FAILED failed ---"
    [ "$_TESTS_FAILED" -eq 0 ]
}

# build_image compiles the binaries statically and wraps them in a
# busybox image: a shell, ps and sleep are all the cases need, and a
# 1.5 MB base keeps a 100-cycle soak about slinit rather than the image.
build_image() {
    mkdir -p "$BUILD_DIR/bin"
    for c in slinit slinitctl slinit-runner; do
        (cd "$REPO_DIR" && CGO_ENABLED=0 go build -trimpath -o "$BUILD_DIR/bin/$c" "./cmd/$c") || return 1
    done
    printf 'FROM %s\nCOPY bin/ /sbin/\n' "$BASE_IMAGE" > "$BUILD_DIR/Dockerfile"
    "$RUNTIME" build -q -t "$IMAGE" "$BUILD_DIR" >/dev/null
}

# new_svcdir prints a fresh directory for one case's service files.
new_svcdir() {
    d=$(mktemp -d "${TMPDIR:-/tmp}/slinit-ct.XXXXXX")
    chmod 755 "$d"
    echo "$d"
}

# ct_start NAME SVCDIR [RUNTIME-ARGS...] -- [SLINIT-ARGS...]
# Starts a detached container running `slinit -o` as PID 1 with SVCDIR
# as /etc/slinit.d. Anything before `--` goes to `$RUNTIME run`, anything
# after it to slinit.
ct_start() {
    _name=$1 _svc=$2
    shift 2
    _rargs=""
    while [ $# -gt 0 ] && [ "$1" != "--" ]; do
        _rargs="$_rargs $1"
        shift
    done
    [ $# -gt 0 ] && [ "$1" = "--" ] && shift
    "$RUNTIME" rm -f "$_name" >/dev/null 2>&1
    # shellcheck disable=SC2086
    "$RUNTIME" run -d --name "$_name" -v "$_svc:/etc/slinit.d:ro" $_rargs \
        "$IMAGE" /sbin/slinit -o "$@" >/dev/null
}

# ct_wait_ready NAME [SECONDS] — wait until the boot service is up, as
# seen through slinitctl over the container's own control socket.
ct_wait_ready() {
    _i=0
    while [ "$_i" -lt "$((${2:-10} * 5))" ]; do
        "$RUNTIME" exec "$1" slinitctl list 2>/dev/null | grep -q '\[\[+\] *\] boot' && return 0
        sleep 0.2
        _i=$((_i + 1))
    done
    return 1
}

# ct_wait_exit NAME SECONDS — wait for the container to stop on its own.
ct_wait_exit() {
    _i=0
    while [ "$_i" -lt "$(($2 * 5))" ]; do
        [ "$("$RUNTIME" inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "false" ] && return 0
        sleep 0.2
        _i=$((_i + 1))
    done
    return 1
}

ct_exit_code() { "$RUNTIME" inspect -f '{{.State.ExitCode}}' "$1"; }

# ct_stop_ms NAME [TIMEOUT] — `stop` the container, print how long it took
# in milliseconds. The runtime SIGKILLs after TIMEOUT (default 10s); the
# exit code tells whether that happened (137).
ct_stop_ms() {
    _t0=$(date +%s%N)
    "$RUNTIME" stop -t "${2:-10}" "$1" >/dev/null
    _t1=$(date +%s%N)
    echo $(((_t1 - _t0) / 1000000))
}

ct_logs() { "$RUNTIME" logs "$1" 2>&1; }

ct_rm() { "$RUNTIME" rm -f "$1" >/dev/null 2>&1; }
