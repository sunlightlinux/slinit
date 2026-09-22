# A broken service tree must fail the container fast. On bare metal a
# load failure parks slinit at an interactive recovery menu with a 60s
# auto-reboot; in a container there is nobody at the keyboard, so it has
# to exit non-zero at once instead of hanging until an orchestrator
# gives up on it.

N=slinit-ct-loadfail-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: this-service-does-not-exist
EOF

ct_start $N "$SVC"
ct_wait_exit $N 10
check $? "container exited within 10s instead of waiting at a recovery prompt"

ec=$(ct_exit_code $N)
[ "$ec" = "1" ]; check $? "exit code is 1 (got $ec)"

ct_logs $N | grep -q "this-service-does-not-exist"
check $? "the log names the missing service"

summary
