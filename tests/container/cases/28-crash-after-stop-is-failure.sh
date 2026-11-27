# A container's exit code says whether its workload failed, also after an
# operator has stopped something unrelated earlier.
#
# `slinitctl stop` marks the stop as requested, so that a stop which
# cascades until nothing is left ends the container with exit 0 (not
# "boot failure"). The mark used to stay for good: stop a side service,
# and a later crash of the workload ended the container with exit 0, a
# Succeeded pod in Kubernetes. A service that stops on its own now takes
# the mark back. Both halves are checked: the crash exits non-zero, and
# stopping the workload itself still exits 0.

SVC=$(new_svcdir)
names=""
trap 'for n in $names; do ct_rm $n; done; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: app
EOF
cat > "$SVC/app" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF
cat > "$SVC/extra" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF

N=slinit-ct-crash-$$; names="$names $N"
ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"
"$RUNTIME" exec $N slinitctl start extra >/dev/null 2>&1
"$RUNTIME" exec $N slinitctl stop extra >/dev/null 2>&1
pid=$("$RUNTIME" exec $N slinitctl status app 2>/dev/null | awk '/PID:/ {print $2}')
"$RUNTIME" exec $N kill -9 "$pid" >/dev/null 2>&1
ct_wait_exit $N 10
ec=$(ct_exit_code $N)
[ "$ec" != "0" ]
check $? "the workload crashing after an unrelated stop ends the container as a failure (exit $ec)"

N=slinit-ct-stopreq-$$; names="$names $N"
ct_start $N "$SVC"
ct_wait_ready $N 10
"$RUNTIME" exec $N slinitctl stop app >/dev/null 2>&1
ct_wait_exit $N 10
ec=$(ct_exit_code $N)
[ "$ec" = "0" ]
check $? "stopping the workload on request still ends it with exit 0 (exit $ec)"

summary
