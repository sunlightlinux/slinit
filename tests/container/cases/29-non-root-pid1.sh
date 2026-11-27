# slinit as PID 1 of a container that runs as a non-root user
# (`docker run --user 1000:1000`, Kubernetes runAsNonRoot).
#
# Nothing under /run is writable then, so the control socket goes where
# -p says. The workload must still run as that user, answer slinitctl,
# survive a restart, end with exit 0 on `docker stop`, and end as a
# failure when it crashes.

SVC=$(new_svcdir)
names=""
trap 'for n in $names; do ct_rm $n; done; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOS'
type = internal
depends-on: app
EOS
cat > "$SVC/app" <<'EOS'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOS

ready() {
    _i=0
    while [ "$_i" -lt 50 ]; do
        "$RUNTIME" exec "$1" slinitctl -p /tmp/ctl.sock list 2>/dev/null | grep -q '\[\[+\] *\] boot' && return 0
        sleep 0.2
        _i=$((_i + 1))
    done
    return 1
}

N=slinit-ct-nonroot-$$; names="$names $N"
ct_start $N "$SVC" --user 1000:1000 -- -p /tmp/ctl.sock
ready $N
check $? "boot reached STARTED, seen over the -p control socket"

u=$("$RUNTIME" exec $N ps -o user,args | awk '/while/ && !/awk/ {print $1; exit}')
[ "$u" = "1000" ]
check $? "the workload runs as uid 1000 (got '$u')"

"$RUNTIME" exec $N slinitctl -p /tmp/ctl.sock restart app >/dev/null 2>&1
sleep 0.5
[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ] &&
    "$RUNTIME" exec $N slinitctl -p /tmp/ctl.sock status app 2>/dev/null | grep -q 'State: *STARTED'
check $? "a restart of app keeps the container and brings app back"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 5000 ]
check $? "docker stop ends it with exit 0 in ${ms}ms (exit $ec)"

N=slinit-ct-nonroot-crash-$$; names="$names $N"
ct_start $N "$SVC" --user 1000:1000 -- -p /tmp/ctl.sock
ready $N
pid=$("$RUNTIME" exec $N slinitctl -p /tmp/ctl.sock status app 2>/dev/null | awk '/PID:/ {print $2}')
"$RUNTIME" exec $N kill -9 "$pid" >/dev/null 2>&1
ct_wait_exit $N 10
ec=$(ct_exit_code $N)
[ -n "$pid" ] && [ "$ec" != "0" ]
check $? "a crash of the workload ends the container as a failure (pid '$pid', exit $ec)"

summary
