# A container's exit code is its main program's. When the boot service
# is the workload and it exits on its own, slinit must exit with that
# code (main.go containerBootOutcome), so `docker run` reports the job's
# result instead of slinit's.
#
# Four rounds. Exit 7 after a second, and exit 7 at once: the instant
# exit is the case that hung, because the workload died before the event
# loop started and the inactive notification was dropped. Then the same
# two with exit 0, which slinit used to turn into exit 1 and call a boot
# failure — a batch job that succeeded was reported as failed.

N=slinit-ct-exit-$$
SVC=$(new_svcdir)
RES=$(new_svcdir)
chmod 777 "$RES"
trap 'ct_rm $N; rm -rf "$SVC" "$RES"' EXIT

for pair in "sleep 1; exit 7:::7" "exit 7:::7" "sleep 1; exit 0:::0" "exit 0:::0"; do
    cmd=${pair%%:::*}
    want=${pair##*:::}
    cat > "$SVC/boot" <<EOF
type = process
command = /bin/sh -c "$cmd"
restart = no
EOF
    rm -f "$RES/exitcode"
    ct_start $N "$SVC" -v "$RES:/run/slinit/container-results"
    ct_wait_exit $N 15
    check $? "[$cmd] container exited by itself once the workload finished"

    ec=$(ct_exit_code $N)
    [ "$ec" = "$want" ]; check $? "[$cmd] exit code is the workload's $want (got $ec)"
    [ "$(cat "$RES/exitcode" 2>/dev/null)" = "$want" ]; check $? "[$cmd] container-results/exitcode is $want"
    ct_rm $N
done

summary
