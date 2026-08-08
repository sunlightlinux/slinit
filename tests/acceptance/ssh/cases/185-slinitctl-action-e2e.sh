#!/bin/sh
# 185-slinitctl-action-e2e — `extra-command NAME=CMD` on a service
# defines a custom action invocable via `slinitctl action <svc>
# <name>`. `list-actions` enumerates them. Verify end-to-end: the
# custom action's body fires when invoked.

SVC="acceptance-test-action"
MARK=/tmp/acceptance-action.mark

cleanup() { svc_remove "$SVC"; rm -f "$MARK"; }
trap cleanup EXIT INT TERM
rm -f "$MARK"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
# extra-command syntax is "<name> <command> [args...]" (space-separated,
# not "name=command") per pkg/config/parser.go. First word is the
# action name; the rest is the command to run when the action fires.
extra-command = dump /bin/sh -c "touch $MARK"
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10 || { test_summary; return; }

# list-actions surfaces the custom action name.
_actions=$(slinitctl --system list-actions "$SVC" 2>&1)
assert_contains "$_actions" "dump" "list-actions shows the 'dump' extra-command"

# action fires the custom command.
slinitctl --system action "$SVC" dump >/dev/null 2>&1
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK" ]; then
    echo "OK: action 'dump' ran (marker $MARK created)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: action 'dump' did not create marker"
fi

test_summary
