# `docker logs` must keep working after boot, not just during it.
#
# slinit mutes the catch-all's console tee once the boot target is up:
# on real hardware that console is a serial line shared with getty.
# A container has neither, the console IS the runtime's log stream, and
# /run/slinit/catch-all.log disappears with the container — so muting it
# made slinit silent for everything that happened afterwards. A service
# crash-looping to its restart limit produced no output at all.

N=slinit-ct-logvis-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = yes
EOF
cat > "$SVC/later" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = no
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

# Everything from here on is post-boot, the window that used to be mute.
"$RUNTIME" exec $N slinitctl start later >/dev/null 2>&1
sleep 1
ct_logs $N | grep -q "later"
check $? "a service started after boot appears in the container log"

"$RUNTIME" exec $N slinitctl stop later >/dev/null 2>&1
sleep 1
ct_logs $N | grep -qE "STOPPD|later"
check $? "stopping it is logged too"

ct_stop_ms $N 10 >/dev/null
ct_logs $N | grep -q "Shutting down slinit"
check $? "the shutdown announcement is in the log"

summary
