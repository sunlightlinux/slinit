# The other half of case 12: when nothing asks for a restart, the
# container ends rather than idling with an empty service set. A
# crash-looping service that exhausts its restart limit is the same
# shape, and both have to say so in the log — this is what `docker logs`
# will hold when someone asks why the container stopped.

N=slinit-ct-lost-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: flapper
EOF
cat > "$SVC/flapper" <<'EOF'
type = process
command = /bin/sh -c "exit 3"
restart = yes
restart-limit-count = 3
restart-limit-interval = 10
EOF

ct_start $N "$SVC"
ct_wait_exit $N 15
check $? "container exited once the boot target could not be kept up"

ec=$(ct_exit_code $N)
[ "$ec" = "1" ]; check $? "exit code is 1 (got $ec)"

logs=$(ct_logs $N)
echo "$logs" | grep -q "process exited with code 3"
check $? "log records the service's own failure"
echo "$logs" | grep -qi "boot failure"
check $? "log says the container is ending because of a boot failure"

summary
