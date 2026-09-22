# Container-mode tests

slinit as PID 1 of a real container (`slinit -o`), driven from the host
through the container runtime's CLI.

The other suites do not cover this. The functional suite boots slinit as
PID 1 of a VM, and acceptance case 69 runs `slinit -o` as a child of a
shell. So nothing exercised the combination a Docker or Podman user
actually gets: container mode *and* PID 1 of a PID namespace. The first
run of this suite found two bugs that only show up there (see below).

## Usage

```bash
./tests/container/run.sh                     # all cases (~1 min)
./tests/container/run.sh cases/06-*.sh       # some cases
VERBOSE=1 ./tests/container/run.sh           # print every check, not only failures

./tests/container/soak.sh                    # 100 spawn/shutdown cycles (~5 min)
CYCLES=500 ./tests/container/soak.sh

KEEP_IMAGE=1 ...                             # reuse the image from the last run
```

Requirements: Go, and a working `docker` the user may talk to. The image
is `busybox:1.37-musl` plus statically built `slinit`, `slinitctl` and
`slinit-runner`, tagged `slinit-container-test:local` and rebuilt from
the working tree on every run unless `KEEP_IMAGE=1`.

`CONTAINER_RUNTIME=podman` is accepted, because the commands used are
CLI-compatible, but **only Docker has been run**. Kubernetes is not
covered. A pod whose container runs `slinit -o` meets the same PID 1
contract these cases pin, but nothing here starts one.

## What the cases pin

| Case | Contract |
|------|----------|
| 01-boot-and-stop | slinit is PID 1; `docker stop` exits 0 well inside the 10s grace period; results dir says `h` |
| 02-exit-code | the workload's exit code becomes the container's, including a workload that exits instantly |
| 03-signal-exit-code | a workload killed by signal N exits 128+N |
| 04-load-failure | a broken service tree exits 1 at once, with the reason in `docker logs` |
| 05-zombie-reaping | orphans reparented to PID 1 are reaped |
| 06-signals | SIGINT, SIGRTMIN+3/+4/+5 each exit 0 with haltcode `h`/`h`/`p`/`r` |
| 07-stop-timeout | a service ignoring SIGTERM is killed by slinit after its stop-timeout, not by the runtime |
| 08-escalation | a third SIGTERM forces the exit (acceptance 69, as real PID 1) |
| 09-control-socket | `docker exec … slinitctl start/stop/restart` work, with exit codes |
| 10-read-only-rootfs | boots and stops with only `/run` and `/tmp` writable |
| 11-shutdown-from-inside | `slinitctl shutdown halt/poweroff/reboot/softreboot` each exit 0 with their halt code |
| 12-restart-after-kill | a `restart = yes` service killed from outside comes back; the container stays up |
| 13-boot-target-lost | when nothing keeps the boot target up, the container ends with the reason logged |
| 14-runtime-log-visibility | `docker logs` keeps receiving events after boot, not only during it |
| 15-not-pid1 | works as PID 2 under `docker run --init` |
| 16-pause-unpause | survives `docker pause` without restarting services or wrong timeouts |
| 17-memory-limit | a service that hits `--memory` dies alone: slinit and the other services stay up |
| 18-pids-limit | PID 1 survives `--pids-limit` exhaustion and still stops cleanly |
| 19-process-tree-cleanup | stopping a service takes its background children and grandchildren with it |
| 20-sighup-noop | SIGHUP is claimed but acted on by nobody |
| 21-nginx-workload | nginx under slinit serving real HTTP through a published port, its access log visible in the container log, stopped on command |

`soak.sh` runs the lifecycle over and over against a tree with a
worker, an orphan generator and a SIGTERM-ignoring service. It rotates
the stop method between `docker stop`, SIGINT and SIGRTMIN+4. A cycle
fails on a 10s readiness timeout, a non-zero exit, a runtime SIGKILL
(137), or a stop slower than `STOP_BUDGET_MS` (5000). It prints
p50/p95/max for readiness and stop time, and keeps the logs of failed
cycles in `_build/soak/`.

## Getting an application's own logs out of a container

slinit discards a service's output unless told otherwise: the default
`log-type` is `none`, as it is in dinit. In a container that surprises,
because images log to stdout and expect the runtime to collect it —
nginx's access log is a symlink to `/dev/stdout`, and it ended up in
`/dev/null`.

Give the service slinit's own stdout:

```
options = runs-on-console
```

There is no `/dev/console` in a container without a tty, so the service
falls back to slinit's stdout, which is the runtime's log stream. Case
21 pins this. `logfile = /dev/stdout` does not work — opening it fails
with ELOOP — and `log-type = buffer` keeps the output inside the
container, readable with `slinitctl catlog`.

## Bugs this suite found

- **An instantly-exiting boot service hung slinit forever.** The service
  set created its inactive-notification channel lazily, when the event
  loop first asked for it, so a service that died before `Run` started
  had its notification dropped. Not container-specific: as bare-metal
  PID 1, the same boot never reached the recovery menu. Regression
  test: `pkg/eventloop/early_inactive_test.go`, plus case 02's
  `exit 7`.
- **Exit messages were lost.** `os.Exit` skips the deferred catch-all
  `Stop()`, so whatever was still in the catch-all pipe never reached
  the console. A container with a missing service exited 1 with nothing
  in `docker logs` but startup warnings. Case 04 checks for the reason.
- **`docker logs` went silent once boot finished.** slinit mutes the
  catch-all's console tee at boot-ready, because on real hardware that
  console is a serial line shared with getty. A container has neither,
  and `/run/slinit/catch-all.log` dies with the container, so everything
  after boot — a crash loop, a restart limit, the reason for an exit —
  was written nowhere an operator could reach. Not muted in container
  mode any more. Regression test: case 14, which fails on the old code.
