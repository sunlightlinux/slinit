# A workload killed by a signal exits with 128+signal, the shell
# convention every container tool expects. SIGUSR1 (10) is used rather
# than SIGKILL so the expected 138 cannot be confused with the runtime
# SIGKILLing slinit itself (137).

N=slinit-ct-sig-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = process
command = /bin/sh -c "sleep 1; kill -USR1 $$$$; sleep 5"
restart = false
EOF

ct_start $N "$SVC"
ct_wait_exit $N 15
check $? "container exited by itself after the workload was signalled"

ec=$(ct_exit_code $N)
[ "$ec" = "138" ]; check $? "exit code is 128+SIGUSR1 = 138 (got $ec)"

summary
