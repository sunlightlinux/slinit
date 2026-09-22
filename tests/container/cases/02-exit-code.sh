# A container's exit code is its main program's. When the boot service
# is the workload and it exits on its own, slinit must exit with that
# code (main.go containerExitCode), so `docker run` reports the job's
# result instead of slinit's.
#
# Run twice: once after a second, and once exiting at once. The instant
# exit is the case that hung — the workload died before the event loop
# started, the inactive notification was dropped, and slinit sat as
# PID 1 in an otherwise empty container forever.

N=slinit-ct-exit-$$
SVC=$(new_svcdir)
RES=$(new_svcdir)
chmod 777 "$RES"
trap 'ct_rm $N; rm -rf "$SVC" "$RES"' EXIT

for cmd in "sleep 1; exit 7" "exit 7"; do
    cat > "$SVC/boot" <<EOF
type = process
command = /bin/sh -c "$cmd"
restart = false
EOF
    rm -f "$RES/exitcode"
    ct_start $N "$SVC" -v "$RES:/run/slinit/container-results"
    ct_wait_exit $N 15
    check $? "[$cmd] container exited by itself once the workload finished"

    ec=$(ct_exit_code $N)
    [ "$ec" = "7" ]; check $? "[$cmd] exit code is the workload's 7 (got $ec)"
    [ "$(cat "$RES/exitcode" 2>/dev/null)" = "7" ]; check $? "[$cmd] container-results/exitcode is 7"
    ct_rm $N
done

summary
