# Repeated SIGTERMs escalate (loop.go escalateShutdown): the third one
# SIGKILLs everything and forces the exit. This is acceptance case 69's
# scenario, but with slinit as PID 1 of a real container rather than a
# child of a shell, which is the configuration that case never reaches.

N=slinit-ct-escal-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: stubborn
EOF
cat > "$SVC/stubborn" <<'EOF'
type = process
command = /bin/sh -c "trap '' TERM; while :; do sleep 1; done"
stop-timeout = 60
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

"$RUNTIME" kill -s TERM $N >/dev/null
sleep 1
[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
check $? "1st SIGTERM: still shutting down (stubborn has 60s)"

"$RUNTIME" kill -s TERM $N >/dev/null
sleep 1
"$RUNTIME" kill -s TERM $N >/dev/null
ct_wait_exit $N 8
check $? "3rd SIGTERM: forced exit within 8s instead of waiting out 60s"

ct_logs $N | grep -qE "killing all processes|forcing exit"
check $? "the log says why it forced the exit"

summary
