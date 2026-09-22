# PID 1 inherits every orphan in the container and must reap them. The
# service below leaves a short-lived orphan every 0.2s: a subshell
# backgrounds `sleep` and exits, so the sleep reparents to PID 1 and dies
# a moment later. Without reaping, zombies pile up at 5 a second.

N=slinit-ct-reap-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: orphaner
EOF
cat > "$SVC/orphaner" <<'EOF'
type = process
command = /bin/sh -c "while :; do (sleep 0.05 &); sleep 0.2; done"
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

sleep 4    # ~20 orphans born and dead by now

reparented=$("$RUNTIME" exec $N ps -o ppid,args | awk '$1 == 1 && $2 == "sleep"' | wc -l)
zombies=$("$RUNTIME" exec $N ps -o stat | grep -c '^Z')
[ "$zombies" -le 1 ]; check $? "no zombie build-up after ~20 orphans ($zombies in Z state)"

# Make sure the scenario actually happened: orphans do land on PID 1.
seen=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
    if "$RUNTIME" exec $N ps -o ppid,args | awk '$1 == 1 && $2 == "sleep" {f=1} END {exit !f}'; then
        seen=1; break
    fi
    sleep 0.1
done
[ "$seen" = 1 ] || [ "$reparented" -gt 0 ]
check $? "orphans were reparented to PID 1 (the test exercised reaping)"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 3000 ]; check $? "clean stop under orphan churn (exit $ec, ${ms}ms)"

summary
