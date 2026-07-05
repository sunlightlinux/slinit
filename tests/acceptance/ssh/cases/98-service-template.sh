#!/bin/sh
# 98-service-template — `service@arg` template instantiation and `$1`
# substitution.
#
# slinit resolves `foo@alpha` by loading the file `foo` (or `foo@`)
# and substituting the argument (`alpha`) wherever the template
# references `$1` in commands. Each instance carries its own state
# machine and lifecycle.

TMPL="${ACCEPTANCE_NS_PREFIX}tmpl"
INST_A="${TMPL}@alpha"
INST_B="${TMPL}@beta"
RESULT_DIR="/tmp/acceptance-tmpl"

cleanup() {
    slinitctl --system --ignore-unstarted stop "$INST_A" 2>/dev/null || true
    slinitctl --system --ignore-unstarted stop "$INST_B" 2>/dev/null || true
    slinitctl --system unload "$INST_A" 2>/dev/null || true
    slinitctl --system unload "$INST_B" 2>/dev/null || true
    svc_remove "$TMPL"
    rm -rf "$RESULT_DIR"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$RESULT_DIR"

# Template: writes its argument into a per-instance file and stays
# alive. Uses $$1 to survive slinit's parser env expansion —
# runtime shell `$1` reaches the child.
svc_deploy "$TMPL" <<EOF
type = process
command = /bin/sh -c "echo instance=\$1 > $RESULT_DIR/\$1-result; while true; do sleep 60; done" alpha
EOF
# Note: the trailing "alpha" positional is a decoy that gets
# overwritten by the @arg mechanism; slinit uses $1 for the
# service argument, not a shell parameter.

# Actually — slinit's $1 substitution rewrites the command text at
# parse time; that means we can't test with a plain shell $1 (the
# parser's expandEnvVars sees $1 and interprets it as the service
# arg). Use a template that puts the arg in the file name path,
# which is the canonical pattern.
svc_deploy "$TMPL" <<EOF
type = process
command = /bin/sh -c 'echo instance=\$0 > $RESULT_DIR/\$0-result; while true; do sleep 60; done' \$1
EOF

# Start two instances with different args.
slinitctl --system start "$INST_A" 2>/dev/null
slinitctl --system start "$INST_B" 2>/dev/null
wait_for_service "$INST_A" STARTED 10
wait_for_service "$INST_B" STARTED 10

assert_eq "$(svc_state "$INST_A")" "STARTED" "$INST_A reached STARTED"
assert_eq "$(svc_state "$INST_B")" "STARTED" "$INST_B reached STARTED"

# Give the child a moment to write.
sleep 1
assert_eq "$(cat $RESULT_DIR/alpha-result 2>/dev/null)" "instance=alpha" \
    "instance argument 'alpha' substituted into the child command"
assert_eq "$(cat $RESULT_DIR/beta-result 2>/dev/null)" "instance=beta" \
    "instance argument 'beta' substituted into the child command"

# Stopping one instance must not affect the other.
slinitctl --system stop "$INST_A" 2>/dev/null
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$(svc_state "$INST_A")" in
    STOPPED)
        echo "OK: $INST_A stopped independently"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $INST_A state after stop: $(svc_state "$INST_A")"
        ;;
esac
assert_eq "$(svc_state "$INST_B")" "STARTED" "$INST_B unaffected by $INST_A stop"

test_summary
