# Stopping a service takes its whole process tree with it, not just the
# process slinit forked. Background children and grandchildren left
# behind would survive as orphans on PID 1 for the life of the
# container, holding whatever they hold.

N=slinit-ct-tree-$$
SVC=$(new_svcdir)
BIN=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC" "$BIN"' EXIT

cat > "$BIN/tree.sh" <<'EOF'
#!/bin/sh
sleep 300 &
sh -c 'sleep 300 & sleep 300' &
exec sleep 300
EOF
chmod 755 "$BIN/tree.sh"

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: worker
waits-for: tree
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = yes
EOF
cat > "$SVC/tree" <<'EOF'
type = process
command = /svc-bin/tree.sh
restart = no
EOF

ct_start $N "$SVC" -v "$BIN:/svc-bin:ro"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

before=$("$RUNTIME" exec $N ps -o args= | grep -c "sleep 300")
[ "$before" -ge 4 ]; check $? "the service really built a tree ($before processes)"

"$RUNTIME" exec $N slinitctl stop tree >/dev/null 2>&1
sleep 2

after=$("$RUNTIME" exec $N ps -o args= | grep -c "sleep 300")
[ "$after" = "0" ]; check $? "the whole tree is gone after the stop ($after left)"

[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
check $? "stopping one service did not end the container"

summary
