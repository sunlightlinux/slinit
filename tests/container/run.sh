#!/bin/sh
# run.sh — container-mode suite: slinit as PID 1 of a real container.
#
#   ./tests/container/run.sh                  # all cases
#   ./tests/container/run.sh cases/03-*.sh    # some cases
#   KEEP_IMAGE=1 ./tests/container/run.sh     # skip the image rebuild
#
# Each case is a host-side script: it writes service files, starts a
# container, drives it with the runtime CLI, and asserts on exit codes,
# timings, `slinitctl` output and logs. See README.md.
set -u

CT_DIR="$(cd "$(dirname "$0")" && pwd)"
export CT_DIR
. "$CT_DIR/lib.sh"

if ! "$RUNTIME" info >/dev/null 2>&1; then
    echo "container suite: '$RUNTIME' is not usable (daemon down, or no permission)" >&2
    exit 2
fi

if [ "${KEEP_IMAGE:-0}" != "1" ] || ! "$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "building $IMAGE from $BASE_IMAGE ..."
    build_image || { echo "image build failed" >&2; exit 2; }
fi

if [ $# -eq 0 ]; then
    set -- "$CT_DIR"/cases/*.sh
fi

passed=0 failed=0 failed_names=""
for case in "$@"; do
    name=$(basename "$case" .sh)
    out=$(sh -c '. "$CT_DIR/lib.sh"; . "$1"' _ "$case" 2>&1)
    if [ $? -eq 0 ]; then
        passed=$((passed + 1))
        echo "  PASS $name"
        [ "${VERBOSE:-0}" = "1" ] && echo "$out" | sed 's/^/    /'
    else
        failed=$((failed + 1))
        failed_names="$failed_names $name"
        echo "  FAIL $name"
        echo "$out" | sed 's/^/    /'
    fi
done

echo
echo "Results: $passed passed, $failed failed ($((passed + failed)) total)"
[ -n "$failed_names" ] && echo "Failed:$failed_names"
[ "$failed" -eq 0 ]
