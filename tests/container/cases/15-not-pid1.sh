# `docker run --init` puts the runtime's own tiny init at PID 1 and
# slinit at PID 2. Container mode then runs with isPID1 false, a
# different path through main: no subreaper duties of its own, and the
# signals arrive from docker-init rather than the runtime.

N=slinit-ct-pid2-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = yes
EOF

ct_start $N "$SVC" --init
ct_wait_ready $N 10
check $? "boot service reached STARTED under --init"

[ "$("$RUNTIME" exec $N cat /proc/1/comm)" != "slinit" ]
check $? "PID 1 is the runtime's init, not slinit"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 3000 ]
check $? "docker stop still exits 0 promptly (exit $ec, ${ms}ms)"

summary
