#!/bin/sh
# 110-required-paths — `required-files` and `required-dirs` are
# pre-start guards. If any listed path is absent, slinit refuses to
# spawn the service.

SVC_MISSING="${ACCEPTANCE_NS_PREFIX}reqpath-missing"
SVC_PRESENT="${ACCEPTANCE_NS_PREFIX}reqpath-present"
MISSING_FILE="/tmp/acceptance-reqpath-nope-file"
MISSING_DIR="/tmp/acceptance-reqpath-nope-dir"
PRESENT_FILE="/tmp/acceptance-reqpath-present-file"
PRESENT_DIR="/tmp/acceptance-reqpath-present-dir"

cleanup() {
    svc_remove "$SVC_MISSING"
    svc_remove "$SVC_PRESENT"
    rm -rf "$MISSING_FILE" "$MISSING_DIR" "$PRESENT_FILE" "$PRESENT_DIR"
}
trap cleanup EXIT INT TERM
cleanup

# ---------------------------------------------------------------
# 1. Missing required-file → refuses to start
# ---------------------------------------------------------------
rm -f "$MISSING_FILE"
svc_deploy "$SVC_MISSING" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
required-files = $MISSING_FILE
restart = no
EOF

slinitctl --system start "$SVC_MISSING" 2>/dev/null || true
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$(svc_state "$SVC_MISSING")" in
    STOPPED|"")
        echo "OK: missing required-file kept service STOPPED"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: state=$(svc_state "$SVC_MISSING")"
        ;;
esac

# ---------------------------------------------------------------
# 2. Present required-file + required-dir → starts cleanly
# ---------------------------------------------------------------
touch "$PRESENT_FILE"
mkdir -p "$PRESENT_DIR"
svc_deploy "$SVC_PRESENT" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
required-files = $PRESENT_FILE
required-dirs = $PRESENT_DIR
restart = no
EOF

slinitctl --system start "$SVC_PRESENT" 2>/dev/null
wait_for_service "$SVC_PRESENT" STARTED 10
assert_eq "$(svc_state "$SVC_PRESENT")" "STARTED" \
    "service starts when all required paths present"

test_summary
