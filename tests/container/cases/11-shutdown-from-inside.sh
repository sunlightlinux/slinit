# `slinitctl shutdown <type>` from inside the container, which is how a
# process in the container asks to end it. Each type must bring the
# services down, exit 0, and record its own halt code. softreboot exits
# too: re-executing slinit in place would leave the runtime with a
# container that never stops.

N=slinit-ct-shutdown-$$
SVC=$(new_svcdir)
RES=$(new_svcdir)
chmod 777 "$RES"
trap 'ct_rm $N; rm -rf "$SVC" "$RES"' EXIT

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

for pair in halt:h poweroff:p reboot:r softreboot:s; do
    type=${pair%%:*} want=${pair##*:}
    rm -f "$RES/haltcode" "$RES/exitcode"
    ct_start $N "$SVC" -v "$RES:/run/slinit/container-results"
    if ! ct_wait_ready $N 10; then
        fail "$type: container never became ready"
        ct_rm $N
        continue
    fi
    "$RUNTIME" exec $N slinitctl shutdown "$type" now >/dev/null 2>&1
    ct_wait_exit $N 8
    check $? "$type: container exited within 8s"
    ec=$(ct_exit_code $N)
    hc=$(cat "$RES/haltcode" 2>/dev/null)
    [ "$ec" = "0" ] && [ "$hc" = "$want" ]
    check $? "$type: exit 0 with haltcode '$want' (got exit $ec, haltcode '$hc')"
    ct_rm $N
done

summary
