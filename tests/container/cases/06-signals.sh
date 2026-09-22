# Each shutdown signal a container runtime or an operator can send to
# PID 1, and what it must do in container mode: bring the services down
# in order and exit 0, with the halt code saying which kind of shutdown
# it was. SIGINT is what `docker run` forwards on Ctrl-C; the RT signals
# are systemd's container convention (`docker kill -s RTMIN+4`).

N=slinit-ct-sig-$$
SVC=$(new_svcdir)
RES=$(new_svcdir)
chmod 777 "$RES"
trap 'ct_rm $N; rm -rf "$SVC" "$RES"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF

for pair in INT:h RTMIN+3:h RTMIN+4:p RTMIN+5:r; do
    sig=${pair%%:*} want=${pair##*:}
    rm -f "$RES/haltcode" "$RES/exitcode"
    ct_start $N "$SVC" -v "$RES:/run/slinit/container-results"
    if ! ct_wait_ready $N 10; then
        fail "SIG$sig: container never became ready"
        ct_rm $N
        continue
    fi
    "$RUNTIME" kill -s "$sig" $N >/dev/null
    ct_wait_exit $N 8
    check $? "SIG$sig: container exited on its own within 8s"
    ec=$(ct_exit_code $N)
    hc=$(cat "$RES/haltcode" 2>/dev/null)
    [ "$ec" = "0" ] && [ "$hc" = "$want" ]
    check $? "SIG$sig: exit 0 with haltcode '$want' (got exit $ec, haltcode '$hc')"
    ct_rm $N
done

summary
