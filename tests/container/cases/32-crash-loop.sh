# A workload in a crash loop, with a restart delay and a restart limit.
#
# While it loops the container must stay up, also during each restart
# delay, when nothing runs (a service waiting out its delay counts as
# active). `docker stop` in the middle of the loop must end it promptly
# with exit 0. And once the restart limit is used up the container must
# end as a failure, after the restarts, not at the first crash. Up to
# 4.0.3 it never ended: app's last exit stopped boot, boot (restart =
# yes) came back at once and waited for app, and app settled into
# FAILED without telling it, so boot waited in STARTING for good. And
# restart-limit-count = 3 allowed only 2 restarts: each exit was counted
# twice against the limit.

SVC=$(new_svcdir)
LOOP=$(new_svcdir)
RES=$(new_svcdir)
chmod 777 "$RES"
names=""
trap 'for n in $names; do ct_rm $n; done; rm -rf "$SVC" "$LOOP" "$RES"' EXIT

cat > "$SVC/boot" <<'EOS'
type = internal
restart = yes
depends-on: app
EOS
cat > "$SVC/app" <<'EOS'
type = process
command = /bin/sh -c "echo run >> /res/runs; sleep 0.3; exit 3"
restart = yes
restart-delay = 1
restart-limit-count = 3
restart-limit-interval = 60
EOS
# The same loop with a limit it does not reach in this case.
cp "$SVC/boot" "$LOOP/boot"
sed 's/^restart-limit-count = 3$/restart-limit-count = 100/' "$SVC/app" > "$LOOP/app"

N=slinit-ct-loop-stop-$$; names="$names $N"
ct_start $N "$LOOP" -v "$RES:/res"
sleep 3.5
[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
check $? "the container stays up while app crash-loops"
r=$(wc -l < "$RES/runs" 2>/dev/null || echo 0)
[ "$r" -ge 2 ]
check $? "app was restarted in the meantime ($r runs)"
ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 5000 ]
check $? "docker stop in the crash loop exits 0 in ${ms}ms (exit $ec)"

rm -f "$RES/runs"
N=slinit-ct-loop-limit-$$; names="$names $N"
ct_start $N "$SVC" -v "$RES:/res"
ct_wait_exit $N 20
check $? "the container ends once the restart limit is used up"
ec=$(ct_exit_code $N)
r=$(wc -l < "$RES/runs" 2>/dev/null || echo 0)
[ "$ec" != "0" ]
check $? "it ends as a failure (exit $ec)"
[ "$r" = 4 ]
check $? "after app was restarted up to the limit: 3 restarts, 4 runs ($r runs)"

summary
