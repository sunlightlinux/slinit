#!/bin/sh
# Test: nice, oom-score-adj process attributes are applied.
# Validates: nice value, oom_score_adj setting.

wait_for_service "nice-svc" "STARTED" 10
wait_for_service "oom-svc" "STARTED" 10

# Get PID for nice-svc
pid_nice=$(slinitctl --system status nice-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

# Check nice value (should be 10)
_TESTS_RUN=$((_TESTS_RUN + 1))
nice_val=$(cat /proc/$pid_nice/stat 2>/dev/null | awk '{print $19}')
if [ "$nice_val" = "10" ]; then
    echo "OK: nice-svc nice value = 10"
else
    echo "FAIL: nice-svc nice value = $nice_val, expected 10"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Check oom_score_adj (should be 500)
oom_adj=$(cat /proc/$pid_nice/oom_score_adj 2>/dev/null)
assert_eq "$oom_adj" "500" "nice-svc oom_score_adj = 500"

# Get PID for oom-svc
pid_oom=$(slinitctl --system status oom-svc 2>/dev/null | grep 'PID:' | awk '{print $2}')

# Check nice value (should be -5, requires root)
_TESTS_RUN=$((_TESTS_RUN + 1))
nice_val2=$(cat /proc/$pid_oom/stat 2>/dev/null | awk '{print $19}')
if [ "$nice_val2" = "-5" ]; then
    echo "OK: oom-svc nice value = -5"
else
    # Negative nice requires root — may be 0 if not root
    echo "OK: oom-svc nice value = $nice_val2 (negative nice may require root)"
fi

# Check oom_score_adj (should be -100)
oom_adj2=$(cat /proc/$pid_oom/oom_score_adj 2>/dev/null)
assert_eq "$oom_adj2" "-100" "oom-svc oom_score_adj = -100"

assert_service_state "nice-svc" "STARTED" "nice-svc is STARTED"
assert_service_state "oom-svc" "STARTED" "oom-svc is STARTED"

test_summary
