# Functional Tests

Automated QEMU-based integration tests for slinit running as PID 1.

Each test boots a minimal Alpine Linux VM with slinit as init, runs a test
script inside the guest via a virtio-serial channel, and validates the output.

## Usage

```bash
# Run all tests (40 tests)
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

**Note**: Some runit-inspired features (pre-stop-hook, control-command, chroot,
new-session, lock-file, close-fds, log rotation, log filtering, log processor)
are validated via unit tests. Additional functional test cases can be added for
integration validation.

## How It Works

1. **Build phase**: `build-vm.sh` downloads Alpine Linux minirootfs, cross-compiles
   slinit + slinitctl + slinit-check + slinit-monitor, and creates an initramfs
   with demo services
2. **Per-test boot**: Each test gets its own QEMU VM boot. The test script is
   injected into the initramfs as a service
3. **Guest runner**: `lib/guest-runner.sh` runs inside the VM, waits for slinit
   to be ready, executes the test script, and writes results to a virtio-serial port
4. **Host reader**: `run-tests.sh` reads results from the virtio-serial Unix socket
   and reports PASS/FAIL

## Adding Tests

1. Create `tests/functional/cases/NN-name.sh`
2. Use assertion helpers from `lib/assert.sh`:
   - `assert_contains "$output" "expected" "description"`
   - `assert_service_state "name" "STATE" "description"`
   - `wait_for_service "name" "STATE" timeout_secs`
   - `test_summary` (call at end of test)
3. Exit 0 = pass, non-zero = fail

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
