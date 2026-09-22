# `--pids-limit` is a common container hardening knob, and a service
# that forks past it makes every later fork fail — including forks
# slinit itself needs. PID 1 must survive that and still shut the
# container down cleanly.
#
# Note: `docker exec slinitctl ...` may itself fail here, because the Go
# runtime needs to create OS threads and the limit is already reached.
# That is the runtime's constraint, not slinit's, so this case asserts
# on the container's state rather than on slinitctl's output.

N=slinit-ct-pids-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: worker
waits-for: forker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = yes
EOF
cat > "$SVC/forker" <<'EOF'
type = process
command = /bin/sh -c "i=0; while [ $$i -lt 60 ]; do (sleep 30 &); i=$$((i+1)); done; while :; do sleep 1; done"
restart = no
EOF

ct_start $N "$SVC" --pids-limit 25
sleep 6

[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
check $? "slinit survived running out of PIDs"

ms=$(ct_stop_ms $N 15)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 8000 ]
check $? "still stops cleanly with the limit reached (exit $ec, ${ms}ms)"

summary
