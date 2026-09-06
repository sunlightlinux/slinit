# 800-many-directive-svc — parser stress: load a svc file with
# ~50 directives (real-world upper-bound for a heavy service).
# Measures start latency (which includes parse) vs the plain
# scripted-start baseline (case 740 = 1.126 ms). Delta = parse
# cost for the full-directive svc.
_name="perf-heavy-svc-$$"
_svcfile="/etc/slinit.d/$_name"
cat > "$_svcfile" <<'EOF'
type = scripted
command = /bin/true
description = perf heavy-directive stress test
umask = 0022
working-dir = /tmp
nice = 5
oom-score-adjust = 500
stop-timeout = 10s
start-timeout = 10s
restart = no
restart-delay = 1s
restart-limit-count = 3
restart-limit-interval = 60s
log-type = none
env-var = FOO=bar
env-var = BAZ=qux
depends-on: system-init
after: socklog
before: crond
run-as = root
cpu-affinity = 0-1
ionice-class = 2
ionice-level = 4
no-new-privs = yes
protect-home = read-only
protect-system = full
protect-kernel-tunables = yes
protect-kernel-modules = yes
protect-kernel-logs = yes
protect-control-groups = yes
protect-proc = invisible
proc-subset = pid
private-tmp = yes
private-devices = yes
private-network = no
lock-personality = yes
restrict-namespaces = yes
restrict-realtime = yes
restrict-suid-sgid = yes
memory-deny-write-execute = yes
memory-pressure-watch = yes
cpu-pressure-watch = yes
io-pressure-watch = yes
capabilities-bounding-set = CAP_NET_BIND_SERVICE
capabilities-ambient-set =
rlimit-nofile = 1024
rlimit-nproc = 100
rlimit-core = 0
rlimit-stack = 8388608
rlimit-memlock = 65536
timeout-stop-sec = 10s
success-exit-status = 0
EOF
slinitctl load "$_name" > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "CtlStart_heavy_50directive_svc" "slinitctl start $_name"

slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
