#!/bin/sh
# Test: a restart called off while a dependent holds the service does not
# leave the service counted active, so the shutdown is not held up.
#
# A dependent that starts while a service is stopping takes an
# acquisition on it and so wants it back; with restart-delay that restart
# is parked on the delay timer and the service sits STOPPED, still
# counted active. Stopping the service there called the restart off, but
# the count only drops when nothing requires the service, and the
# dependent still did; when the dependent let go later nothing took the
# count down, and the shutdown waited out its emergency timeout with
# every service STOPPED (3766b90, found by the control fuzzer). Both a
# waits-for and a depends-on dependent, each against its own nested
# `slinit --user` so the shutdown can be timed.
#
# What this can catch, measured: v4.0.3 passes (its stop in a restart
# delay did nothing, so the restart was never called off; 7cbe970 made
# the hole reachable). HEAD has two guards for it, 3766b90's settling in
# abandonPendingRestart and 5f02f2b's per-service active count; with
# 3766b90's removed alone the case (and 3766b90's own unit test) still
# passes, with both removed both variants fail with the shutdown still
# running after 15 s.

_variant() { # tag dep-keyword
    _t="$1"; _kw="$2"
    R="/tmp/317-$_t"
    S="$R/svcs"
    rm -rf "$R"; mkdir -p "$S"
    printf '%s\n' 'type = internal' > "$S/boot"
    # Takes a second to stop, so the dependent's start lands in STOPPING.
    cat > "$R/svc.sh" <<'EOF'
#!/bin/sh
trap 'sleep 1; exit 0' TERM
sleep 1000 & wait $!
sleep 1000 & wait $!
EOF
    chmod +x "$R/svc.sh"
    printf '%s\n' 'type = process' "command = $R/svc.sh" 'restart = false' \
        'restart-delay = 20' > "$S/svc"
    printf '%s\n' 'type = internal' "$_kw: svc" > "$S/dept"

    setsid /sbin/slinit --user --socket-path "$R/sock" --services-dir "$S" \
        --log-file "$R/log" </dev/null >/dev/null 2>&1 &
    _nest=$!
    sl() { timeout 10 slinitctl --socket-path "$R/sock" "$@"; }
    st() { sl status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }
    _e=0
    until [ "$(st boot)" = STARTED ] || [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
    [ "$(st boot)" = STARTED ] || skip_case "$_t: nested slinit did not come up"

    sl start svc >/dev/null 2>&1
    [ "$(st svc)" = STARTED ] || skip_case "$_t: svc did not start"
    sl --no-wait stop svc >/dev/null 2>&1
    sl --no-wait start dept >/dev/null 2>&1
    _s=$(st svc)
    [ "$_s" = STOPPING ] || skip_case "$_t: svc was '$_s' when dept started, not STOPPING"
    # svc finishes stopping and parks its restart on the 20 s delay.
    _e=0
    until [ "$(st svc)" = STOPPED ] || [ "$_e" -ge 20 ]; do sleep 0.2; _e=$((_e + 1)); done
    echo "$_t: svc $(st svc), dept $(st dept) in the delay"

    sl stop svc >/dev/null 2>&1       # calls the restart off
    sl stop dept >/dev/null 2>&1
    sleep 1
    echo "$_t: after the stops: svc $(st svc), dept $(st dept)"
    assert_eq "$(st svc)" "STOPPED" "$_t: svc STOPPED"
    assert_eq "$(st dept)" "STOPPED" "$_t: dept STOPPED"

    _t0=$(date +%s)
    sl poweroff >/dev/null 2>&1
    _e=0
    while [ "$_e" -lt 30 ] && kill -0 "$_nest" 2>/dev/null; do sleep 0.5; _e=$((_e + 1)); done
    _alive=0; kill -0 "$_nest" 2>/dev/null && _alive=1
    echo "$_t: shutdown $(( $(date +%s) - _t0 ))s, still running: $_alive"
    grep -i 'waiting for' "$R/log" 2>/dev/null | tail -2
    kill -9 "$_nest" 2>/dev/null
    assert_eq "$_alive" "0" "$_t: the shutdown finishes within 15 s"
}

_variant soft waits-for
_variant hard depends-on

test_summary
