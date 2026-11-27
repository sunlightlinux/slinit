# Functional Tests

Automated QEMU-based integration tests for slinit running as PID 1.

Each test boots a minimal Alpine Linux VM with slinit as init, runs a test
script inside the guest via a virtio-serial channel, and validates the output.

## Usage

```bash
# Run all tests (326 cases)
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
- `sha256sum` (coreutils): optional downloads are pinned by checksum
- `unsquashfs` or `7z`, optional: extracts `autofs4.ko` and `efivarfs.ko`
  from the netboot modloop for 277-slinit-mount-bind-flags and
  222-nosystemd-efivarfs-ro, which skip without them
- a musl build of `libslinit-mlock.so`, optional, for 125-mlockall. Any
  one of: `musl-gcc` (Debian/Ubuntu `musl-tools`), `zig`, or just the
  host's `cc` — with none of the first two, `build-vm.sh` fetches Alpine's
  `musl-dev` (3.4 MB, pinned sha256) and links against the image's own
  `ld-musl`, which is all `musl-gcc` does. The library is checked before
  it goes in the image (musl in `DT_NEEDED`, and musl's loader runs its
  constructor); a glibc build is refused, because in the guest it fails
  silently.
- OVMF, optional, for 222-nosystemd-efivarfs-ro (`.d/firmware` = `efi`).
  Found at the usual places — `/usr/share/qemu/ovmf-x86_64-4m-*.bin`
  (openSUSE), `/usr/share/OVMF/OVMF_{CODE,VARS}{_4M,}.fd` (Debian/Ubuntu),
  `/usr/share/edk2/ovmf/`, `/usr/share/edk2/x64/`, `/usr/share/edk2-ovmf/`
  — or set `SLINIT_OVMF_CODE` and `SLINIT_OVMF_VARS`. Plain builds only,
  not secure-boot or SMM ones. The vars file is copied per run, never
  written. Without OVMF the case is reported SKIP before booting.
- Alpine's `vmlinuz-lts` (13 MB, pinned sha256), fetched by
  `build-vm.sh` for 126-numa-mempolicy: `linux-virt` is built without
  `CONFIG_NUMA`. When the mirror has moved on to a newer lts the pin no
  longer matches, the build says so, and 126 is reported SKIP until the
  checksum is updated. The same goes for `musl-dev` and 125.

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

### A green run is not full coverage

A case that finds its precondition missing calls `skip_case "<reason>"`
and is reported **SKIP**, counted under "skipped" and not under
"passed". On a host with everything under Requirements few cases skip,
and each remaining skip names a gap listed below — but the tally to read
is still all three numbers, not the first one.

It did not always work that way. A skipping case used to print `SKIP:`
and then return 0, which the harness reported as **PASS** — a
case that asserted nothing was indistinguishable from a case that
verified everything. `164-slice-hierarchy` asserts that a service
configured with `slice` alone lands in `/system.slice/<name>`, and it
skipped for want of cgroup v2 from the day it was written, which is how
`slice` came to be silently dropped by the loader for the whole life of
the directive without the suite noticing (fixed in v2.3.9).

What the VM does *not* provide, and what that leaves uncovered:

| Missing | Cases affected |
|---------|----------------|
| TPM | `condition-security = measured-os` checks the negative path only |
| OVMF on the host | `222-nosystemd-efivarfs-ro` SKIPs |
| a musl build of `libslinit-mlock.so` | `125-mlockall` SKIPs |
| the pinned `vmlinuz-lts` | `126-numa-mempolicy` SKIPs |
| `unsquashfs`/`7z` on the host | `277-slinit-mount-bind-flags`, `222` SKIP |

The last four are host-side, and the build or the harness says which is
missing (see Requirements). cgroup v2, `chrt`, `/etc/machine-id` and a
UDP-capable `nc`, which this table used to list, are all there now: the
cgroup cases mount cgroup2 through their own `system-init`, and the
others run (checked 2026-11-26: 96, 118, 135, 141, 142, 160, 164, 180,
184, 199, 221 all PASS, none SKIP).

So: when a case covers something you are actually changing, check that
it *ran*.

### Checking the harness itself

```bash
./tests/functional/selftest.sh
```

Every case under `cases/` is written to pass, so the suite cannot notice
when the harness stops reporting failures — it just gets greener. That
has happened twice, both times found by accident:

* `e1673d4` — `_output=$(...) || true` made `$?` always 0, "causing all
  functional tests to report PASS even when assertions failed".
* the console-streaming change — `(. "$TEST_SCRIPT") | tee /dev/console`
  followed by `_rc=$?`, which reads `tee`'s status and not the script's.
  Same outcome, five months later, and it shipped in three green CI runs.

`selftest.sh` boots three cases whose verdicts are known in advance — one
that must pass, one that must fail, one that must skip — and checks what
the harness said about each. CI runs it on shard 0 before the cases, so a
harness that cannot fail a case fails there instead of returning eight
green shards. Run it yourself after touching anything in `lib/`.

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
| 21 | shutdown | `shutdown poweroff +10` is reported pending by `shutdown --status` and `shutdown -c` cancels it |
| 22 | chain-to | Service chaining (chain-to directive) |
| 23 | start-timeout | Start timeout handling |
| 24 | working-dir | Working directory for service processes |
| 25 | cpu-affinity | CPU pinning via sched_setaffinity, on a 2-CPU VM: the pinned service runs on CPU 1 only, an unpinned sibling on 0-1 |
| 26 | stop-command | Stop-command runs on stop (leaves a marker the case checks) |
| 27 | consumer-of | Pipe logging (log-type=pipe + consumer-of) |
| 28 | env-file | Environment file loading into service |
| 29 | slinit-check | Offline and online config linter |
| 30 | finish-command | Finish-command runs after process exit with args |
| 31 | down-file | Down marker file keeps a service pulled in by `waits-for` stopped; an explicit start overrides it |
| 32 | pause-continue | Pause (SIGSTOP) and continue (SIGCONT) a service |
| 33 | once | Start once without auto-restart |
| 34 | env-dir | Runit-style env-dir (one file per variable) |
| 35 | ready-check | Ready-check-command polling-based readiness |
| 36 | initd-autodetect | /etc/init.d auto-detect with LSB headers |
| 37 | socket-activation | Socket listen, LISTEN_FDS env, socket file |
| 38 | cron-task | Cron-like periodic task execution |
| 39 | start-limiter | A nested `slinit --user --parallel-start-limit 2` starts four 1.5 s scripted services; at most 2 are starting at any moment and all four finish |
| 40 | shared-logger | Multi-service shared logger (SharedLogMux) |
| 41 | namespace | PID/user namespace isolation with UID/GID mapping |
| 42 | pre-stop-hook | Pre-stop hook runs before service stop |
| 43 | control-command | Custom signal handler (control-command-HUP) |
| 44 | chroot | Chroot filesystem isolation |
| 45 | lock-file | Exclusive lock file (flock) for services |
| 46 | log-rotation | Log file rotation by size with max-files limit |
| 47 | log-filtering | log-include drops DEBUG, log-exclude then drops odd-numbered ERROR lines the include let through |
| 48 | new-session | New session (setsid) for service process |
| 49 | close-fds | Close stdin/stdout/stderr (redirect to /dev/null) |
| 50 | nice-oom-ioprio | Nice value and OOM score adjustment |
| 51 | clock-guard | Boot with the RTC at 2010: the guard moves the clock forward to the seeded `/var/lib/slinit/clock` stamp (not the 2024 floor, not host time) and rewrites the file |
| 52 | catch-all-logger | Early-boot catch-all logger captures stdout/stderr to `/run/slinit/catch-all.log` |
| 53 | restart-backoff | Restart-delay step + cap: gaps between attempts measured from `/proc/uptime` grow 0.3 → 0.9 → 1.5 s and hold at the 1.5 s cap |
| 54 | overlay-config | `conf.d/` overlay overrides values in the base service description |
| 55 | service-template | Service templates with `@argument` substitution (`$1`) |
| 56 | rlimits | rlimit-* values are applied to the service process |
| 57 | extra-commands | `extra-command-*` and `extra-started-command-*` custom actions |
| 58 | healthcheck | `healthcheck-command` detects an unhealthy service |
| 59 | smooth-recovery | Smooth recovery restarts without propagating failure to dependents |
| 60 | service-env | `SLINIT_SERVICENAME` / `SLINIT_SERVICEDSCDIR` auto-injected per service |
| 61 | options-flags | `options = signal-process-only`: a child outlives the service's stop; kill-all-on-stop's cgroup sweep is not observable here (no cgroup mounted), its half is the control |
| 62 | query-deps | `slinitctl dependents` / dependency graph query |
| 63 | required-paths | `required-files` / `required-dirs` pre-start guards |
| 64 | stop-timeout | `stop-timeout` escalates to SIGKILL on timeout |
| 65 | term-signal | `term-signal` sends a custom signal instead of SIGTERM on stop |
| 66 | bgprocess | bgprocess service type reads PID from a `pid-file` |
| 67 | watchdog | `watchdog-timeout` kills + restarts unresponsive service |
| 68 | load-options | `load-options` `export-passwd-vars` (USER, HOME=/root, SHELL from passwd) / `export-service-name` |
| 69 | restart-limit | `restart-limit-count` puts service into FAILED after too many restarts |
| 70 | include-directive | `@include` inlines another file into the service definition |
| 71 | umask | `umask =` sets the file-creation mask for the service process |
| 72 | path-activation | `start-on-path-exists` starts a service when an inotify-watched file appears |
| 73 | override-files | a sibling `<service>.override` file replaces the base service's command and description |
| 74 | script-block | `script ... end script` inline shell body runs as the service command |
| 75 | apparmor | `apparmor-switch` fails closed when the AppArmor LSM is unavailable; plain services unaffected |
| 76 | debug | `debug = yes` SIGSTOPs the runner pre-exec; service runs only after SIGCONT |
| 77 | service-dirs | `runtime-directory`/`state-directory` created on start; runtime dir removed on stop, state dir persists |
| 78 | sandbox | filesystem sandbox knobs (private-tmp, protect-system, protect-home) rewrite the child's mount namespace; a `read-write-paths` hole under a read-only `/etc` stays writable |
| 79 | sandbox-expansion | `${RUNTIME_DIR}`/`${STATE_DIR}` placeholders resolve in sandbox path lists |
| 80 | seccomp | `system-call-filter` / `system-call-architectures` install a seccomp BPF filter that blocks the named syscalls |
| 81 | hardening | Restrict*/Protect* cluster (protect-kernel-*, lock-personality, protect-hostname, protect-clock, protect-control-groups) applied via slinit-runner |
| 82 | credentials | `load-credentials`/`import-credentials`/`set-credentials` populate `${CREDENTIALS_DIRECTORY}` for the service process |
| 83 | initd-openrc-depend | /etc/init.d auto-detect handles OpenRC-style `depend()` — `need X` translates to slinit `depends-on`, script sourced with start/stop dispatch |
| 84 | slinit-binfmt | `--root=DIR` fixture: late-wins discovery, parse errors include file+line; real /proc/sys/fs/binfmt_misc register/unregister when the kernel supports it (exit 3 when it doesn't) |
| 85 | slinit-sysctl | applies dotted + slashed keys to real /proc/sys/*; verbose summary reports applied/ignored/errors; `-` best-effort miss is ignored by default but escalates under `--strict`; malformed config error names file+line |
| 86 | slinit-svc-value | file-per-key backing under `$RC_SVCDIR/options/`; symlink dispatch (service_get_value, save_options alias, etc.); empty-value delete; no trailing newline on writes; `service_export` skips already-stored keys; SLINIT_SERVICENAME env fallback |
| 87 | slinit-start-stop-daemon | `--start --background --make-pidfile` fork against real /bin/sleep; `--status` probes pidfile-live; double-`--start` refused with exit 1 (softened to 0 under `--oknodo`); `--stop --retry TERM/2/KILL/2` terminates the child; stale pidfile yields LSB code 5 (0 with `--oknodo`) |
| 88 | slinit-supervise-daemon | detach into supervisor loop via re-exec with a private argv marker; supervisor + daemon.pidfile companion both written; short-lived child respawned within budget (>=2 iterations); `--stop` tears the tree down and cleans both pidfiles; second `--stop` with missing pidfile still exits 0 |
| 89 | slinit-fstabinfo | fixture-driven output selectors (`--blockdevice`, `--options`, `--mountargs`, `--passno /mnt`); filters (`--fstype`, `--passno =2`); positional narrowing; `--file` seam; EINFO_QUIET suppression |
| 90 | slinit-mountinfo | real `/proc/mounts` includes `/proc` with fstype=proc, rootfs skipped; fixture drives reverse-order output, regex filters (fstype/skip-fstype, point-regex), `--node`/`--options` selectors; `--netdev`/`--nonetdev` cross-reference `/etc/fstab` for `_netdev`; relative positional rejected |
| 91 | slinit-einfo | argv[0] dispatch across 22 applet symlinks; einfo→stdout / ewarn+eerror→stderr; `n`-suffix suppresses newline; `v*` variants gated on EINFO_VERBOSE; EINFO_QUIET blanket suppression; eend marker + status propagation; eindent no-op; eval_ecolors emits all 6 shell vars; ewaitfile fires + times out |
| 92 | slinit-shell-var | single-arg mapping (`my-service.d/1` → `my_service_d_1`); multi-arg joined with literal space, inner spaces sanitised; pure-punctuation → all underscores; zero args → empty; sanitised output usable as a shell identifier (round-trip via `eval`) |
| 93 | heartbeat | `--heartbeat-interval` emits a grep-friendly summary line (active/failed/stopped/starting/stopping counts, restarts(N), watchdog-misses, rss) |
| 94 | stderr-ring-buffer | `--stderr-ring-buffer-size` + `--stderr-ring-buffer-interval` arm the daemon's recent-log ring; RingDumper announces itself in the log |
| 95 | profile-subsystem | runsvchdir-analogue: `list-profiles`, `active-profile`, `activate-profile` (validate against loaded services, `-` deactivates, unknown profile NAKed) |
| 96 | log-forward-udp | `log-forward-udp = host:port` sends producer stdout to a UDP listener framed per RFC 3164; self-tests the receiver on BusyBox and SKIPs if UDP is dropped |
| 97 | sentinel-file-ipc | `--sentinel-dir` inotify path — chmod +x on `reboot` fires the handler + audit line + unlink; plain touch is ignored |
| 98 | svcdirwatch | `--watch-services-dir` inotify auto-load: a service dropped into the dir shows up in `list` without being requested, and is unloaded when its file is removed |
| 99 | command-argv0 | override argv[0] presented to the child (chpst -b analogue); SKIPs on BusyBox where /bin/sleep dispatches on argv[0] |
| 100 | log-max-line-length | svlogd -l analogue: overlong lines truncated to N + `+` overflow marker, still land in the log |
| 101 | log-sanitize | svlogd -r/-R analogue: control bytes rewritten to the sanitize char before disk |
| 102 | log-timestamp | svlogd -tt analogue: `log-timestamp = human` prepends `YYYY-MM-DD_HH:MM:SS.µs` |
| 103 | shared-logger-lossy | `shared-logger-lossy = yes` on a logger that does not read for 4 s: the producer's 4000 lines do not block it, and only some reach the logger |
| 104 | log-buffer-nmin | logfile on a 64 KiB tmpfs filled by ~100 KB of output: with `logfile-min-files = 1` old files are pruned on ENOSPC and the last line is still written |
| 105 | wait-timeout | `-w SEC` / `--wait=SEC` caps how long slinitctl waits for a reply; non-integer / negative values fail flag-parse before touching the socket |
| 106 | shutdown-flag-surface | `slinit-shutdown --help` lists every documented reboot(8)-compat flag (`--reboot`, `--halt`, `-r/-h/-p/-s/-k/-f`, `--force`, `--no-sync`, `--no-wtmp`, `--wtmp-only`, `--no-wall`, `--use-passed-cfd`, `--system`, `--grace=`) |
| 107 | status-file-namecap | `slinitctl status` prints a `File:` line; mtime bump after load surfaces `(modified since loaded)`; `.`-prefix names rejected at load |
| 108 | openrc-depend-ordering | `depend() { after other }` in an init.d script is advisory ordering: starting B alone leaves A stopped (the scripts ship in `.d/initd/` so the fallback is enabled at boot) |
| 109 | kexec-preflight | `slinitctl shutdown kexec` warns when `/sys/kernel/kexec_loaded == 0`; nested `slinit --user` isolates the shutdown so the host isn't affected |
| 110 | enable-v7-status | protocol v7 `CmdEnableServiceV7` returns the target's status in the same round-trip; distinguishes "enabled" from "already enabled" |
| 111 | protect-kernel-modules | blocks `init_module`/`finit_module`/`delete_module` via seccomp; probe: modprobe from inside the guarded service |
| 112 | protect-kernel-logs | blocks `syslog(2)`; seccomp mode 2 active on child |
| 113 | protect-clock | blocks `clock_settime`/`settimeofday`/`adjtimex` via seccomp |
| 114 | protect-control-groups | remounts `/sys/fs/cgroup` ro in the service's mount ns; PID 1's view of `cgroup.controllers` stays readable |
| 115 | protect-hostname | blocks `sethostname`/`setdomainname`; host hostname untouched |
| 116 | lock-personality | blocks `personality(2)` via seccomp; child alive under mode-2 filter |
| 117 | namespace-net-ipc | service runs in distinct net + IPC namespaces from PID 1 (inode ids differ) |
| 118 | sched-policy | `sched-policy = fifo` → policy 1, priority 10 in /proc/PID/stat |
| 119 | sched-priority | `sched-policy = rr` + `sched-priority = 42` → policy 2, priority 42 in /proc/PID/stat |
| 120 | sched-deadline | `sched-runtime`/`sched-deadline`/`sched-period` → policy 6 in /proc/PID/stat |
| 121 | sched-reset-on-fork | the FIFO service's child, forked after the policy is set, is SCHED_OTHER |
| 122 | securebits | `securebits = keep-caps,no-setuid-fixup` parses; child comes up |
| 123 | normal-exit | `normal-exit = 42` with `restart = yes`: exit 42 runs once, a control exiting 41 is respawned |
| 124 | success-action | two nested `slinit --user`: a clean exit with `success-action = exit` terminates its daemon, with `none` the daemon stays up |
| 125 | mlockall | `mlockall = current+future` → service memory locked (`VmLck` > 0, via the `libslinit-mlock.so` preload) and `RLIMIT_MEMLOCK = unlimited` in `/proc/PID/limits`; SKIPs without a musl build of the library (see Requirements) |
| 126 | numa-mempolicy | `numa-mempolicy = bind` on node 0 → every mapping of the service in `/proc/PID/numa_maps` shows `bind:0`; boots the lts kernel (virt has no CONFIG_NUMA) with two NUMA nodes |
| 127 | state-directory-mode | `state-directory` + `state-directory-mode` creates `/var/lib/<svc>` with the requested mode |
| 128 | cache-directory | `cache-directory` + `cache-directory-mode` (0710, not the 0755 default) at `/var/cache/<svc>` |
| 129 | runtime-directory-preserve | `runtime-directory-preserve = yes` keeps `/run/<svc>/…` after stop |
| 130 | namespace-cgroup | `namespace-cgroup = yes` puts the service in its own cgroup ns |
| 131 | reboot-argument | nested `slinit --user`: the success action is handed `reboot-argument = recovery` (logged) and the daemon exits |
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
| 142 | condition-fraction | staged rollout gate: `TAG:0` skips, `TAG:100` runs; with a seeded `/etc/machine-id` the bucket is 46.22%, so `stage1:46` skips and `stage1:47` runs |
| 143 | condition-path-is-socket | `S_ISSOCK` check: `/run/slinit.socket` (real AF_UNIX) starts, `/etc/os-release` (regular file) skips |
| 144 | condition-measured-os | no TPM in the QEMU VM → `condition-security = measured-os` fails and skips the service |
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
| 156 | refuse-manual-start-stop | `refuse-manual-start` blocks a direct `slinitctl start` once stopped; `refuse-manual-stop` blocks a direct stop while running; `--force` overrides the stop |
| 157 | restart-randomized-delay | A failing service restarts with 0.1 s + random 0–0.4 s: every gap stays in range and the gaps vary (without the jitter they are all 0.1 s) |
| 158 | reset-failed | `slinitctl reset-failed` (by name and `--all`) makes `is-failed` say no for a missing binary and for a non-zero scripted exit |
| 159 | stop-when-unneeded | `stop-when-unneeded` stops a helper once its last dependent is gone |
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
| 180 | psi-cpu-io-pressure | cpu-pressure-watch / io-pressure-watch arm a trigger: PID 1 holds the cgroup's cpu.pressure and io.pressure open |
| 181 | path-activation-full | start-on-path-changed / -path-modified / -directory-not-empty each fire via pathwatch (uses /etc/slinit.d/-anchored markers) |
| 182 | kill-signal-cluster | kill-mode/final-kill/restart-kill/watchdog-signal/survive-final-kill parse; `slinitctl restart` delivers `restart-kill-signal` (SIGUSR1) while a plain stop delivers SIGTERM |
| 183 | timeout-cluster | timeout cluster parses; stopping a SIGTERM-ignoring service takes timeout-sec (2 s) + timeout-abort-sec (1 s) and the SIGABRT phase is seen |
| 184 | cgroup-expanded | memory-high/-low/-min, swap-max, cpu-max, io-weight (where the kernel has it), cpuset-cpus and cgroup-setting each read back from the service's cgroup; the case mounts cgroup2 itself |
| 185 | log-pipeline-extras | logfile-permissions/uid/gid/rotate-time + log-forward-* + log-level-max + log-sanitize/-extra + log-line-prefix + log-max-line-length + log-read-buffer-size parse; mode 0640 on the file, `[svc] ` prefix on its lines, and log-level-max = notice keeps a <5> line and drops a <7> one |
| 186 | service-dir-modes | runtime/state/cache/logs/configuration-directory each with -mode + -quota + -accounting; all 5 dirs created with requested mode |
| 187 | notify-guess-exit-openfile | notify-access/guess-main-pid/exit-type parse; `open-file` hands the child an fd on the file |
| 188 | pass-unset-env | pass-environment whitelist and unset-environment blacklist visible in child /proc/self/environ |
| 189 | standard-input | standard-input-text and standard-input-data bytes reach child stdin (base64 decoded for -data) |
| 190 | exec-condition-searchpath | exec-condition=/bin/true runs command; =/bin/false skips it; exec-search-path becomes the child's PATH |
| 191 | import-credential | `import-credential = my-secret-*` copies the two matching credentials from a seeded `/etc/credstore` (content intact) and not the third; set-credential alongside |
| 192 | lsm-fail-closed | selinux-context and smack-process-label refuse to start when /sys/fs/selinux and /sys/fs/smackfs are absent (fail-closed contract) |
| 193 | restart-misc | restart-policy tail parses; `restart-force-exit-status = 42 43` restarts a service that exits 42 although `restart = no` |
| 194 | skarnet-niches | alert-file + alert-level + pass-cs-fd + utmp-mode parse; alert-level=err routes only the <3> line to the alert file, the main log gets all three |
| 195 | options-cluster-a | options=unmask-intr,skippable,start-interruptible parse; unmask-intr + runs-on-console gives the service a controlling terminal (tty_nr != 0) |
| 196 | options-cluster-b | `options = runs-on-console`: the service's stdout is `/dev/console`, a control's is not |
| 197 | starts-rwfs-log | options=starts-rwfs,starts-log with ready-notification=pipefd:3 promote to STARTED on readiness byte |
| 198 | bucket-b-legacy | Bucket B legacy cluster parses; coredump-filter 0x3f, timer-slack-nsec 120000 and personality x86 read back from `/proc/PID` (values chosen to differ from the kernel defaults) |
| 199 | cgroup-cpuset-hugetlb | cpuset.mems, cpuset.cpus.partition and hugetlb.2MB.max (a byte limit) read back from the service's cgroup; the case mounts cgroup2 itself |
| 200 | seccomp-arch-log-mdwe | system-call-architectures + system-call-log + memory-deny-write-execute stack on top of @system-service filter; Seccomp mode 2 confirmed |
| 201 | misc-coverage | cron-persistent + cron-randomized-delay + socket-uid/-gid + bind-read-only-paths + keyword non-match |
| 202 | journalctl-list-boots | `--list-boots` enumeration + `-b` shortcut (bare / `-b0` / `--boot`); boot ID matches event's `_boot_id` field (v2.1.0) |
| 203 | journalctl-kernel-events | `-k`/`--dmesg` returns kernel lines (must not be empty: slinit starts with no `/dev` in this VM), as bare-prefix `kernel:` lines with no [PID] bracket (v2.1.0 render rule); JSON has `unit=kernel` + `_transport=kernel` |
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
| 219 | nosystemd-clock-jump | systemd#1143 — a wall-clock step made PID 1 spin printing "Time has been changed"; the wall clock is stepped a year forward and back (`date -s @epoch`; each step checked) and PID 1 must stay responsive, idle and not churn services (v2.3.5) |
| 220 | nosystemd-dep-failure | systemd#1312 + #6478 — a service whose hard dependency failed must stay down rather than restart-loop, and `slinitctl start` must exit non-zero when the start ends in failure (v2.3.5) |
| 221 | nosystemd-logout-survivors | Debian #825394 — nohup'd jobs, tmux and screen must survive their session ending; the most-cited systemd complaint from users who leave work running over ssh (v2.3.5) |
| 222 | nosystemd-efivarfs-ro | systemd#2402 — efivarfs must be mounted read-only, so a careless recursive delete cannot wipe EFI variables and brick the firmware (v2.3.5); boots under OVMF with `efivarfs.ko` loaded before slinit, so the filesystem is mountable; SKIPs without OVMF on the host |
| 223 | nosystemd-journald-restart | systemd#6620 — restarting the journal daemon must not silence already-running services; slinit's events sit in PID 1's own ring, so no service writes into a closed pipe (v2.3.5) |
| 224 | nosystemd-numeric-username | systemd#6237 — a `run-as` value naming an account like `0day` must resolve as a *name*, not be read as UID 0 and run as root (v2.3.5) |
| 225 | pid1-signal-survival | Not from the nosystemd list: PID 1 must not die from a signal a local root can send it. Host-driven — the harness presses the real Ctrl+Alt+Del (v2.3.6) |
| 226 | systemd-unit-nginx | A stock systemd `.service` unit runs a real daemon: resolution from `/etc/systemd/system`, `Type=forking` → `bgprocess` with the PID from `PIDFile`, `RuntimeDirectory`, `PrivateTmp`, `NoNewPrivileges`, `ExecStop`; `reload` re-reads the unit without touching the daemon |
| 227 | analyze-plot | `slinitctl analyze plot` renders the boot as an SVG timeline that agrees with `analyze time` |
| 228 | cron-persistent | `cron-persistent` catch-up from the on-disk last-run store; jitter and fixed jitter do not break the interval loop |
| 229 | sigpwr-power-hook | `SIGPWR` + `/run/powerstatus` — the sysvinit UPS contract, which only a real PID 1 can prove |
| 230 | path-activation-arm-storm | Several path triggers firing at arm time, during the load, do not stall the loader |
| 231 | follow-pid | `follow-pid` adopts a process slinit did not start, from the pid its owner writes; stopping the service signals it, and a pid file that appears late is waited for |
| 232 | stderr-rotation | `stderr-logfile-max-*` rotates the stderr file on its own policy, independently of the stdout one |
| 233 | restart-on-success | `restart = on-success` keeps a service going while it exits 0 and stops at the first failure (launchd `KeepAlive{SuccessfulExit}`) |
| 234 | standard-input-file | fd 0 reads from a file (launchd `StandardInPath`); a missing file fails the start instead of leaving an empty descriptor |
| 235 | condition-network-online | `condition-network-online` agrees with the machine, in both directions |
| 236 | socket-multicast-group | the multicast membership reaches the kernel, is absent without the directive, and is released on stop |
| 237 | rusage | `slinitctl rusage` in its three scopes, end to end, with the numbers checked rather than only the exit status |
| 238 | socket-accept-inetd | inetd `nowait`: a real TCP connection reaches a per-connection instance with the connected socket on stdio |
| 239 | start-on-mount | a service starts when a filesystem is mounted, which inotify cannot see |
| 240 | skipped-is-visible | a service skipped by an unmet `condition-*` is distinguishable from one that ran, though both read STARTED |
| 241 | control-concurrency-storm | the mutating control verbs from many connections at once; PID 1 must survive |
| 242 | stop-command-completion | a service does not report STOPPED while its stop-command is still running |
| 243 | syslog-dev-log | PID 1 serves `/dev/log` into the journal for syslog(3) clients |
| 244 | manager-securebits | `slinit --securebits` reaches every service through slinit-runner |
| 245 | stop-restart-wait | `stop` and `restart` wait for the outcome, as `start` does |
| 246 | start-when-idle | `start-when-idle` (systemd `Type=idle`) waits for the other starts, at most five seconds |
| 247 | enable-disable-wait | `enable` waits for the start and `disable` for the stop, as dinitctl does; `disable` leaves up what something else requires |
| 248 | read-write-paths | a path carved out of `protect-system = strict` is writable, probed from inside the service |
| 249 | scripted-interrupted-start | a stop cancels a start-interruptible scripted start: the stop command runs and it is not a failure |
| 250 | scripted-timeout-no-stop-timeout | a timed-out scripted start command ignoring SIGINT is still killed with `stop-timeout = 0` |
| 251 | stop-command-run-as | the `stop-command` of a `run-as` service runs as that user |
| 252 | rm-dep-while-waiting | `rm-dep` of the dependency a service waits on lets it start |
| 253 | waits-for-restart-after-dep-stop | a `waits-for` dependency the operator stopped does not block the dependent's auto-restart |
| 254 | pidfile-pid1-refused | a `pid-file` naming PID 1 is refused (a stop used to SIGTERM init) |
| 255 | protect-system-submount | `protect-system = strict` makes a filesystem mounted below `/` read-only too |
| 256 | unload-held-by-monitor | `unload` is refused while slinit-monitor holds the service |
| 257 | name-dotdot-refused | a service name with a `..` path element cannot load a file outside the service directories |
| 258 | infinity-timeouts | `start-timeout`/`stop-timeout = infinity` load and mean no limit |
| 259 | protect-system-keeps-mount-flags | the read-only remount keeps a mount's own `noexec`/`nosuid`/`nodev` |
| 260 | protect-kernel-tunables-sys-ro | `protect-kernel-tunables` makes `/sys` read-only in the service |
| 261 | chroot-non-process-refused | `chroot` on a scripted service is refused at load instead of being ignored |
| 262 | journal-vacuum-keeps-active-file | `slinit-journalctl --vacuum-*` keeps the file slinit-journald opened on an earlier day |
| 263 | journal-flush-keeps-persistent | `--flush` moves the volatile journal in without overwriting the persistent same-day file |
| 264 | tmpfiles-mode-and-symlink | slinit-tmpfiles keeps a mode's sticky bit and does not chmod through a symlink at a `d` path |
| 265 | process-interrupted-start-escalates | an interrupted process start escalates to SIGKILL at stop-timeout; a closed readiness fd brings the process down |
| 266 | seccomp-log-keeps-deny | a call refused by `system-call-filter` stays refused when `system-call-log` lists it too |
| 267 | protect-home-submount | `protect-home = read-only` makes a filesystem mounted below `/home` read-only too |
| 268 | pass-cs-fd-at-boot | `options = pass-cs-fd` works for a boot-started service (`slinitctl --use-passed-cfd`) |
| 269 | chroot-runner | `chroot` + `no-new-privs`: a runner planted inside the jail never runs; the service runs jailed with NoNewPrivs and `working-dir` resolved in the jail |
| 270 | postfork-attrs-first-child | a FIFO service's at-once forked child is FIFO 20 (reset-on-fork = no) and has its `rlimit-nofile`: the runner waits at a start gate until slinit has set them |
| 271 | getty-reclaims-tty | slinit-getty (with `new-session = yes`) takes a stale tty back: root-owned, mode 0620/0600, vhangup survived, tty2 its controlling terminal |
| 272 | runas-aux-commands | `exec-condition`, `cron-command` and `healthcheck-command` of a `run-as = nobody` service run as nobody, not root |
| 273 | seedrng-refuses-symlinks | slinit-seedrng removes planted `seed.*` symlinks without reading or writing through them: a dangling link's target is never created |
| 274 | hook-timeout-kills-group | a `pre-start-command` / `pre-stop-hook` killed by `hook-timeout` takes its backgrounded children with it |
| 275 | ssd-exec-symlink | slinit-start-stop-daemon `--exec` through a symlinked dir (merged /usr) finds the daemon; an `--exec` busybox applet link matches no other busybox process |
| 276 | systemd-exec-comment-continuation | a `#`/`;` comment line inside a backslash-continued `ExecStart=` is skipped for a directly loaded `.service` unit |
| 277 | slinit-mount-bind-flags | an autofs `bind,ro,noexec` unit from slinit-mount gives a read-only, noexec bind (mountinfo, write and exec refused); skips without `autofs4.ko` |
| 285 | boot-missing | no boot service file: PID 1 survives, logs the load failure, parks at the recovery menu with the control socket answering (runner outside the graph, see `rootfs/sbin/init` below) |
| 286 | boot-unparseable | a syntax error in the boot file: the error names `/etc/slinit.d/boot:3`, never "not found"; recovery menu, socket answers |
| 287 | boot-dep-broken | boot's hard dependency does not parse (soft one absent): the error names it; a `start` sent while the menu is up returns instead of hanging |
| 288 | boot-dep-cycle | a cycle reachable from boot (through a `waits-for` edge too) is refused at load as a cyclic dependency |
| 289 | boot-hang | a boot dependency that never becomes ready (no start timeout): boot stays STARTING, `list`/`status`/`boot-time` answer, Ctrl-Alt-Del (SIGINT to PID 1) stops the hung service at once |
| 290 | svcdir-oddities | a FIFO, a dangling symlink, a directory, a 10 MiB one-line file in `/etc/slinit.d`: each start fails at once with the reason in slinit's log; 10 MiB of comments then a description loads |
| 291 | svcdir-missing | `/etc/slinit.d` absent: PID 1 survives at the recovery menu, socket answers, a start reports not found |
| 293 | bootmode-emergency | `emergency`: no service graph, a shell under PID 1, the console says so and how to leave, socket answers |
| 294 | bootmode-single | `single`: rescue mode, as 293 |
| 295 | ro-root | read-only root at boot: /var writers (scripted write, `state-directory`, `logfile`) fail on their own and promptly, a /run writer works, boot completes |
| 296 | hostname-machineid-garbage | garbage `/etc/machine-id` (was a kernel panic) and `/etc/hostname`: boot completes, events are emitted, the hostname is sane |
| 297 | confd-overlay-bad | a `/etc/slinit.conf.d` overlay with a syntax error, or a directory in its place, fails only its service, naming the overlay file and line |
| 298 | kcmdline-t-after-word | `splash -t kcmd-target`: the -t after a stray kernel word is honoured, boot is not started in its place |
| 299 | kcmdline-dash-s-after-word | `splash -s`: boots normally to boot; `splash` is not a service (a -s that disabled the kernel-word filter sent this to the rescue menu) |
| 300 | kcmdline-dash-s-around-word | `-s auto -s`: boots normally to boot; `auto` is not a service |
| 301 | kcmdline-m-after-word | `splash -m kcmd-target`: as dinit, only the words after -m are services |
| 302 | kcmdline-oddities | unknown options, a bad `-F` value, `-s`, stray words, a KEY=VALUE word and a ~1.6 KB line: `-t no-such-svc -t boot` after them are both honoured, boot starts, the KEY=VALUE reaches services |
| 303 | stop-in-restart-delay | `stop` (plain and `--force`) while a crashed service waits out its restart delay stops it for good: no further run, target `stop` |
| 304 | shutdown-unpins-starting | a nested instance's poweroff with a triggered service pinned while STARTING finishes in seconds instead of waiting out the emergency timeout |
| 305 | add-dep-hard-needs-started-target | a hard `add-dep` from a running service to a stopped one is refused and leaves no edge; `waits-for`, and a hard edge to a started service, are still accepted |
| 306 | logbuffer-after-restart | a `log-type = buffer` service restarted while a setsid'd child holds its old stdout: the new run's 100 KB reach catlog, the run does not block |
| 307 | reload-drops-logfile | reloading a stopped service whose file lost `logfile` and `stderr-logfile` stops its output going to the old files |
| 308 | shutdown-time-typo | `reboot 10:3O`, `poweroff +5min`, `halt now-ish` exit 2 and shut nothing down; a valid time still schedules |
| 309 | fifo-does-not-freeze-pid1 | an env-file FIFO and a log-file FIFO with nobody on the other end leave PID 1 answering; the log FIFO fails the start; `/dev/null` is still an env-file |
| 310 | hostname-comment-fqdn | `/etc/hostname` with a comment, a blank line and `node-310.example.com.`: the hostname is the name line, without the trailing dot |
| 311 | ondemand-trigger-limit | an on-demand process that exits without accepting its client: clean exits are held to the trigger window's rate and the service stays up, failed ones stop at the limit of 20 and fail it (v4.0.3: ~4000 launches in 3 s) |
| 312 | pidfile-foreign-pid-refused | a `run-as = nobody` bgprocess whose pid file names a root process: not adopted, and the root process survives the stop; one naming its own daemon is adopted and stopped |
| 313 | pin-leftovers | a start pin is not left on a dependency after `rm-dep` of the edge that carried it, nor on a `depends-ms` dependency pinned while it was starting; a nested instance's poweroff then finishes at once |
| 314 | supervise-daemon-respawn-leak | a crash-looping daemon under `slinit-supervise-daemon` with a `--stdout-logger`: the supervisor's descriptors stay flat and old loggers exit (v4.0.3: +5 descriptors and one stray logger per respawn) |
| 315 | add-dep-prepared-by | `add-dep`/`rm-dep ... prepared-by` succeed and the edge really comes and goes (v4.0.3: always "reply 52") |
| 316 | stop-in-restart-delay-with-dependent | a stop in a restart delay with a `depends-on` dependent STARTING (waiting for the restart): no further run, the dependent is stopped too, a nested poweroff finishes at once |
| 317 | abandoned-restart-with-dependent | a restart parked on its delay because a dependent (waits-for, then depends-on) started while the service was stopping, then called off: a nested poweroff still finishes at once (fails only with both 3766b90 and 5f02f2b removed) |
| 318 | thread-count-flat | a nested instance running 150 processes, 150 with an output-logger and 150 idle on-demand sockets stays on a fixed set of OS threads (HEAD 22; v4.0.3 607) |
| 319 | pin-shutdown-holds | three pin paths that left a service for the shutdown to wait out, each on its own nested instance: a start refused by the dependency's stop pin (c83cad8), a service pinned while STARTING with a dependency still starting (54dd6e5), two failed starts leaving the leaf pinned (9bf0acc) |
| 320 | rm-dep-wakes-stopping-dependency | `rm-dep` of the edge a stopping dependency was waiting on lets it finish stopping; a nested poweroff finishes at once |
| 321 | pidfile-symlink-path | a `run-as = nobody` bgprocess reaching root's own pid file through a symlink in a directory nobody owns: not adopted, the root process survives; a root daemon's pid file in its user's directory (no symlink) is still accepted |
| 322 | cpu-affinity-all-threads | a nested `slinit --user --cpu-affinity 0` in a two-CPU VM: every one of its threads, and every hook it forks, is on CPU 0 (v4.0.3: 9 of 10 threads and 20 of 20 hooks were not) |
| 323 | control-socket-reopen-after-failed-bind | a nested instance whose control socket cannot be bound at start, fixed and SIGUSR1'd: the first connection is answered (v4.0.3: nil-pointer panic) |
| 324 | shared-logger-producer-fd | 30 start/stop cycles of a process and of a scripted shared-logger producer on a nested GOGC=off instance: its descriptors stay flat (v4.0.3: +30 each) |
| 325 | journald-dirty-file-rotated | a binary `slinit-journald` killed with SIGKILL and restarted sets the file left online aside and starts a new one; the directory still reads |
| 326 | stop-pin-spares-reached-milestone | a stop pin refusing a stopping service's restart fails the dependent waiting on it, but spares a `depends-ms` dependent already past its milestone, which goes on to STARTED (v4.0.3 fails on the first, c83cad8; without 5afa5d2 on the second) |
| 327 | user-instance-low-nproc | a `slinit --user` run as nobody under `ulimit -u 20` starts, runs its service and answers (without 0f1c71d the thread reserve alone exceeded the limit and it died at start) |

## How It Works

1. **Build phase**: `build-vm.sh` downloads Alpine Linux minirootfs, cross-compiles
   the slinit binaries (daemon, `slinitctl`, `slinit-check`, `slinit-monitor`,
   `slinit-shutdown`, `slinit-init-maker`, `slinit-nuke`, `rc-service`, `rc-update`,
   `rc-status`) and creates an initramfs with demo services
2. **Per-test boot**: Each test gets its own QEMU VM boot. The test script is
   injected into the initramfs as a service
3. **Guest runner**: `lib/guest-runner.sh` runs inside the VM, waits for slinit
   to be ready, executes the test script, and writes results to a virtio-serial port
   (a second UART on a kernel without virtio built in — see `kernel` below)
4. **Host reader**: `run-tests.sh` reads results from the result channel's Unix socket
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
   - `skip_case "reason"` — ends the case as SKIPPED when the VM cannot
     provide what it needs (no cgroup v2, no `chrt`, no TPM). Use this
     rather than printing a message and returning 0, which reports PASS.
4. Manual assertions: increment `_TESTS_RUN` and `_TESTS_FAILED` directly for
   custom checks (see existing tests for examples)
5. Exit 0 = pass, 77 = skip (what `skip_case` exits with), anything else =
   fail. A case that bails out early on a real failure must return
   non-zero: `test_summary` already returns 1 when something failed, so
   follow it with `return 1`, never `return 0`.

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

A few names inside the `.d/` directory are not services:

- `initd/` goes to `/etc/init.d/`, `systemd/` to `/etc/systemd/system/`.
- `rootfs/` is laid over the guest root as-is, for files a case needs
  outside `/etc` (51 seeds `/var/lib/slinit/clock`).
- `qemu-args` holds extra QEMU options, whitespace-separated, `#` lines
  ignored. 25 asks for `-smp 2` so an affinity mask can be wrong; 51 for
  `-rtc base=2010-01-01T00:00:00` so the clock guard has something to fix.
- `kernel-args` holds extra kernel command-line words, appended to the
  default line, `#` lines ignored. 141 and 180 ask for `psi=1`: the VM
  kernel builds PSI but leaves it off.
- `kernel` names a kernel flavour other than `virt`; only `lts` is built
  (126, for `CONFIG_NUMA`). lts has virtio as modules, so a case on it
  reports over a second UART (`ttyS1`, named on the kernel command line
  as `slinit_test.result=`) instead of virtio-serial. A flavour that was
  not built is reported SKIP with the reason.
- `firmware` is `bios` (the default, SeaBIOS) or `efi` (OVMF; 222).
  Not the default for everything: per case it was 2.4 s faster under
  KVM but 1.7 s slower under TCG, which CI runs.
- `rootfs/sbin/init` replaces the `/sbin/init -> slinit` link with a
  shell script that ends in `exec /sbin/slinit "$@"`. It can break the
  system before slinit sees it (remove or rewrite `/etc/slinit.d`,
  remount `/` read-only, plant a FIFO), and it can start the test runner
  itself, in the background, for a case where nothing in the service
  graph will run: a boot that cannot load, rescue/emergency mode, or a
  shutdown that would stop the test-runner service. 290 and 295 only
  break things; 285-289, 291, 293 and 294 also start the runner.
  Such a runner must `mknod /dev/null` first (a background job's stdin
  is /dev/null and /dev is not mounted yet), redirect inside the
  subshell, and remove `/etc/slinit.d/test-runner` so it does not run
  twice. PID 1's `comm` is then `slinit`, not `init`.
- `system-init` replaces the base one. The cgroup cases (135, 141, 160,
  164, 180, 184, 199, 221) use it to mount cgroup2 and delegate
  controllers at the root, which the base VM does not do.

## Debugging

```bash
# Run with verbose output to see VM console log
VERBOSE=1 ./tests/functional/run-tests.sh tests/functional/cases/05-dependencies.sh

# Check test output files
cat tests/functional/_output/05-dependencies/result.txt
cat tests/functional/_output/05-dependencies/console.log
```
