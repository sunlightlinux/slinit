# Hardened deployments run containers with a read-only root filesystem
# and tmpfs only where writes are expected. slinit must boot and shut
# down cleanly with nothing writable but /run and /tmp.

N=slinit-ct-ro-$$
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

ct_start $N "$SVC" --read-only --tmpfs /run --tmpfs /tmp
ct_wait_ready $N 10
check $? "boot service reached STARTED on a read-only rootfs"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 3000 ]; check $? "clean stop (exit $ec, ${ms}ms)"

ct_logs $N | grep -iE "read-only file system|EROFS" | grep -viE "^.*WARN" >/dev/null
[ $? -ne 0 ]; check $? "no errors (as opposed to warnings) about the read-only rootfs"

summary
