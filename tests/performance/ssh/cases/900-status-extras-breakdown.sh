# 900-status-extras-breakdown — splits the `status` vs `status5` gap
# into the two things v7 status does extra, and prices the suspected
# culprit on its own.
#
# Case 310 states the explanation: `status` walks the cgroup tree and
# calls printRecentJournal, which fork+execs a whole slinit-journalctl,
# so the ~2x gap is a subprocess spawn and not slinit's control path.
# Nothing measured it. "Roughly half journalctl fork" has been in that
# header since v2.4.2 as an assertion, and an unmeasured assertion in
# this suite is exactly how case 750 came to time an error reply for
# years.
#
# Four readings, chosen so each pair differs by one thing:
#
#   1 v5_baseline      status5 on an internal service — control path
#                      only, no extras at all.
#   2 internal_journal status on the same internal service. `boot` is
#                      type=internal and STARTED with no PID, so
#                      printCgroupTree is skipped and the journal spawn
#                      is the ONLY addition over reading 1.
#   3 process_both     status on a type=process service with a live
#                      PID, so the cgroup tree runs too.
#   4 journal_spawn    the spawn alone, with the exact argv
#                      printRecentJournal builds.
#
# So (2 - 1) is the journal spawn as paid inside status, (4) is the same
# spawn measured directly, and the two agreeing is what confirms the
# attribution rather than assuming it. (3 - 2) is the cgroup tree.
#
# Every reading is bracketed — measured twice, in opposite order, with
# the smaller taken and the spread printed — because the differences
# here are sub-millisecond and one interrupted sample would otherwise
# carry the conclusion. perf_baseline_ns documents why the minimum and
# not the mean.
#
# Measured on ceres v3.2.2, ITERS=30, all four spreads 1-2%: v5
# 1.144ms, internal 2.221ms, process 2.301ms, spawn alone 1.156ms. So
# journal_in_status 1.078ms at 49% of status, agreeing with the spawn
# timed directly to 7%. The attribution 310 asserted holds.
#
# Four runs now, and the journal half is the stable part: 1.082/49%,
# 1.078/49%, 1.094/48%, 1.148/50%. That is the finding worth quoting.
#
# The fourth run is also why both deltas carry a floor rather than only
# the cgroup one. It came back with 38% spread on the v5 reading, which
# put the journal share at 1.148 against a floor of 0.453 — still a
# measurement, at 3x its floor instead of the 37x a quiet run gives.
# Same conclusion, much coarser, and a reader cannot tell those apart
# from a bare millisecond figure. Printing the ratio is the whole
# point: it says how much the number is worth, not just what it is.
#
# The cgroup share has read -0.005, +0.080 and -0.048 ms across three
# runs on the same host. The sign never settled, so **the cgroup-tree
# cost is below what this case can resolve**, and the figure should not
# be quoted as a measurement whatever one run prints.
#
# Worth spelling out because I got it wrong twice in opposite
# directions. The first run had 14% spread on the internal reading and
# gave -0.005, which I reported as "the cgroup tree is free". The
# second had 1-2% spreads throughout and gave +0.080, which I then
# published as a measured cost — and it passes the case's own
# within-run test, because tight brackets in one run say nothing about
# agreement across runs. A single run cannot tell you that; only this
# comment can.
#
# The verdict line below is the best a single run can do: it compares
# each delta against the bracket spread of the readings it came from
# and refuses to dignify anything smaller. Treat `measured` as "not
# obviously noise in this run", not as settled.
#
# Note the whole gap is conditional on slinit-journalctl being on PATH:
# printRecentJournal does exec.LookPath and returns silently if it is
# missing, so on a target without it `status` is cheap and this case
# measures nothing. That is why it skips rather than reporting zeros.

if ! command -v slinit-journalctl > /dev/null 2>&1; then
    echo "SKIP: slinit-journalctl not on PATH — printRecentJournal is a no-op, nothing to split"
    return 0 2>/dev/null || exit 0
fi

_svc="perf-extras-$$"
_svcfile="/etc/slinit.d/$_svc"
printf 'type = process\ncommand = /bin/sh -c "while :; do sleep 60; done"\n' > "$_svcfile"
slinitctl start "$_svc" > /dev/null 2>&1

# Needs a live PID or reading 3 measures the same path as reading 2 and
# the cgroup-tree share comes out as noise around zero.
if ! slinitctl status5 "$_svc" 2>/dev/null | grep -q '[Pp][Ii][Dd]'; then
    slinitctl stop "$_svc" > /dev/null 2>&1
    slinitctl unload "$_svc" > /dev/null 2>&1
    rm -f "$_svcfile"
    echo "SKIP: throwaway process service came up without a PID — cannot separate the cgroup tree"
    return 0 2>/dev/null || exit 0
fi

_jcmd="slinit-journalctl -u boot -n 10 --no-pager --output=short"

# First bracket, in order.
_v5_1=$(perf_median_ns     "$ITERS" 'slinitctl status5 boot')
_int_1=$(perf_median_ns    "$ITERS" 'slinitctl status boot')
_proc_1=$(perf_median_ns   "$ITERS" "slinitctl status $_svc")
_spawn_1=$(perf_median_ns  "$ITERS" "$_jcmd")

# Second bracket, reversed, so a drift during the run shows up as
# spread instead of biasing one reading.
_spawn_2=$(perf_median_ns  "$ITERS" "$_jcmd")
_proc_2=$(perf_median_ns   "$ITERS" "slinitctl status $_svc")
_int_2=$(perf_median_ns    "$ITERS" 'slinitctl status boot')
_v5_2=$(perf_median_ns     "$ITERS" 'slinitctl status5 boot')

slinitctl stop   "$_svc" > /dev/null 2>&1
slinitctl unload "$_svc" > /dev/null 2>&1
rm -f "$_svcfile"

_v5=$(perf_baseline_ns    "$_v5_1"    "$_v5_2")
_int=$(perf_baseline_ns   "$_int_1"   "$_int_2")
_proc=$(perf_baseline_ns  "$_proc_1"  "$_proc_2")
_spawn=$(perf_baseline_ns "$_spawn_1" "$_spawn_2")

_ms() { awk -v n="$1" 'BEGIN{printf "%.3f", n/1e6}'; }

printf "BenchmarkStatusExtras_1_v5_baseline      %4d  %s ms (spread %s%%)\n" \
    "$ITERS" "$(_ms "$_v5")"    "$(perf_spread_pct "$_v5_1" "$_v5_2")"
printf "BenchmarkStatusExtras_2_internal_journal %4d  %s ms (spread %s%%)\n" \
    "$ITERS" "$(_ms "$_int")"   "$(perf_spread_pct "$_int_1" "$_int_2")"
printf "BenchmarkStatusExtras_3_process_both     %4d  %s ms (spread %s%%)\n" \
    "$ITERS" "$(_ms "$_proc")"  "$(perf_spread_pct "$_proc_1" "$_proc_2")"
printf "BenchmarkStatusExtras_4_journal_spawn    %4d  %s ms (spread %s%%)\n" \
    "$ITERS" "$(_ms "$_spawn")" "$(perf_spread_pct "$_spawn_1" "$_spawn_2")"

# The attribution, stated as arithmetic so a future reader does not have
# to redo it from four numbers.
# awk, not $(( )). perf_median_of_file averages the two middle samples
# when the count is even — ITERS is 30, so always — and awk prints the
# result in scientific notation (1.14653e+06), which POSIX shell integer
# arithmetic rejects outright: "Illegal number". Every other bracketed
# case in this suite does its maths in awk for the same reason. My local
# check fed clean integers and so never saw the real format.
_journal_share=$(awk -v a="$_int"  -v b="$_v5"  'BEGIN{printf "%.0f", a-b}')
_cgroup_share=$(awk -v a="$_proc" -v b="$_int" 'BEGIN{printf "%.0f", a-b}')
_share_pct=$(awk -v a="$_journal_share" -v b="$_int" 'BEGIN{
    if (b>0) printf "%.0f", 100*a/b; else print "n/a"}')
_agree_pct=$(awk -v a="$_journal_share" -v b="$_spawn" 'BEGIN{
    m=(a+b)/2; d=a-b; if(d<0) d=-d;
    if (m>0) printf "%.0f", 100*d/m; else print "n/a"}')

# A delta is only reported as a figure when it is bigger than the
# bracket spread of the readings it came from. Otherwise the case says
# so, because a number invites belief and this one did not deserve it:
# across three runs the cgroup share read -0.005, +0.080 and -0.048 ms,
# and I published the middle one as a measured 0.08 ms purely because
# that run's spreads happened to be tight. The sign never settled. The
# honest output is "below resolution", and printing the noise floor
# beside it is what makes that checkable rather than a dodge.
_cgroup_noise=$(awk -v a="$_proc_1" -v b="$_proc_2" -v c="$_int_1" -v d="$_int_2" 'BEGIN{
    p=a-b; if(p<0) p=-p; i=c-d; if(i<0) i=-i; print (p>i) ? p : i }')
_cgroup_verdict=$(awk -v d="$_cgroup_share" -v n="$_cgroup_noise" 'BEGIN{
    a=d; if(a<0) a=-a; print (a>n) ? "measured" : "below-resolution" }')

# The journal share gets the same treatment, because the rule applies to
# whichever delta it is applied to and printing it for only one of them
# invites the reader to trust the other unconditionally. It normally
# clears its floor by a wide margin — 1.077 against 0.029 ms on a quiet
# run — but a noisy one brought it to 1.148 against 0.453, still a
# measurement and a much coarser one, and the line should say which.
_journal_noise=$(awk -v a="$_int_1" -v b="$_int_2" -v c="$_v5_1" -v d="$_v5_2" 'BEGIN{
    i=a-b; if(i<0) i=-i; v=c-d; if(v<0) v=-v; print (i>v) ? i : v }')
# n==0 is reachable: two brackets can land on the identical median on a
# quiet host, and awk division by zero is fatal in some awks and gives
# "inf" in others. Either way it would take the case down, which is how
# the scientific-notation crash got here in the first place.
_journal_verdict=$(awk -v d="$_journal_share" -v n="$_journal_noise" 'BEGIN{
    a=d; if(a<0) a=-a;
    if (n<=0) { print (a>0) ? "measured-floor-0" : "below-resolution"; exit }
    if (a<=n) { print "below-resolution"; exit }
    printf "measured-%.0fx-floor", a/n }')

printf "StatusExtras_breakdown  journal_in_status=%s ms (%s%% of status)  spawn_alone=%s ms  disagreement=%s%%  noise_floor=%s ms  verdict=%s\n" \
    "$(_ms "$_journal_share")" "$_share_pct" \
    "$(_ms "$_spawn")" "$_agree_pct" \
    "$(_ms "$_journal_noise")" "$_journal_verdict"
printf "StatusExtras_cgroup_tree  %s ms  noise_floor=%s ms  verdict=%s\n" \
    "$(_ms "$_cgroup_share")" "$(_ms "$_cgroup_noise")" "$_cgroup_verdict"
