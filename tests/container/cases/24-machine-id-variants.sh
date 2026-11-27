# Container images commonly ship /etc/machine-id empty, or holding
# "uninitialized" (systemd's first-boot marker), and a broken one is
# possible too. Every service event is stamped with the machine ID, and
# up to v4.0.3 an ID that did not parse left the cache unready, so the
# first event made slinit panic: as PID 1 of a machine a kernel panic at
# every boot (101b022, functional case 296), here the container dying.
# systemd treats empty and "uninitialized" as no ID at all, so those
# must not even warn (9d76338); a malformed one warns and boots on a
# transient ID.

SVC=$(new_svcdir)
MID=$(new_svcdir)
names=""
trap 'for n in $names; do ct_rm $n; done; rm -rf "$SVC" "$MID"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF
# Not under boot: stopping it must not end the container (13's contract).
cat > "$SVC/probe" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF

for variant in empty uninitialized malformed; do
    N=slinit-ct-mid-$variant-$$
    names="$names $N"
    case $variant in
        empty)         : > "$MID/$variant" ;;
        uninitialized) echo uninitialized > "$MID/$variant" ;;
        malformed)     echo 'not-a-machine-id' > "$MID/$variant" ;;
    esac
    chmod 644 "$MID/$variant"
    ct_start $N "$SVC" -v "$MID/$variant:/etc/machine-id:ro"
    ct_wait_ready $N 10
    check $? "$variant machine-id: the boot service reached STARTED"

    # A start and a stop: service events, each stamped with the ID.
    "$RUNTIME" exec $N slinitctl start probe >/dev/null 2>&1
    "$RUNTIME" exec $N slinitctl stop probe >/dev/null 2>&1
    sleep 1
    [ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
    check $? "$variant machine-id: slinit is still running after service events"

    logs=$(ct_logs $N)
    if printf '%s\n' "$logs" | grep -q -E 'panic|nil pointer|Attempted to kill init'; then
        fail "$variant machine-id: $(printf '%s\n' "$logs" | grep -m1 -E 'panic|nil pointer')"
    fi
    case $variant in
        empty | uninitialized)
            if printf '%s\n' "$logs" | grep -qi 'machine-id'; then
                fail "$variant machine-id warned: $(printf '%s\n' "$logs" | grep -i -m1 'machine-id')"
            else
                pass "$variant machine-id: treated as none, no warning"
            fi ;;
    esac
    ct_rm $N
done

summary
