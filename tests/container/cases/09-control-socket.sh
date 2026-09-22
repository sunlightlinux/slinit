# `docker exec <ct> slinitctl ...` is how an operator manages services in
# a running container. The control socket has to be up and answer the
# everyday commands, with exit codes a script can trust.

N=slinit-ct-ctl-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF
cat > "$SVC/extra" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF
cat > "$SVC/broken" <<'EOF'
type = process
command = /nonexistent/binary
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

ctl() { "$RUNTIME" exec $N slinitctl "$@" >/dev/null 2>&1; }

ctl start extra;  check $? "slinitctl start extra exits 0"
"$RUNTIME" exec $N slinitctl status extra 2>/dev/null | grep -q "STARTED"
check $? "extra is STARTED"
ctl stop extra;   check $? "slinitctl stop extra exits 0"
ctl start broken; [ $? -ne 0 ]; check $? "slinitctl start broken exits non-zero"
ctl restart worker; check $? "slinitctl restart worker exits 0"

summary
