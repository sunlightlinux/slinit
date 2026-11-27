# Ways a workload fails other than the plain process crash loop of case
# 32, each of which must end the container as a failure, after the
# restarts it is allowed and not later:
#
# - a bgprocess daemon crash-looping, its boot target restart = yes
#   (4.0.3: boot waited in STARTING for good, and the limit of 3 allowed
#   2 restarts);
# - a process with smooth-recovery, which restarts it in place without
#   taking its dependents down;
# - a scripted setup step that the workload depends on and that fails:
#   once, no restart, the workload never starts.

SVC=$(new_svcdir)
RES=$(new_svcdir)
chmod 777 "$RES"
names=""
trap 'for n in $names; do ct_rm $n; done; rm -rf "$SVC" "$RES"' EXIT

mk() { mkdir -p "$SVC/$1"; cat > "$SVC/$1/$2"; }

mk bg boot <<'EOS'
type = internal
restart = yes
depends-on: d
EOS
mk bg d <<'EOS'
type = bgprocess
command = /bin/sh -c "echo r >> /res/runs-bg; (sleep 0.3; exit 3) & echo $$! > /run/d.pid"
pid-file = /run/d.pid
restart = yes
restart-delay = 0.5
restart-limit-count = 3
restart-limit-interval = 60
EOS

mk smooth boot <<'EOS'
type = internal
depends-on: app
EOS
mk smooth app <<'EOS'
type = process
command = /bin/sh -c "echo r >> /res/runs-smooth; sleep 0.3; exit 3"
restart = yes
smooth-recovery = yes
restart-delay = 0.3
restart-limit-count = 3
restart-limit-interval = 60
EOS

mk setup boot <<'EOS'
type = internal
depends-on: app
EOS
mk setup setup <<'EOS'
type = scripted
command = /bin/sh -c "echo r >> /res/runs-setup; exit 4"
EOS
mk setup app <<'EOS'
type = process
command = /bin/sh -c "echo r >> /res/runs-setup-app; while :; do sleep 1; done"
depends-on: setup
EOS

for t in bg:4 smooth:4 setup:1; do
    k=${t%%:*} want=${t##*:}
    N=slinit-ct-loop-$k-$$; names="$names $N"
    ct_start $N "$SVC/$k" -v "$RES:/res"
    ct_wait_exit $N 15
    check $? "$k: the container ends on its own"
    ec=$(ct_exit_code $N)
    r=$(wc -l < "$RES/runs-$k" 2>/dev/null || echo 0)
    [ "$ec" != "0" ]
    check $? "$k: it ends as a failure (exit $ec)"
    [ "$r" = "$want" ]
    check $? "$k: after $want run(s) ($r)"
done
[ ! -e "$RES/runs-setup-app" ]
check $? "setup: the workload never started after its setup step failed"

summary
