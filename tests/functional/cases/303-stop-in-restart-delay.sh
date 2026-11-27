#!/bin/sh
# Test: `slinitctl stop` during a restart delay stops the service for good.
#
# Between a crashed process's exit and its restart the service is STOPPED
# but still wanted up. Up to v4.0.3 a stop there, forced or not, answered
# "already stopped" and changed nothing: the service came back when the
# delay ran out, so a crash-looping service could only be stopped by
# catching it while it ran (fixed in 7cbe970, found by the control fuzzer).
#
# Each run appends a line to a counter file, so "came back" is measured
# as a second run, not inferred from the state at one instant.

_run_variant() {
    _name="$1"     # service name
    _flag="$2"     # extra global flag ("" or --force); it goes BEFORE the verb
    _count="/run/$_name.count"
    rm -f "$_count"
    printf '%s\n' 'type = process' \
        "command = /bin/sh -c \"echo run >> $_count; sleep 1; exit 1\"" \
        'restart = on-failure' 'restart-delay = 4' \
        'restart-limit-count = 100' > "/etc/slinit.d/$_name"

    slinitctl --system --no-wait start "$_name" >/dev/null 2>&1

    # Wait for the first run to end: one line in the counter and the
    # service STOPPED, i.e. inside the 4 s restart delay.
    _deadline=$(( $(date +%s) + 10 ))
    _in_delay=0
    while [ "$(date +%s)" -lt "$_deadline" ]; do
        _lines=$(wc -l < "$_count" 2>/dev/null || echo 0)
        if [ "$_lines" = "1" ] && [ "$(svc_state "$_name")" = "STOPPED" ]; then
            _in_delay=1
            break
        fi
        sleep 0.2
    done
    [ "$_in_delay" = "1" ] || skip_case "$_name never reached its restart delay (lines=$_lines)"

    _out=$(timeout 5 slinitctl --system $_flag stop "$_name" 2>&1)
    echo "stop $_flag: $_out"

    # Past the delay with room to spare: a second run would have started
    # by now and written its line.
    sleep 7
    _lines=$(wc -l < "$_count" 2>/dev/null || echo 0)
    assert_eq "$_lines" "1" "stop ${_flag:-plain} in the restart delay: no run after it"
    assert_service_state "$_name" "STOPPED" "stop ${_flag:-plain} in the restart delay: still STOPPED"
    # Not merely "between runs": nothing may still want it started.
    _tgt=$(timeout 5 slinitctl --system status "$_name" 2>/dev/null | awk '/Target:/ {print $2; exit}')
    assert_eq "$_tgt" "stop" "stop ${_flag:-plain}: target is stop, no longer wanted up"
}

_run_variant test-303-plain ""
_run_variant test-303-force "--force"

test_summary
