#!/bin/sh
# 21-waits-for — soft dependency. PARENT `waits-for = CHILD` means PARENT
# will trigger CHILD's start, but PARENT must still reach STARTED even when
# CHILD fails (unlike depends-on, which propagates the failure).

PARENT="acceptance-test-wf-parent"
CHILD="acceptance-test-wf-child"

cleanup() {
    svc_remove "$PARENT" "$CHILD"
}
trap cleanup EXIT INT TERM

# CHILD that fails on start. No restart so it stays failed.
svc_deploy "$CHILD" <<EOF
type = scripted
command = /bin/sh -c 'exit 1'
restart = false
EOF

# PARENT runs as a long-lived process; waits-for asks slinit to bring
# CHILD up alongside it but does not gate PARENT on CHILD's success.
# Dependency keywords (waits-for, depends-on, before, after, prepared-by)
# require the `:` operator. With `=` the daemon rejects the description.
svc_deploy "$PARENT" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
waits-for: $CHILD
restart = false
EOF

slinitctl --system start "$PARENT" >/dev/null 2>&1
wait_for_service "$PARENT" "STARTED" 10 || true

# PARENT must be STARTED despite CHILD's failure.
assert_service_state "$PARENT" "STARTED" "$PARENT STARTED despite child failure"

# CHILD must NOT be STARTED — it failed. Acceptable end-states are
# STOPPED or FAILED; the precise label depends on slinit's classification
# of an exit-1 scripted service.
_child_state=$(svc_state "$CHILD")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_child_state" in
    STOPPED|FAILED)
        echo "OK: $CHILD is '$_child_state' (failed soft-dep, parent unaffected)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $CHILD in unexpected state '$_child_state'"
        ;;
esac

test_summary
