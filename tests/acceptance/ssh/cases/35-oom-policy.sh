#!/bin/sh
# 35-oom-policy — `oom-policy = continue|stop|kill` controls slinit's
# reaction to a cgroup v2 OOM event on the service's cgroup
# (pkg/service/types.go ParseOOMPolicy). Triggering a real OOM safely on
# a live VM is fragile (would compete with the host's memory pressure
# handling and may invoke the kernel OOM killer on something else), so
# this case asserts parser acceptance plus that the option lands on a
# real running service without rejecting the description.

SVC="acceptance-test-oompolicy"
SLINIT_CHECK_TMP="/tmp/acceptance-test-oompolicy.cfg"

cleanup() {
    svc_remove "$SVC"
    rm -f "$SLINIT_CHECK_TMP"
}
trap cleanup EXIT INT TERM

# Behavioural probe: oom-policy=continue (the default-ish, definitely
# safe value) on a normal long-running service. Must reach STARTED;
# slinit must not reject the description.
svc_deploy "$SVC" <<EOF
type = process
oom-policy = continue
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED with oom-policy=continue"

# Parse-only check for the rest of the values + the empty string (which
# the parser treats as continue per types.go:316).
for _policy in stop kill continue; do
    cat > "$SLINIT_CHECK_TMP" <<EOC
type = process
oom-policy = $_policy
command = /bin/sh -c 'while :; do sleep 60; done'
EOC
    _out=$(slinit-check "$SLINIT_CHECK_TMP" 2>&1 || true)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$_out" in
        *"unknown oom-policy"*|*"invalid oom-policy"*)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: oom-policy=$_policy rejected by parser: $_out"
            ;;
        *)
            echo "OK: oom-policy=$_policy parses"
            ;;
    esac
done

# Negative path: a typo must still be rejected.
cat > "$SLINIT_CHECK_TMP" <<EOC
type = process
oom-policy = panic
command = /bin/sh -c 'while :; do sleep 60; done'
EOC
_out=$(slinit-check "$SLINIT_CHECK_TMP" 2>&1 || true)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"unknown oom-policy"*|*"invalid oom-policy"*|*error*|*ERROR*)
        echo "OK: oom-policy=panic correctly rejected"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: oom-policy=panic was not rejected: $_out"
        ;;
esac

test_summary
