#!/bin/sh
# Test: settings no functional case had used — failure-conditions,
# start-delay, depends-ms, rlimit-memlock and rlimit-as — each checked by
# what it changes, not by the parser accepting it.

reason() {
    timeout 5 slinitctl --system status5 "$1" 2>/dev/null |
        awk '/Stop-reason:/ {print $2; exit}'
}

failed_rc() { timeout 5 slinitctl --system is-failed "$1" >/dev/null 2>&1; echo $?; }

printf '%s\n' '#!/bin/sh' 'sleep 1' 'exit 3' > /tmp/281-exit3.sh
printf '%s\n' '#!/bin/sh' 'sleep 1' 'exit 0' > /tmp/281-exit0.sh
chmod +x /tmp/281-exit3.sh /tmp/281-exit0.sh

svc() {
    _name=$1; shift
    printf '%s\n' 'type = process' 'restart = false' "$@" > "/etc/slinit.d/$_name"
}

# --- failure-conditions ---------------------------------------------------
# Default: a non-zero exit of a started process is a failure, exit 0 is not.
svc test-fc-default 'command = /tmp/281-exit3.sh'
# none: no end of the process is a failure.
svc test-fc-none 'command = /tmp/281-exit3.sh' 'failure-conditions = none'
# process-stops: even exit 0 is.
svc test-fc-stops 'command = /tmp/281-exit0.sh' 'failure-conditions = process-stops'
svc test-fc-clean 'command = /tmp/281-exit0.sh'
# dependency-fails: a hard dependency failing marks this one too.
svc test-fc-child 'command = /tmp/281-exit3.sh'
svc test-fc-parent 'command = /bin/sleep 2811' 'depends-on = test-fc-child' \
    'failure-conditions = process-fails dependency-fails'
svc test-fc-child2 'command = /tmp/281-exit3.sh'
svc test-fc-parent2 'command = /bin/sleep 2812' 'depends-on = test-fc-child2'

for s in default none stops clean parent parent2; do
    slinitctl --system --no-wait start "test-fc-$s" >/dev/null 2>&1
done
sleep 4

# The verdict is is-failed's (0 = failed): the stop reason of a process
# that ended on its own is "terminated" under every setting, as in dinit.
assert_eq "$(failed_rc test-fc-default)" "0" "default: exit 3 after start is a failure"
assert_eq "$(failed_rc test-fc-clean)" "1" "default: exit 0 after start is not"
assert_eq "$(failed_rc test-fc-none)" "1" "failure-conditions = none: exit 3 is not a failure"
assert_eq "$(failed_rc test-fc-stops)" "0" "failure-conditions = process-stops: exit 0 is a failure"
assert_eq "$(reason test-fc-parent)" "dependency-failed" \
    "dependency-fails: a failing hard dependency marks the dependent"
assert_eq "$(reason test-fc-parent2)" "normal" \
    "without dependency-fails the dependent just stops"
assert_eq "$(failed_rc test-fc-parent)" "0" "dependency-fails: the dependent is-failed"
assert_eq "$(failed_rc test-fc-parent2)" "1" "without dependency-fails the dependent is not failed"

# --- start-delay ----------------------------------------------------------
svc test-sdelay 'command = /bin/sleep 2813' 'start-delay = 3'
_t0=$(date +%s)
slinitctl --system --no-wait start test-sdelay >/dev/null 2>&1
sleep 1
assert_service_state test-sdelay STARTING "start-delay: still STARTING after 1s"
assert_eq "$(ps -o args 2>/dev/null | grep -c '[s]leep 2813')" "0" \
    "start-delay: nothing forked before the delay"
wait_for_service test-sdelay STARTED 10 || true
_dt=$(( $(date +%s) - _t0 ))
assert_service_state test-sdelay STARTED "start-delay: STARTED after the delay"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_dt" -ge 3 ]; then
    echo "OK: start-delay: took ${_dt}s (>= 3)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: start-delay: STARTED after ${_dt}s, expected >= 3"
fi
slinitctl --system stop test-sdelay >/dev/null 2>&1

# --- depends-ms -----------------------------------------------------------
# Milestone: must start for the dependent to start; stopping it later
# does not take the dependent down, unlike depends-on.
svc test-ms-dep 'command = /bin/sleep 2814'
svc test-ms-top 'command = /bin/sleep 2815' 'depends-ms = test-ms-dep'
svc test-on-top 'command = /bin/sleep 2816' 'depends-on = test-ms-dep'
slinitctl --system start test-ms-top >/dev/null 2>&1
slinitctl --system start test-on-top >/dev/null 2>&1
assert_service_state test-ms-dep STARTED "depends-ms: the milestone was started"
assert_service_state test-ms-top STARTED "depends-ms: the dependent started"
slinitctl --system --force stop test-ms-dep >/dev/null 2>&1
wait_for_service test-ms-dep STOPPED 10 || true
sleep 1
assert_service_state test-ms-top STARTED "depends-ms: milestone stopping leaves the dependent up"
assert_service_state test-on-top STOPPED "depends-on: the same stop takes a hard dependent down"
slinitctl --system stop test-ms-top >/dev/null 2>&1

# A milestone that cannot start fails the dependent.
svc test-ms-bad 'command = /nonexistent/281'
svc test-ms-top2 'command = /bin/sleep 2817' 'depends-ms = test-ms-bad'
slinitctl --system start test-ms-top2 >/dev/null 2>&1
sleep 1
assert_service_state test-ms-top2 STOPPED "depends-ms: a milestone that fails to start fails the dependent"
assert_eq "$(reason test-ms-top2)" "dependency-failed" "depends-ms: reason is dependency-failed"

# --- rlimit-memlock / rlimit-as -------------------------------------------
svc test-rlim 'command = /bin/sleep 2818' 'rlimit-memlock = 131072' \
    'rlimit-as = 1073741824:2147483648'
slinitctl --system start test-rlim >/dev/null 2>&1
wait_for_service test-rlim STARTED 10 || true
_pid=$(ps -o pid,args 2>/dev/null | awk '$2 == "/bin/sleep" && $3 == "2818" {print $1; exit}')
_lim=$(cat "/proc/$_pid/limits" 2>/dev/null)
_ml=$(echo "$_lim" | awk '/^Max locked memory/ {print $4, $5}')
_as=$(echo "$_lim" | awk '/^Max address space/ {print $4, $5}')
assert_eq "$_ml" "131072 131072" "rlimit-memlock: soft and hard set"
assert_eq "$_as" "1073741824 2147483648" "rlimit-as: soft:hard set"
slinitctl --system stop test-rlim >/dev/null 2>&1

test_summary
