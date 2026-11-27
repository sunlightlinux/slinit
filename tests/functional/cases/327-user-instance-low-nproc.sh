#!/bin/sh
# Test: a user instance under a small RLIMIT_NPROC starts and keeps
# working.
#
# slinit makes a reserve of idle OS threads at start (cf61810), so the Go
# runtime never needs a pthread_create once tasks run out. Sized by the
# CPUs alone (GOMAXPROCS+16), the reserve was itself the failure under a
# tight task limit: a container with --pids-limit 25 died at start
# (container case 18), and so does a user instance whose RLIMIT_NPROC,
# counted over all the user's tasks, is below that. 0f1c71d caps the
# reserve at an eighth of RLIMIT_NPROC for a non-root slinit (and a
# quarter of the pids cgroup's free tasks). v4.0.3, with no reserve,
# passes this; the branch before 0f1c71d does not.
#
# The user instance is run as a service of PID 1 with run-as = nobody
# and `ulimit -u 20`, so nobody's whole task count is this instance and
# its children.

id nobody >/dev/null 2>&1 || skip_case "no user nobody in the image"
R=/tmp/327
rm -rf "$R"; mkdir -p "$R/svcs"
printf '%s\n' 'type = internal' 'waits-for: w' > "$R/svcs/boot"
printf '%s\n' 'type = process' 'command = /bin/sleep 3270' > "$R/svcs/w"
chown -R nobody "$R"

# slinit has no rlimit-nproc directive; the shell sets it, then execs.
printf '%s\n' 'type = process' 'run-as = nobody' \
    "command = /bin/sh -c \"ulimit -u 20 && exec /sbin/slinit --user --socket-path $R/sock --services-dir $R/svcs --log-file $R/log\"" \
    'restart = false' > /etc/slinit.d/test-327
_o=$(slinitctl --system start test-327 2>&1) || echo "start: $_o"

sl() { timeout 5 slinitctl --socket-path "$R/sock" "$@"; }
_e=0
until [ "$(sl status w 2>/dev/null | awk '/State:/ {print $2}')" = STARTED ] ||
      [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
sleep 1
_st=$(svc_state test-327)
_pid=$(timeout 5 slinitctl --system status test-327 2>/dev/null | awk '/PID:/ {print $2}')
_thr=$(awk '/^Threads:/ {print $2}' "/proc/$_pid/status" 2>/dev/null)
_lim=$(awk '/^Max processes/ {print $3}' "/proc/$_pid/limits" 2>/dev/null)
echo "user instance: state $_st, pid '$_pid', threads '$_thr', RLIMIT_NPROC '$_lim'"
[ "$_lim" = 20 ] || [ "$_st" != STARTED ] || skip_case "ulimit -u 20 was not applied (limit '$_lim')"

assert_eq "$_st" "STARTED" "the user instance is still running"
assert_eq "$(sl status w 2>/dev/null | awk '/State:/ {print $2}')" "STARTED" \
    "and has started its service"
_out=$(sl list 2>&1); _rc=$?
assert_eq "$_rc" "0" "and answers its control socket"
[ "$_st" = STARTED ] || grep -m2 -E 'failed to create new OS thread|fatal error' "$R/log" 2>/dev/null

slinitctl --system stop test-327 >/dev/null 2>&1
test_summary
