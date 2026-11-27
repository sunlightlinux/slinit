#!/bin/sh
# Test: --cpu-affinity pins every thread of slinit, and so the commands it
# forks from any of them.
#
# Up to v4.0.3 --cpu-affinity called sched_setaffinity(0, ...), which
# sets only the calling thread: the Go runtime's other threads stayed on
# every CPU, and a command forked from one of them without an affinity of
# its own (a hook, a health check, a cron job) ran anywhere (835f7ee;
# measured 34 of 35 threads unpinned). 25 covers a service's own
# cpu-affinity, not the daemon option. Checked on a nested
# `slinit --user --cpu-affinity 0` in a two-CPU VM.

[ "$(grep -c '^processor' /proc/cpuinfo)" -ge 2 ] || skip_case "needs two CPUs"

R=/tmp/322
S="$R/svcs"
rm -rf "$R"; mkdir -p "$S"
printf '%s\n' 'type = internal' > "$S/boot"
# The hook records the CPUs it may run on; grep runs as the hook's child.
printf '%s\n' 'type = process' 'command = /bin/sleep 3220' 'restart = false' \
    "pre-start-command = /bin/sh -c \"grep Cpus_allowed_list /proc/self/status >> $R/hook\"" \
    > "$S/svc"

setsid /sbin/slinit --user --cpu-affinity 0 --socket-path "$R/sock" \
    --services-dir "$S" --log-file "$R/log" </dev/null >/dev/null 2>&1 &
NEST=$!
sl() { timeout 10 slinitctl --socket-path "$R/sock" "$@"; }
_e=0
until [ "$(sl status boot 2>/dev/null | awk '/State:/ {print $2}')" = STARTED ] ||
      [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
[ "$_e" -lt 20 ] || skip_case "nested slinit did not come up"

# Give the hook many chances to be forked from different threads.
i=0
while [ "$i" -lt 20 ]; do
    sl start svc >/dev/null 2>&1
    sl stop svc >/dev/null 2>&1
    i=$((i + 1))
done

_total=0; _off=0
for t in /proc/$NEST/task/*; do
    _total=$((_total + 1))
    _c=$(awk '/^Cpus_allowed_list:/ {print $2}' "$t/status" 2>/dev/null)
    [ "$_c" = 0 ] || _off=$((_off + 1))
done
_hooks=$(wc -l < "$R/hook" 2>/dev/null || echo 0)
_hoff=$(grep -vc 'Cpus_allowed_list:[[:space:]]*0$' "$R/hook" 2>/dev/null)
echo "threads: $_off of $_total not on CPU 0; hooks: ${_hoff:-0} of $_hooks not on CPU 0"
kill -9 "$NEST" 2>/dev/null

[ "$_hooks" -ge 10 ] || skip_case "the hook ran only $_hooks times"
assert_eq "$_off" "0" "every thread of slinit is on CPU 0"
assert_eq "${_hoff:-0}" "0" "every hook it forked runs on CPU 0"

test_summary
