# A service that ignores SIGTERM must not make `docker stop` fall back
# to SIGKILLing the whole container. slinit owns the escalation: it
# waits the service's stop-timeout, SIGKILLs the service itself, and
# exits cleanly — long before the runtime's own grace period ends.

N=slinit-ct-stopto-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: stubborn
EOF
cat > "$SVC/stubborn" <<'EOF'
type = process
command = /bin/sh -c "trap '' TERM; while :; do sleep 1; done"
stop-timeout = 2
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

ms=$(ct_stop_ms $N 20)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ]; check $? "exit 0, not 137 — slinit killed the service, the runtime did not kill slinit (got $ec)"
[ "$ms" -ge 1800 ] && [ "$ms" -lt 8000 ]
check $? "stop took ${ms}ms: waited out the 2s stop-timeout, then finished"

summary
