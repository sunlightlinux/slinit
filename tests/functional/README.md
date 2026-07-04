# Functional Tests

Automated QEMU-based integration tests for slinit running as PID 1.

Each test boots a minimal Alpine Linux VM with slinit as init, runs a test
script inside the guest via a virtio-serial channel, and validates the output.

## Usage

```bash
# Run all tests (158 tests)
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

- Go 1.22+
- `qemu-system-x86_64`
- `curl`, `cpio`, `gzip`
- `socat` or `nc` (for virtio-serial result reading)
- KVM recommended (falls back to software emulation)

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
