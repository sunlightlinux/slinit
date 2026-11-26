#!/bin/sh
# Test: the dinit-v5 wire queries — status5, list5 — and query-name /
# query-load-mech, none of which had a functional case.
#
# status5 renders three layouts off one status struct: a running service
# (PID), a process that exited (si_code / si_status), and an exec that
# never happened (Exec-stage / Exec-errno). The last one used to be told
# apart from the second by a stage number that is 0 for the first stage,
# so a failed exec printed its errno as an si_code. Each layout is checked
# against what the service really did.

printf '%s\n' 'type = process' 'command = /bin/sleep 2801' \
    'restart = false' > /etc/slinit.d/test-v5-run
printf '%s\n' '#!/bin/sh' 'exit 7' > /tmp/280-exit7.sh
chmod +x /tmp/280-exit7.sh
printf '%s\n' 'type = scripted' 'command = /tmp/280-exit7.sh' \
    > /etc/slinit.d/test-v5-exit
printf '%s\n' 'type = process' 'command = /nonexistent/280-binary' \
    'restart = false' > /etc/slinit.d/test-v5-noexec
printf '%s\n' 'type = internal' > /etc/slinit.d/test-v5-tpl

slinitctl --system start test-v5-run >/dev/null 2>&1
wait_for_service test-v5-run STARTED 10 || true
slinitctl --system start test-v5-exit >/dev/null 2>&1
slinitctl --system start test-v5-noexec >/dev/null 2>&1
sleep 1

# Running: the PID status5 reports is the sleep's.
_s=$(timeout 5 slinitctl --system status5 test-v5-run 2>&1)
echo "$_s"
_real=$(ps -o pid,args 2>/dev/null | awk '$2 == "/bin/sleep" && $3 == "2801" {print $1; exit}')
_shown=$(echo "$_s" | awk '/PID:/ {print $2}')
assert_contains "$_s" "State:       STARTED" "status5: running service is STARTED"
assert_contains "$_s" "[has-pid]" "status5: running service has the has-pid flag"
assert_eq "$_shown" "$_real" "status5: PID is the running process"
assert_eq "$(test -n "$_real" && echo yes)" "yes" "the running sleep was found (so the PID check is not empty = empty)"

# Exited: CLD_EXITED with the real code.
_s=$(timeout 5 slinitctl --system status5 test-v5-exit 2>&1)
echo "$_s"
assert_contains "$_s" "Stop-reason: failed" "status5: a script exiting 7 failed"
assert_contains "$_s" "si_code:     1 (CLD_EXITED)" "status5: exit reported as CLD_EXITED"
assert_contains "$_s" "si_status:   7" "status5: with the script's exit code"

# Exec failure: stage and errno, not a fake si_code.
_s=$(timeout 5 slinitctl --system status5 test-v5-noexec 2>&1)
echo "$_s"
assert_contains "$_s" "Stop-reason: exec-failed" "status5: missing binary is exec-failed"
assert_contains "$_s" "Exec-errno:  2 (ENOENT)" "status5: errno is ENOENT"
assert_not_contains "$_s" "si_code" "status5: exec failure is not shown as a wait status"

# -l appends the show fields.
_s=$(timeout 5 slinitctl --system status5 -l test-v5-run 2>&1)
assert_contains "$_s" "Details:" "status5 -l adds the details block"
assert_contains "$_s" "sleep 2801" "status5 -l details carry the command"

# list5: one line per service, with the same facts.
_l=$(timeout 5 slinitctl --system list5 2>&1)
echo "$_l" | grep test-v5
_run=$(echo "$_l" | grep '^test-v5-run ')
assert_contains "$_run" "state=STARTED" "list5: running service STARTED"
assert_contains "$_run" "[has-pid]" "list5: running service has a pid"
assert_contains "$(echo "$_l" | grep '^test-v5-noexec ')" "stop-reason=exec-failed" \
    "list5: missing binary is exec-failed"
assert_contains "$(echo "$_l" | grep '^test-v5-exit ')" "stop-reason=failed" \
    "list5: failing script is failed"

# query-name: the daemon's name for a handle, instance argument included.
slinitctl --system start test-v5-tpl@inst >/dev/null 2>&1
assert_eq "$(timeout 5 slinitctl --system query-name test-v5-tpl@inst 2>&1)" \
    "test-v5-tpl@inst" "query-name returns the full instance name"

_m=$(timeout 5 slinitctl --system query-load-mech 2>&1)
echo "$_m"
assert_contains "$_m" "(directory)" "query-load-mech: directory loader"
assert_contains "$_m" "/etc/slinit.d" "query-load-mech: lists the services dir"

slinitctl --system stop test-v5-run >/dev/null 2>&1
test_summary
