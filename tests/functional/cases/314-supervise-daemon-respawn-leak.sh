#!/bin/sh
# Test: slinit-supervise-daemon does not leak descriptors or loggers
# across respawns.
#
# Up to v4.0.3 the supervisor kept its copies of the daemon's stdin,
# stdout and stderr until a garbage collection ran, and it allocates
# almost nothing between respawns: a crash-looping daemon grew the
# supervisor's descriptor table by three per respawn, and with a
# --stdout-logger every old logger never saw EOF and kept running
# (5f14821; measured 175 descriptors and 36 loggers after 4 s).

D=/tmp/314
rm -rf "$D"; mkdir -p "$D"
cat > "$D/daemon.sh" <<'EOF'
#!/bin/sh
echo run >> /tmp/314/runs
echo out-314
exit 1
EOF
# A logger nobody else runs, so its processes can be counted by name
# (no exec: the shell keeps the script's name while cat reads).
cat > "$D/logger314.sh" <<'EOF'
#!/bin/sh
cat > /dev/null
EOF
chmod +x "$D/daemon.sh" "$D/logger314.sh"

slinit-supervise-daemon leak314 --start --pidfile "$D/sup.pid" \
    --exec "$D/daemon.sh" --stdout-logger "$D/logger314.sh" \
    --respawn-max 0 --respawn-delay 100ms >/dev/null 2>"$D/err"
SUP=$(cat "$D/sup.pid" 2>/dev/null)
[ -n "$SUP" ] && [ -d "/proc/$SUP" ] || skip_case "supervisor did not start: $(cat "$D/err")"

sleep 1
_fd0=$(ls "/proc/$SUP/fd" 2>/dev/null | wc -l)
_r0=$(wc -l < "$D/runs" 2>/dev/null || echo 0)
sleep 8
_fd1=$(ls "/proc/$SUP/fd" 2>/dev/null | wc -l)
_r1=$(wc -l < "$D/runs" 2>/dev/null || echo 0)
_loggers=$(pgrep -f logger314.sh | wc -l)
echo "respawns $_r0 -> $_r1, supervisor fds $_fd0 -> $_fd1, loggers running $_loggers"

slinit-supervise-daemon leak314 --stop --pidfile "$D/sup.pid" >/dev/null 2>&1

# The supervisor's own rate limit gives about one respawn a second.
[ $((_r1 - _r0)) -ge 4 ] || skip_case "only $((_r1 - _r0)) respawns in 8 s: too few to judge"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_fd1" -le $((_fd0 + 4)) ]; then
    echo "OK: supervisor descriptors flat over $((_r1 - _r0)) respawns ($_fd0 -> $_fd1)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: supervisor grew $((_fd1 - _fd0)) descriptors over $((_r1 - _r0)) respawns"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_loggers" -le 2 ]; then
    echo "OK: old loggers exit ($_loggers running)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_loggers loggers still running: old ones never saw EOF"
fi

test_summary
