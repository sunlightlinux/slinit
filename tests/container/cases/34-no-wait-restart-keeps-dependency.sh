# `slinitctl --no-wait restart app`, where app depends on dep and nothing
# else holds dep, must bring app back up with dep.
#
# A --no-wait restart sends the stop and the start back to back, so the
# start lands while app is still stopping, after app has already let go
# of dep. On 4.0.3 the start took no hold: dep stopped for good and app
# waited for it in STARTING, at the first try. Eight restarts in a row
# here, each followed by a check that app and dep are both STARTED.

N=slinit-ct-nwrestart-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOS'
type = internal
depends-on: keep
EOS
for s in keep dep; do
    printf '%s\n' 'type = process' 'command = /bin/sh -c "while :; do sleep 1; done"' > "$SVC/$s"
done
printf '%s\n' 'type = process' 'command = /bin/sh -c "while :; do sleep 1; done"' 'depends-on: dep' > "$SVC/app"

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"
"$RUNTIME" exec $N slinitctl start app >/dev/null 2>&1

state() { "$RUNTIME" exec $N slinitctl status "$1" 2>/dev/null | awk '/State:/ {print $2}'; }
ok=0 last=""
for i in 1 2 3 4 5 6 7 8; do
    "$RUNTIME" exec $N slinitctl --no-wait restart app >/dev/null 2>&1
    j=0
    while [ $j -lt 15 ]; do
        [ "$(state app)" = STARTED ] && [ "$(state dep)" = STARTED ] && break
        sleep 0.2
        j=$((j + 1))
    done
    last="app $(state app), dep $(state dep)"
    [ "$(state app)" = STARTED ] && [ "$(state dep)" = STARTED ] || break
    ok=$((ok + 1))
done
[ "$ok" = 8 ]
check $? "eight --no-wait restarts of app, app and dep STARTED after each ($ok done; $last)"

summary
