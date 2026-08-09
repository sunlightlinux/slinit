# Functional Tests

Automated QEMU-based integration tests for slinit running as PID 1.

Each test boots a minimal Alpine Linux VM with slinit as init, runs a test
script inside the guest via a virtio-serial channel, and validates the output.

## Usage

```bash
# Run all tests (218 cases)
./tests/functional/run-tests.sh

# Run a single test
./tests/functional/run-tests.sh tests/functional/cases/01-boot-starts.sh

# Run multiple specific tests
./tests/functional/run-tests.sh tests/functional/cases/01-*.sh tests/functional/cases/05-*.sh

# Verbose output (show VM console log on failure)
VERBOSE=1 ./tests/functional/run-tests.sh

# Force VM image rebuild
KEEP_BUILD=0 ./tests/functional/run-tests.sh

# Custom timeout per test (default: 60s)
TIMEOUT=120 ./tests/functional/run-tests.sh
```

## Requirements

- Go 1.25+
- `qemu-system-x86_64`
- `curl`, `cpio`, `gzip`
- `socat` or `nc` (for virtio-serial result reading)
- KVM recommended (falls back to software emulation)

### KVM stability caveat on hybrid CPUs and nested virt

On Intel hybrid architectures (Meteor Lake / Core Ultra 100 series and
newer) or when the test host is itself a guest under another hypervisor
(VirtualBox, VMware, Hyper-V), functional-test VMs can fail with a
kernel Oops during boot or shutdown — typically `inflate_fast` page
fault during initramfs decompression, `0xCC` poison in registers, or
an NX-protected page execution. The bug lives in the KVM ↔ hybrid-CPU
feature-passthrough path; the guest kernel is fine on non-hybrid
silicon and non-nested KVM.

If you hit this, pass `-cpu kvm64` to the QEMU invocation (or drop
`-enable-kvm` and let it fall back to TCG at the cost of ~10-30× slower
boot). Bare-metal hosts on older Intel, AMD Zen, or server SKUs are
unaffected.

## Test Cases

| # | Name | What it validates |
|---|------|-------------------|
| 01 | boot-starts | Boot service reaches STARTED state |
| 02 | list-services | `slinitctl list` shows all services |
| 03 | start-stop | Start and stop a service via control socket |
| 04 | trigger | Trigger/untrigger a triggered service |
| 05 | dependencies | Dependency chain ordering and propagation |
| 06 | scripted-service | Scripted service start/stop commands |
| 07 | restart | Auto-restart on failure |
| 08 | logbuffer | Log buffer capture and catlog retrieval |
| 09 | boot-time | Boot timing analysis command |
| 10 | signal | Signal delivery to service processes |
| 11 | env | Runtime environment management (setenv/getallenv) |
| 12 | provides-alias | Service alias lookup via `provides` |
| 13 | restart | Restart command (stop + start) |
| 14 | wake-release | Wake (start without marking active) and release |
| 15 | is-started-failed | Exit code status checks (is-started/is-failed) |
| 16 | reload | Hot config reload from disk |
| 17 | unload | Unload stopped services from memory |
| 18 | add-rm-dep | Runtime dependency add/remove |
| 19 | unpin | Pin/unpin service state |
| 20 | enable-disable | Enable/disable (waits-for dep management) |
| 21 | shutdown | Shutdown command acceptance |
| 22 | chain-to | Service chaining (chain-to directive) |
| 23 | start-timeout | Start timeout handling |
| 24 | working-dir | Working directory for service processes |
| 25 | cpu-affinity | CPU pinning via sched_setaffinity |
| 26 | stop-command | Stop-command execution before signal |
| 27 | consumer-of | Pipe logging (log-type=pipe + consumer-of) |
| 28 | env-file | Environment file loading into service |
| 29 | slinit-check | Offline and online config linter |
| 30 | finish-command | Finish-command runs after process exit with args |
| 31 | down-file | Down marker file prevents service auto-start |
| 32 | pause-continue | Pause (SIGSTOP) and continue (SIGCONT) a service |
| 33 | once | Start once without auto-restart |
| 34 | env-dir | Runit-style env-dir (one file per variable) |
| 35 | ready-check | Ready-check-command polling-based readiness |
| 36 | initd-autodetect | /etc/init.d auto-detect with LSB headers |
| 37 | socket-activation | Socket listen, LISTEN_FDS env, socket file |
| 38 | cron-task | Cron-like periodic task execution |
| 39 | start-limiter | Soft parallel start limit (all services start) |
| 40 | shared-logger | Multi-service shared logger (SharedLogMux) |
| 41 | namespace | PID/user namespace isolation with UID/GID mapping |
| 42 | pre-stop-hook | Pre-stop hook runs before service stop |
| 43 | control-command | Custom signal handler (control-command-HUP) |
| 44 | chroot | Chroot filesystem isolation |
| 45 | lock-file | Exclusive lock file (flock) for services |
| 46 | log-rotation | Log file rotation by size with max-files limit |
| 47 | log-filtering | Log include/exclude regex filtering |
| 48 | new-session | New session (setsid) for service process |
| 49 | close-fds | Close stdin/stdout/stderr (redirect to /dev/null) |
| 50 | nice-oom-ioprio | Nice value and OOM score adjustment |
| 51 | clock-guard | Boot-time clock protection (floor + timestamp file) |
| 52 | catch-all-logger | Early-boot catch-all logger captures stdout/stderr to `/run/slinit/catch-all.log` |
| 53 | restart-backoff | Restart-delay step + cap apply progressive backoff between restarts |
| 54 | overlay-config | `conf.d/` overlay overrides values in the base service description |
| 55 | service-template | Service templates with `@argument` substitution (`$1`) |
| 56 | rlimits | rlimit-* values are applied to the service process |
| 57 | extra-commands | `extra-command-*` and `extra-started-command-*` custom actions |
| 58 | healthcheck | `healthcheck-command` detects an unhealthy service |
| 59 | smooth-recovery | Smooth recovery restarts without propagating failure to dependents |
| 60 | service-env | `SLINIT_SERVICENAME` / `SLINIT_SERVICEDSCDIR` auto-injected per service |
| 61 | options-flags | `options =` flags (kill-all-on-stop, signal-process-only) |
| 62 | query-deps | `slinitctl dependents` / dependency graph query |
| 63 | required-paths | `required-files` / `required-dirs` pre-start guards |
| 64 | stop-timeout | `stop-timeout` escalates to SIGKILL on timeout |
| 65 | term-signal | `term-signal` sends a custom signal instead of SIGTERM on stop |
| 66 | bgprocess | bgprocess service type reads PID from a `pid-file` |
| 67 | watchdog | `watchdog-timeout` kills + restarts unresponsive service |
| 68 | load-options | `load-options` `export-passwd-vars` / `export-service-name` |
| 69 | restart-limit | `restart-limit-count` puts service into FAILED after too many restarts |
| 70 | include-directive | `@include` inlines another file into the service definition |
| 71 | umask | `umask =` sets the file-creation mask for the service process |
| 72 | path-activation | `start-on-path-exists` starts a service when an inotify-watched file appears |
| 73 | override-files | a sibling `<service>.override` file replaces the base service's command and description |
| 74 | script-block | `script ... end script` inline shell body runs as the service command |
| 75 | apparmor | `apparmor-switch` fails closed when the AppArmor LSM is unavailable; plain services unaffected |
| 76 | debug | `debug = yes` SIGSTOPs the runner pre-exec; service runs only after SIGCONT |
| 77 | service-dirs | `runtime-directory`/`state-directory` created on start; runtime dir removed on stop, state dir persists |
| 78 | sandbox | filesystem sandbox knobs (private-tmp, protect-system, protect-home) rewrite the child's mount namespace |
| 79 | sandbox-expansion | `${RUNTIME_DIR}`/`${STATE_DIR}` placeholders resolve in sandbox path lists |
| 80 | seccomp | `system-call-filter` / `system-call-architectures` install a seccomp BPF filter that blocks the named syscalls |
| 81 | hardening | Restrict*/Protect* cluster (protect-kernel-*, lock-personality, protect-hostname, protect-clock, protect-control-groups) applied via slinit-runner |
| 82 | credentials | `load-credentials`/`import-credentials`/`set-credentials` populate `${CREDENTIALS_DIRECTORY}` for the service process |
| 83 | initd-openrc-depend | /etc/init.d auto-detect handles OpenRC-style `depend()` — `need X` translates to slinit `depends-on`, script sourced with start/stop dispatch |
| 84 | slinit-binfmt | `--root=DIR` fixture: late-wins discovery, parse errors include file+line; real /proc/sys/fs/binfmt_misc register/unregister when the kernel supports it (exit 3 when it doesn't) |
| 85 | slinit-sysctl | applies dotted + slashed keys to real /proc/sys/*; verbose summary reports applied/ignored/errors; `-` best-effort miss is ignored by default but escalates under `--strict`; malformed config error names file+line |
| 86 | slinit-svc-value | file-per-key backing under `$RC_SVCDIR/options/`; symlink dispatch (service_get_value, save_options alias, etc.); empty-value delete; no trailing newline on writes; `service_export` skips already-stored keys; SLINIT_SERVICENAME env fallback |
| 87 | slinit-start-stop-daemon | `--start --background --make-pidfile` fork against real /bin/sleep; `--status` probes pidfile-live; double-`--start` refused with exit 1 (softened to 0 under `--oknodo`); `--stop --retry TERM/2/KILL/2` terminates the child; stale pidfile yields LSB code 5 (0 with `--oknodo`) |
| 88 | slinit-supervise-daemon | detach into supervisor loop via re-exec + SLINIT_SSD_SUPERVISOR=1; supervisor + daemon.pidfile companion both written; short-lived child respawned within budget (>=2 iterations); `--stop` tears the tree down and cleans both pidfiles; second `--stop` with missing pidfile still exits 0 |
| 89 | slinit-fstabinfo | fixture-driven output selectors (`--blockdevice`, `--options`, `--mountargs`, `--passno /mnt`); filters (`--fstype`, `--passno =2`); positional narrowing; `--file` seam; EINFO_QUIET suppression |
| 90 | slinit-mountinfo | real `/proc/mounts` includes `/proc` with fstype=proc, rootfs skipped; fixture drives reverse-order output, regex filters (fstype/skip-fstype, point-regex), `--node`/`--options` selectors; `--netdev`/`--nonetdev` cross-reference `/etc/fstab` for `_netdev`; relative positional rejected |
| 91 | slinit-einfo | argv[0] dispatch across 22 applet symlinks; einfo→stdout / ewarn+eerror→stderr; `n`-suffix suppresses newline; `v*` variants gated on EINFO_VERBOSE; EINFO_QUIET blanket suppression; eend marker + status propagation; eindent no-op; eval_ecolors emits all 6 shell vars; ewaitfile fires + times out |
| 92 | slinit-shell-var | single-arg mapping (`my-service.d/1` → `my_service_d_1`); multi-arg joined with literal space, inner spaces sanitised; pure-punctuation → all underscores; zero args → empty; sanitised output usable as a shell identifier (round-trip via `eval`) |
| 93 | heartbeat | `--heartbeat-interval` emits a grep-friendly summary line (active/failed/stopped/starting/stopping counts, restarts(N), watchdog-misses, rss) |
| 94 | stderr-ring-buffer | `--stderr-ring-buffer-size` + `--stderr-ring-buffer-interval` arm the daemon's recent-log ring; RingDumper announces itself in the log |
| 95 | profile-subsystem | runsvchdir-analogue: `list-profiles`, `active-profile`, `activate-profile` (validate against loaded services, `-` deactivates, unknown profile NAKed) |
| 96 | log-forward-udp | `log-forward-udp = host:port` sends producer stdout to a UDP listener framed per RFC 3164; self-tests the receiver on BusyBox and SKIPs if UDP is dropped |
| 97 | sentinel-file-ipc | `--sentinel-dir` inotify path — chmod +x on `reboot` fires the handler + audit line + unlink; plain touch is ignored |
| 98 | svcdirwatch | `--watch-services-dir` inotify auto-load: a service dropped into the dir becomes startable without an explicit reload-all |
| 99 | command-argv0 | override argv[0] presented to the child (chpst -b analogue); SKIPs on BusyBox where /bin/sleep dispatches on argv[0] |
| 100 | log-max-line-length | svlogd -l analogue: overlong lines truncated to N + `+` overflow marker, still land in the log |
| 101 | log-sanitize | svlogd -r/-R analogue: control bytes rewritten to the sanitize char before disk |
| 102 | log-timestamp | svlogd -tt analogue: `log-timestamp = human` prepends `YYYY-MM-DD_HH:MM:SS.µs` |
| 103 | shared-logger-lossy | `shared-logger-lossy = yes` + `shared-logger-queue-size` opt-in path; producer output still reaches the sink under backpressure |
| 104 | log-buffer-nmin | `log-buffer-size` + `logfile-min-files` parse cleanly and don't break rotator init |
| 105 | wait-timeout | `-w SEC` / `--wait=SEC` caps how long slinitctl waits for a reply; non-integer / negative values fail flag-parse before touching the socket |
| 106 | shutdown-flag-surface | `slinit-shutdown --help` lists every documented reboot(8)-compat flag (`--reboot`, `--halt`, `-r/-h/-p/-s/-k/-f`, `--force`, `--no-sync`, `--no-wtmp`, `--wtmp-only`, `--no-wall`, `--use-passed-cfd`, `--system`, `--grace=`) |
| 107 | status-file-namecap | `slinitctl status` prints a `File:` line; mtime bump after load surfaces `(modified since loaded)`; `.`-prefix names rejected at load |
| 108 | openrc-depend-ordering | `depend() { after other }` in an init.d script maps to advisory ordering (`AfterOptional`), not a hard dep — both services load and start via init.d auto-detect |
| 109 | kexec-preflight | `slinitctl shutdown kexec` warns when `/sys/kernel/kexec_loaded == 0`; nested `slinit --user` isolates the shutdown so the host isn't affected |
| 110 | enable-v7-status | protocol v7 `CmdEnableServiceV7` returns the target's status in the same round-trip; distinguishes "enabled" from "already enabled" |
| 111 | protect-kernel-modules | blocks `init_module`/`finit_module`/`delete_module` via seccomp; probe: modprobe from inside the guarded service |
| 112 | protect-kernel-logs | blocks `syslog(2)`; seccomp mode 2 active on child |
| 113 | protect-clock | blocks `clock_settime`/`settimeofday`/`adjtimex` via seccomp |
| 114 | protect-control-groups | remounts `/sys/fs/cgroup` ro in the service's mount ns; PID 1's view of `cgroup.controllers` stays readable |
| 115 | protect-hostname | blocks `sethostname`/`setdomainname`; host hostname untouched |
| 116 | lock-personality | blocks `personality(2)` via seccomp; child alive under mode-2 filter |
| 117 | namespace-net-ipc | service runs in distinct net + IPC namespaces from PID 1 (inode ids differ) |
| 118 | sched-policy | `sched-policy = fifo` → SCHED_FIFO via chrt -p (SKIP if chrt missing) |
| 119 | sched-priority | `sched-policy = rr` + `sched-priority = 42` → SCHED_RR / prio 42 |
| 120 | sched-deadline | SCHED_DEADLINE via `sched-runtime`/`sched-deadline`/`sched-period` |
| 121 | sched-reset-on-fork | RESET_ON_FORK bit surfaced (either explicit flag or policy+priority readback) |
| 122 | securebits | `securebits = keep-caps,no-setuid-fixup` parses; child comes up |
| 123 | normal-exit | `normal-exit = 42` — scripted svc exiting 42 lands in STOPPED (not FAILED) |
| 124 | success-action | `success-action = none` parses cleanly and svc reaches a terminal state |
| 125 | mlockall | `mlockall = current+future` → `RLIMIT_MEMLOCK = unlimited` in `/proc/PID/limits` (mlockall is not inherited across execve; the durable effect is the raised rlimit) |
| 126 | numa-mempolicy | `numa-mempolicy = bind` on node 0; SKIPs if CONFIG_NUMA is off |
| 127 | state-directory-mode | `state-directory` + `state-directory-mode` creates `/var/lib/<svc>` with the requested mode |
| 128 | cache-directory | `cache-directory` + `cache-directory-mode` at `/var/cache/<svc>` |
| 129 | runtime-directory-preserve | `runtime-directory-preserve = yes` keeps `/run/<svc>/…` after stop |
| 130 | namespace-cgroup | `namespace-cgroup = yes` puts the service in its own cgroup ns |
| 131 | reboot-argument | parse-only smoke for `reboot-argument = recovery`; svc reaches a terminal state |
| 132 | socket-permissions | `socket-permissions = 0640` sets the listener socket mode |
| 133 | slinit-init-maker | `-dry-run` doesn't touch disk; real generation writes boot/system-init/getty-tty[N]/README; `-force` overwrites; slinit-check accepts the generated tree |
| 134 | slinit-seedrng | fresh run writes seed.credit or seed.no-credit under `-seed-dir`; `-skip-credit` accepted; second run rotates the seed (sha256 changes) |
| 135 | cgroup-v2 | memory.max / memory.high / pids.max / cpu.weight applied to the service's cgroup (with subtree_control auto-delegation) |
| 136 | vtty | `vtty = true` opens `/run/slinit/vtty-<svc>.sock`; `/proc/PID/stat` tty_nr non-zero; socket removed on stop |
| 137 | bundle-of | s6-rc-style bundles resolve into their members via `slinitctl start <bundle>` |
| 138 | log-select | s6-log-style include/exclude regex chain filters logger stdin |
| 139 | persist-intent | slinit-shutdown `--persist-intent` writes /run/slinit/shutdown.intent |
| 140 | supplementary-groups | numeric GID resolver installs `Groups: 27 100 500` on the child's /proc/PID/status; guarded by run-as = 65534:65534 |
| 141 | psi-pressure | `memory-pressure-watch = yes` keeps a `/sys/fs/cgroup/.../memory.pressure` fd open on slinit (PID 1); fd is released on stop |
| 142 | condition-fraction | staged rollout gate: `TAG:0` skips (no PID), `TAG:100` starts (live PID) — bucket math is deterministic against `/etc/machine-id` |
| 143 | condition-path-is-socket | `S_ISSOCK` check: `/run/slinit.socket` (real AF_UNIX) starts, `/etc/os-release` (regular file) skips |
| 144 | condition-security=measured-os | no TPM in the QEMU VM → `condition-security = measured-os` fails and skips the service |
| 145 | minimum-uptime-sec | `slinit --help` surface: `--minimum-uptime-sec` flag documented with boot-loop context |
| 146 | memory-thp | `memory-thp = never` routes through slinit-runner; child's `/proc/PID/status` THP_enabled=0 when the field is exposed (kernel ≥ 5.11) |
| 147 | fd-store-preserve | `file-descriptor-store-preserve = yes\|no\|on-success` all parse and start; NOTIFY_SOCKET is exported to each child |
| 148 | dynamic-user | `dynamic-user = yes` allocates a transient UID from the [61184, 65519] pool; UID reallocated on restart, never appears in `/etc/passwd` |
| 149 | no-new-privs | `options = no-new-privs` → child's `/proc/PID/status NoNewPrivs=1`; control service without the option shows `NoNewPrivs=0` |
| 150 | ambient-cap | `capabilities = cap_net_bind_service` sets bit 10 (0x400) in child's CapAmb/CapEff/CapPrm (validated with `run-as = nobody`) |
| 151 | close-fds | `close-stdin/close-stdout/close-stderr = yes` redirects fds 0/1/2 → /dev/null; each `/proc/PID/fd/N` resolves to `/dev/null` |
| 152 | protect-kernel-tunables | ro-remount of `/proc/sys` blocks writes to `net.ipv4.ip_forward`; seccomp deny list catches `swapoff` |
| 153 | protect-proc | `protect-proc = invisible` (hidepid=invisible) hides PID 1 (root-owned) from a `run-as = nobody` service; own PID still visible |
| 154 | proc-subset=pid | `/proc/uptime`, `/proc/meminfo` disappear inside the service's mount ns; PID dirs remain; host `/proc/uptime` unaffected |
| 155 | condition-measured-uki | Predicate skips when the boot wasn't measured via UKI + TPM PCR 4/5/7; asserts on measured host |
| 156 | dynamic-user | Transient UID from the per-daemon pool; `/etc/passwd` unchanged, `/proc/PID/status Uid:` reports new UID |
| 157 | fdstore-preserve | `file-descriptor-store-preserve = on-success` retains FDSTORE=1 entries across restart; explicit `no` drops them |
| 158 | psi-pressure-watch | `memory-pressure-watch = yes` + threshold fires SvcEventPressureMemory when cgroup memory PSI crosses limit |
| 159 | measured-os | `condition-security = measured-os` verifies TPM event log; skip on TPM-less host |
| 160 | freeze-thaw | `slinitctl freeze/thaw` toggles cgroup v2 `cgroup.freeze`; atomic vs SIGSTOP for whole subtree |
| 161 | cron-accuracy-sec | Cron fires are coalesced into buckets of the configured accuracy for RTC wakeup batching |
| 162 | job-timeout-sec | Whole-job timer aborts the start even when the underlying command hasn't blown its start-timeout |
| 163 | env-generator | Executable that emits KEY=VAL lines at start-time; merged after env-file/env-dir, wins conflicts |
| 164 | slice-hierarchy | Nested `slice = system.slice/foo.slice` produces the matching cgroup path under `/sys/fs/cgroup/` |
| 165 | slinit-tmpfiles | Declarative `f/d/L/w/e` directives populate `/run`/`/var` at boot; type-aware apply matches systemd-tmpfiles.d |
| 166 | slinit-sysusers | Declarative user/group creation at boot; idempotent, honours pre-existing entries |
| 167 | pre-start-command | Hook runs sync before main; non-zero exit blocks the fork/exec, service never reaches STARTED |
| 168 | post-start-command | Hook runs async after STARTED; a slow hook does NOT delay the STARTED promotion |
| 169 | reload-signal | `slinitctl reload-signal svc` delivers `reload-signal = SIG*` to main pid; undeclared svc fails cleanly |
| 170 | start-timeout-ready-check | `start-timeout` fires when `ready-check-command` never succeeds; service ends up FAILED, passing ready-check services proceed normally |
| 171 | reset-env | `slinitctl reset-env svc` clears runtime setenv mutations; getallenv drops the previously-set keys |
| 172 | log-processor | Each rotated logfile is passed through `log-processor`; multiple rotations within the window all fire the hook |
| 173 | metadata | `author` / `version` / `usage` directives surface as labeled lines in `slinitctl status`; absent directives produce no phantom lines |
| 174 | ioprio | `ioprio = be:7` reaches the child; `ionice -p PID` reports best-effort class and priority 7 (distinct from any default) |
| 175 | always-chain | `options = always-chain` fires `chain-to` even after non-zero exit; without the flag the chain is suppressed on failure |
| 176 | shares-console | `options = shares-console` parses and starts cleanly (flag round-trip through config → record) |
| 177 | tty-cluster | Full TTY cluster (tty-path + columns/rows/vhangup/vt-disallocate/reset) — setupTTY opens /dev/tty1, wires stdin, applies knobs |
| 178 | restrict-cluster | Full restrict-* cluster (realtime/namespaces/suidsgid/file-systems/address-families) stacks and installs cleanly; child sees Seccomp: 2 |
| 179 | dbus-cluster | bus-name / bus-policy / bus-name-scope parse under the dbus-optional design (no dbus daemon in VM) |
| 180 | psi-cpu-io-pressure | cpu-pressure-watch/threshold + io-pressure-watch/threshold parse + service starts |
| 181 | path-activation-full | start-on-path-changed / -path-modified / -directory-not-empty each fire via pathwatch (uses /etc/slinit.d/-anchored markers) |
| 182 | kill-signal-cluster | kill-mode + final-kill-signal + restart-kill-signal + watchdog-signal + survive-final-kill-signal coexist |
| 183 | timeout-cluster | timeout-sec + timeout-abort-sec + timeout-start-failure-mode + timeout-stop-failure-mode parse |
| 184 | cgroup-expanded | memory-high/-low/-min + swap-max + cpu-max + io-weight + cpuset-cpus + cgroup-setting + startup-allowed-* land in cgroupfs (spot-checked via memory.high) |
| 185 | log-pipeline-extras | logfile-permissions/uid/gid/rotate-time + log-forward-* + log-level-max + log-sanitize/-extra + log-line-prefix + log-max-line-length + log-read-buffer-size parse; mode 0640 verified on file |
| 186 | service-dir-modes | runtime/state/cache/logs/configuration-directory each with -mode + -quota + -accounting; all 5 dirs created with requested mode |
| 187 | notify-guess-exit-openfile | notify-access + guess-main-pid + exit-type + open-file combo parses + starts |
| 188 | pass-unset-env | pass-environment whitelist and unset-environment blacklist visible in child /proc/self/environ |
| 189 | standard-input | standard-input-text and standard-input-data bytes reach child stdin (base64 decoded for -data) |
| 190 | exec-condition-searchpath | exec-condition=/bin/true runs command; =/bin/false skips it; exec-search-path parses |
| 191 | import-credential | import-credential parses coexisting with set-credential; credentials tmpfs populated |
| 192 | lsm-fail-closed | selinux-context and smack-process-label refuse to start when /sys/fs/selinux and /sys/fs/smackfs are absent (fail-closed contract) |
| 193 | restart-misc | start-limit-action + restart-force-exit-status + restart-max-delay + restart-mode + runtime-randomized-extra coexist |
| 194 | skarnet-niches | alert-file + alert-level + options=pass-cs-fd + utmp-mode all parse and coexist |
| 195 | options-cluster-a | options=unmask-intr,skippable,start-interruptible round-trip through parser |
| 196 | options-cluster-b | options=runs-on-console attaches console to STARTED svc (starts-on-console covered in acceptance) |
| 197 | starts-rwfs-log | options=starts-rwfs,starts-log with ready-notification=pipefd:3 promote to STARTED on readiness byte |
| 198 | bucket-b-legacy | coredump-filter + ignore-sigpipe + memory-ksm + personality + remove-ipc + timer-slack-nsec coexist |
| 199 | cgroup-cpuset-hugetlb | cgroup-cpuset-mems + cgroup-hugetlb + cpuset-partition parse; kernels without hugetlb controller fail-open |
| 200 | seccomp-arch-log-mdwe | system-call-architectures + system-call-log + memory-deny-write-execute stack on top of @system-service filter; Seccomp mode 2 confirmed |
| 201 | misc-coverage | cron-persistent + cron-randomized-delay + socket-uid/-gid + bind-read-only-paths + keyword non-match |
| 202 | journalctl-list-boots | `--list-boots` enumeration + `-b` shortcut (bare / `-b0` / `--boot`); boot ID matches event's `_boot_id` field (v2.1.0) |
| 203 | journalctl-kernel-events | `-k`/`--dmesg` returns bare-prefix `kernel:` lines with no [PID] bracket (v2.1.0 render rule); JSON has `unit=kernel` + `_transport=kernel` |
| 204 | slinit-supports | self-introspection CLI: `--list-directives` / `--list-opcodes` / `--list-all` + name lookup + non-zero exit on unknown (v2.1.0) |
| 205 | slinitctl-analyze | analyze `time`/`blame`/`critical-chain`/`dot` with live impls; `plot` is a documented not-implemented stub (v2.1.2) |
| 206 | slinit-runit-convert | log companion path (v2.1.5): `-log` file with `consumer-of` + `log-type = pipe` on primary; `working-dir` defaults to sv dir |
| 207 | slinit-openrc-convert | variable-only script (self-contained slinit file) vs custom `start()` script (wrapped via `openrc-run` at runtime) (v2.1.4) |
| 208 | slinit-systemd-convert | `Type`/`Restart`/`User`+`Group`/`After`+`Requires` with `.service`/`.target` suffix strip; `forking`→`bgprocess`; `oneshot` defaults `restart=no`; line continuation joins ExecStart (v2.1.4) |
| 209 | journalctl-invocation-tracking | per-start UUID + `--list-invocations` dedupe on fresh boot; `--invocation` filter isolates a single lifecycle (v2.1.8) |
| 210 | journalctl-catalog | catalog round-trip via `--root`: `--list-catalog` / `--dump-catalog` / `--update-catalog`; compiled gob cache; header title-casing (`Defined-By`) (v2.1.8) |
| 211 | journalctl-fss-setup-keys | fresh `--setup-keys` mints key + prints verification token; second run refuses without `--force`; `--force` rotates the seed (v2.1.8/v2.1.9) |
| 212 | journalctl-fss-verify | binary `--verify` full tamper round-trip: sealed daemon → verify clean → tamper → verify FAIL (v2.1.0 a6371e1) |
| 213 | dinit-env-compat | `SLINIT_SERVICENAME` + `SLINIT_SERVICEDSCDIR` in child env; `slinitctl` honors `DINIT_SOCKET_PATH` (v2.1.0 4eee317) |
| 214 | slinitctl-disable-dual-wire | atomic V7 default vs `--dinit-compat` client-side + `CmdRmDepV7`; symlink probe tolerates either `waits-for.d` or `boot.d` layout (v2.1.0 6b5f58d) |
| 215 | journalctl-verbose-export | verbose 4-space indent + `[sec.ns]` header; export `__REALTIME`/`__MONOTONIC` lines, no verbose indent (v2.1.0) |
| 216 | journalctl-group-a-bundle | `--fields` / `--header` / `--disk-usage` / `-F` / `--utc` / `--no-hostname` / `--output-fields` / `-g` in one bundled case (v2.1.6 Group A) |
| 217 | journalctl-vacuum-flush | `--vacuum-files` direct on files (current-day preserved); `--flush` + `--relinquish-var` via spawned daemon + admin control socket (v2.1.10) |
| 218 | journalctl-namespace | `--namespace` daemon + `--list-namespaces` enumeration + filter on synthetic JSONL (v2.1.11) |

## How It Works

1. **Build phase**: `build-vm.sh` downloads Alpine Linux minirootfs, cross-compiles
   the slinit binaries (daemon, `slinitctl`, `slinit-check`, `slinit-monitor`,
   `slinit-shutdown`, `slinit-init-maker`, `slinit-nuke`, `rc-service`, `rc-update`,
   `rc-status`) and creates an initramfs with demo services
2. **Per-test boot**: Each test gets its own QEMU VM boot. The test script is
   injected into the initramfs as a service
3. **Guest runner**: `lib/guest-runner.sh` runs inside the VM, waits for slinit
   to be ready, executes the test script, and writes results to a virtio-serial port
4. **Host reader**: `run-tests.sh` reads results from the virtio-serial Unix socket
   and reports PASS/FAIL

## Adding Tests

1. Create `tests/functional/cases/NN-name.sh`
2. If the test needs custom services, create a `.d/` directory with the same
   base name (e.g., `51-clock-guard.d/`) containing service files (`boot` + others)
3. Use assertion helpers from `lib/assert.sh`:
   - `assert_eq "$val" "expected" "description"` — exact string match
   - `assert_contains "$output" "needle" "description"` — substring match
   - `assert_not_contains "$output" "needle" "description"` — absence check
   - `assert_exit_code "command" 0 "description"` — run command and check exit code
   - `assert_service_state "name" "STATE" "description"` — check via slinitctl
   - `wait_for_service "name" "STATE" timeout_secs` — poll until state reached
   - `test_summary` (must call at end of every test)
4. Manual assertions: increment `_TESTS_RUN` and `_TESTS_FAILED` directly for
   custom checks (see existing tests for examples)
5. Exit 0 = pass, non-zero = fail

Example:

```bash
#!/bin/sh
# Test: my new feature

wait_for_service "boot" "STARTED" 10
output=$(slinitctl --system status myservice 2>&1)
assert_contains "$output" "STARTED" "myservice is started"
test_summary
```

## Service Files for Tests

Tests that need custom services can include a `.d/` directory alongside the
`.sh` file (e.g., `05-dependencies.d/` contains service files loaded into
`/etc/slinit.d/` for that test).

## Debugging

```bash
# Run with verbose output to see VM console log
VERBOSE=1 ./tests/functional/run-tests.sh tests/functional/cases/05-dependencies.sh

# Check test output files
cat tests/functional/_output/05-dependencies/result.txt
cat tests/functional/_output/05-dependencies/console.log
```
