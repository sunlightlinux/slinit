# PID 1 inherits every orphan in the container and must reap them. The
# service below leaves a short-lived orphan every 0.2s: a subshell
# backgrounds `sleep` and exits, so the sleep reparents to PID 1 and dies
# half a second later. Without reaping, zombies pile up at 5 a second.
#
# The orphans live 0.5s, so two or three are always there to be seen
# under PID 1 even when `ps` is slow on a loaded host (at 0.05s the
# check sampled too seldom and failed under a full run), and the zombie
# bound leaves room for the few that have just died: unreaped there
# would be about 20.

N=slinit-ct-reap-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: orphaner
EOF
cat > "$SVC/orphaner" <<'EOF'
type = process
command = /bin/sh -c "while :; do (sleep 0.5 &); sleep 0.2; done"
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

sleep 4    # ~20 orphans born and dead by now

reparented=$("$RUNTIME" exec $N ps -o ppid,args | awk '$1 == 1 && $2 == "sleep"' | wc -l)
zombies=$("$RUNTIME" exec $N ps -o stat | grep -c '^Z')
[ "$zombies" -le 3 ]; check $? "no zombie build-up after ~20 orphans ($zombies in Z state)"

# Make sure the scenario actually happened: orphans do land on PID 1.
seen=0
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if "$RUNTIME" exec $N ps -o ppid,args | awk '$1 == 1 && $2 == "sleep" {f=1} END {exit !f}'; then
        seen=1; break
    fi
    sleep 0.1
done
[ "$seen" = 1 ] || [ "$reparented" -gt 0 ]
check $? "orphans were reparented to PID 1 (the test exercised reaping)"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 5000 ]; check $? "clean stop under orphan churn (exit $ec, ${ms}ms)"

summary
