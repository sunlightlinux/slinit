# The baseline contract: slinit comes up as PID 1, starts the boot
# service's dependencies, and a plain `docker stop` (SIGTERM) brings it
# down cleanly — exit 0, well inside the runtime's 10s grace period, and
# a results directory saying it halted.

N=slinit-ct-boot-$$
SVC=$(new_svcdir)
RES=$(new_svcdir)
chmod 777 "$RES"
trap 'ct_rm $N; rm -rf "$SVC" "$RES"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF

ct_start $N "$SVC" -v "$RES:/run/slinit/container-results"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

pid1=$("$RUNTIME" exec $N cat /proc/1/cmdline | tr '\0' ' ')
case "$pid1" in
    "/sbin/slinit -o "*) pass "slinit is PID 1 in the container" ;;
    *) fail "PID 1 is '$pid1', not slinit" ;;
esac

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ]; check $? "docker stop exits 0 (got $ec)"
[ "$ms" -lt 3000 ]; check $? "docker stop finished in ${ms}ms (< 3000ms, runtime never had to SIGKILL)"

[ "$(cat "$RES/exitcode" 2>/dev/null)" = "0" ]; check $? "container-results/exitcode is 0"
[ "$(cat "$RES/haltcode" 2>/dev/null)" = "h" ]; check $? "container-results/haltcode is h (SIGTERM = halt)"

ct_logs $N | grep -q "\[STOPPD\] worker"
check $? "worker was stopped by slinit, not killed with the container"

# Nothing in a container's log should be addressed to a terminal: no
# colour, no cursor control. And a missing /etc/machine-id is the normal
# case for an image, not something to warn about on every start — it
# used to be the first line of every `docker logs`.
logs=$(ct_logs $N)
! printf '%s' "$logs" | grep -q "$(printf '\033')"
check $? "no ANSI escapes anywhere in the log (colour or clear-line)"
! echo "$logs" | grep -q "machine-id"
check $? "no machine-id warning"

summary
