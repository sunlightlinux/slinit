# A container whose cgroup slinit cannot see, under a small pids limit.
#
# slinit fits its startup thread reserve to the pids cgroup's free tasks
# (case 18). Some containers do not show their cgroup: no cgroup mount,
# a masked /sys, `--cgroups=disabled`. The limit still applies, but
# slinit cannot read it, and a reserve sized by the host's CPUs alone
# (GOMAXPROCS+16) then dies at start exactly as in case 18: "runtime:
# failed to create new OS thread", exit 2 (measured with 0f1c71d, before
# its follow-up 58d2b19). Outside PID 1 of a machine, an unreadable
# limit now gets a small fixed reserve and GOMAXPROCS cut to match.
#
# The cgroup is hidden by mounting an empty tmpfs over /sys/fs/cgroup.

N=slinit-ct-cghidden-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF

ct_start $N "$SVC" --pids-limit 25 --tmpfs /sys/fs/cgroup
ct_wait_ready $N 10
check $? "boot reached STARTED with the cgroup hidden and --pids-limit 25"

"$RUNTIME" exec $N test -e /sys/fs/cgroup/pids.max
[ $? -ne 0 ]
check $? "the container's pids limit is not visible to slinit (precondition)"

thr=$("$RUNTIME" exec $N awk '/^Threads:/ {print $2}' /proc/1/status 2>/dev/null)
[ -n "$thr" ] && [ "$thr" -le 20 ]
check $? "slinit holds few threads with no limit to read (${thr:-?})"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ]
check $? "and stops cleanly (exit $ec, ${ms}ms)"

summary
