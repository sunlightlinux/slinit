# Containers run under a memory cap. A service that hits it must fail
# by itself: slinit keeps running, the rest of the services keep
# running, and the container does not go down with it.
#
# The greedy service fills /dev/shm, which counts against the
# container's memory cgroup, so it fails within a second or two without
# needing the kernel OOM killer to pick a victim.

# Probed before anything is built: without a working cap the container
# never starts, so every assertion below would fail describing slinit
# rather than the runtime. Rootless Podman on a host that delegates only
# `pids` to the user slice is the case this was written for.
runtime_can_limit_memory || skip_case \
    "$RUNTIME cannot apply --memory here (rootless with no memory controller delegated?) — the cap, not slinit, is what is missing"

N=slinit-ct-mem-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: worker
waits-for: hog
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = yes
EOF
cat > "$SVC/hog" <<'EOF'
type = process
command = /bin/sh -c "dd if=/dev/zero of=/dev/shm/fill bs=1M count=512"
restart = no
EOF

ct_start $N "$SVC" --memory 64m
ct_wait_ready $N 15
check $? "boot service reached STARTED under a 64m cap"

# Give the greedy service time to hit the cap and die.
i=0
while [ $i -lt 40 ]; do
    "$RUNTIME" exec $N slinitctl status hog 2>/dev/null | grep -q "STOPPED" && break
    sleep 0.25
    i=$((i + 1))
done

"$RUNTIME" exec $N slinitctl status hog 2>/dev/null | grep -q "STOPPED"
check $? "the greedy service died on its own"

[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
check $? "the container is still running"
[ "$("$RUNTIME" inspect -f '{{.State.OOMKilled}}' $N)" = "false" ]
check $? "the runtime did not have to OOM-kill the container"
"$RUNTIME" exec $N slinitctl status worker | grep -q "STARTED"
check $? "the unrelated service is untouched"

ec=$(ct_exit_code $N 2>/dev/null); ms=$(ct_stop_ms $N 10); ec=$(ct_exit_code $N)
[ "$ec" = "0" ]; check $? "clean stop afterwards (exit $ec, ${ms}ms)"

summary
