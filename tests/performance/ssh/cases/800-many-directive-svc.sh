# 800-many-directive-svc — parser stress: load a svc file with
# 43 directives (real-world upper-bound for a heavy service).
# Measures start latency (which includes parse) vs the plain
# scripted-start baseline (case 740 = 1.126 ms). Delta = parse
# cost for the full-directive svc.
_name="perf-heavy-svc-$$"
_svcfile="/etc/slinit.d/$_name"
# Every line must be a directive slinit accepts, or the service does not
# load and every start times the load error. Until 2026-11-01 it had 20
# that were not: names slinit never had (env-var, ionice-class,
# private-devices, timeout-stop-sec...), durations written `10s` where
# slinit takes seconds, and dependencies on system-init, socklog and
# crond. `slinitctl load ... || true` hid it, so the figure was never a
# parse of this file. Check edits with slinit-check before trusting it.
#
# Unquoted EOF so the dependency lines name the suite's fixture.
cat > "$_svcfile" <<EOF
type = scripted
command = /bin/true
description = perf heavy-directive stress test
umask = 0022
working-dir = /tmp
nice = 5
oom-score-adj = 500
stop-timeout = 10
start-timeout = 10
restart = no
restart-delay = 1
restart-limit-count = 3
restart-limit-interval = 60
log-type = none
depends-on: $PERF_TARGET_DEP
after: $PERF_TARGET
before: boot
run-as = root
cpu-affinity = 0-1
ioprio = be:4
options = no-new-privs
protect-home = read-only
protect-system = full
protect-kernel-tunables = yes
protect-kernel-modules = yes
protect-kernel-logs = yes
protect-control-groups = yes
protect-proc = invisible
proc-subset = pid
private-tmp = yes
lock-personality = yes
restrict-namespaces = yes
restrict-realtime = yes
restrict-suidsgid = yes
memory-deny-write-execute = yes
memory-pressure-watch = yes
cpu-pressure-watch = yes
io-pressure-watch = yes
capability-bounding-set = cap_net_bind_service
rlimit-nofile = 1024
rlimit-data = 1073741824
rlimit-core = 0
rlimit-as = 4294967296
EOF
slinitctl load "$_name" > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "CtlStart_heavy_43directive_svc" "slinitctl start $_name"

# stop before unload: the benchmark above leaves the service STARTED,
# and unload refuses a service that is not stopped ("service is not
# stopped"). The error went to /dev/null, so every run leaked one
# loaded service into PID 1 — 62 had accumulated on the reference
# machine, inflating every later measurement and the RSS the leak
# cases watch.
slinitctl stop "$_name" > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
