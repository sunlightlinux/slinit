#!/bin/sh
# 188-shares-console-interplay — `options = shares-console` opts a
# service into cooperative console sharing with other console-hungry
# services. Unlike runs-on-console (exclusive), shares-console lets
# multiple svcs coexist on the console without arbitration conflict.
# The test verifies parse + start + coexistence — two shares-console
# svcs must both reach STARTED without either being blocked.

SVC_A="acceptance-test-shared-console-a"
SVC_B="acceptance-test-shared-console-b"

cleanup() { svc_remove "$SVC_A" "$SVC_B"; }
trap cleanup EXIT INT TERM

for svc in "$SVC_A" "$SVC_B"; do
    svc_deploy "$svc" <<EOF
type = process
options = shares-console
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF
done

slinitctl --system start "$SVC_A" >/dev/null
slinitctl --system start "$SVC_B" >/dev/null
wait_for_service "$SVC_A" "STARTED" 15
wait_for_service "$SVC_B" "STARTED" 15
assert_service_state "$SVC_A" "STARTED" "shares-console svc A STARTED"
assert_service_state "$SVC_B" "STARTED" "shares-console svc B STARTED (coexists with A)"

test_summary
