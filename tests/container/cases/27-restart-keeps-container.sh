# `docker exec CT slinitctl restart app`, where the boot target depends
# on app, must not end the container.
#
# slinitctl restarts as a stop and then a start. The stop takes the boot
# target down with app, so for a moment nothing is active, and a
# container's slinit ends when nothing is active: on 4.0.3 about half of
# such restarts ended the container (slinitctl: "Error: EOF"). Five in a
# row here, each followed by a check that the container still runs and
# app is up again.

N=slinit-ct-restart-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: app
EOF
cat > "$SVC/app" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

ok=0
for i in 1 2 3 4 5; do
    "$RUNTIME" exec $N slinitctl restart app >/dev/null 2>&1 || break
    sleep 0.5
    [ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ] || break
    "$RUNTIME" exec $N slinitctl status app 2>/dev/null | grep -q 'State: *STARTED' || break
    ok=$((ok + 1))
done
[ "$ok" = 5 ]
check $? "five restarts of app, the container up and app STARTED after each ($ok done)"

summary
