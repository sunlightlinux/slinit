# Contributing to slinit

Thank you for your interest in contributing to slinit!

## How to Contribute

### Reporting Issues

- Use the GitHub issue tracker to report bugs
- Include steps to reproduce, expected behavior, and actual behavior
- Include slinit version, OS, and Go version

### Submitting Changes

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-change`)
3. Make your changes
4. Run tests: `go test ./...`
5. Run functional tests if applicable: `./tests/functional/run-tests.sh`
6. Add a `[Unreleased]` entry to [CHANGELOG.md](CHANGELOG.md) if the
   change is user-visible (new feature, behavioural fix, security
   hardening). Internal refactors don't need one.
   If the change touches a surface listed in [STABILITY.md](STABILITY.md),
   check that it is allowed in the release it is aimed at.
7. Commit with a clear message
8. Push to your fork and open a Pull Request

### Proposing a Feature

Check first whether one of slinit's upstreams already has it — dinit
(the base: state machine, description format, `slinitctl` verbs), then
systemd, runit, s6-linux-init, OpenRC, upstart, finit, launchd, immortal
or sysvinit. If one does, match its name and semantics, and say where it
comes from; admins moving in keep their muscle memory, and the
behaviour is already worked out. A feature with no upstream analogue
can still go in, but has to justify the surface it adds. The feature
request template asks for the same.

A feature targets the next minor (4.1 and later). The 4.0 long-term-
support line takes fixes only, so a pull request for it should be a bug
or security fix — see [STABILITY.md](STABILITY.md#long-term-support).

### Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep changes focused and minimal
- Add tests for new functionality
- Update documentation if needed

### Development Setup

```bash
git clone https://github.com/sunlightlinux/slinit.git
cd slinit
go build ./...
go test ./...
```

### Testing

- **Unit tests**: `go test ./...` (~2900 tests across 101 Go dirs, 494 _test.go files)
- **Functional tests**: `./tests/functional/run-tests.sh` (332 QEMU-based cases). `SLINIT_NO_KVM=1` forces software emulation, which is how CI runs them — reach for it when a case passes here and fails on the runner.
- **Acceptance tests**: `./tests/acceptance/ssh/run.sh` (218 SSH-driven cases against a live VM)
- **Performance harnesses**: `./tests/performance/ssh/run.sh` (93 SSH-driven perf cases) + `./tests/performance/demo/{cold-boot,minimal-boot,fork-exec-throughput,pid1-footprint,softreboot-cycle}.sh` (QEMU boot benchmarks) + `./tests/performance/runtime/` (Go microbenchmarks)
- **Fuzz targets**: 54 repo-wide — 29 under `tests/fuzz` (`go test -fuzz=FuzzConfigParse ./tests/fuzz`), 25 beside the code they exercise (`go test -fuzz=FuzzStateMachine ./pkg/service`, the runit, openrc, sysvinit and immortal converters, the sysusers/tmpfiles/killall5 parsers, `pkg/timedate`, `pkg/hostname`, `pkg/journal`, `pkg/process`, `pkg/config`, the `pkg/control` server and its shutdown-time parser, the PID-1 kernel command-line scan in `cmd/slinit`, journalctl's rendering of sender-chosen fields). CI fuzzes each package listed in `.github/workflows/tests.yml`; `go test ./tools/stats` fails if one with a target is missing. `grep -rl '^func Fuzz' --include='*_test.go' .` finds them all
- **Container**: `./tests/container/run.sh` (34 cases with slinit as real PID 1 under Docker or `CONTAINER_RUNTIME=podman`) + `./tests/container/soak.sh` (spawn+shutdown loop; `SOAK_BUDGET_SEC` bounds it by wall time)
- **Kubernetes**: `./tests/k8s/run.sh` (10 cases against a local `kind` cluster)
- Requires `qemu-system-x86_64` for functional tests, Docker for the container suite, `kind` + `kubectl` for the Kubernetes one
- A functional case whose precondition is missing calls `skip_case` and is counted as SKIPPED, not passed (since v2.7.1) — read all three numbers, since roughly one case in ten skips for want of cgroup v2, `chrt`, a machine-id, a TPM or NUMA (see `tests/functional/README.md`)

### Project Structure

- `cmd/` - Entry points (51 directories: 50 Go binaries plus the
  `slinit-resource` shell agent; run `ls cmd/` for the live
  list). Highlights: `slinit` (PID 1), `slinitctl` (control CLI),
  `slinit-runner` (post-fork hardening wrapper), `slinit-check` (config
  linter), `slinit-monitor` / `slinit-shutdown`, `slinit-journalctl` /
  `slinit-journald` / `slinit-journal-migrate` (journal pipeline),
  `slinit-supports` (self-introspection), `slinit-runit-convert` /
  `slinit-openrc-convert` / `slinit-systemd-convert` (migration
  converters), plus OpenRC shims (`rc-service`, `rc-update`,
  `rc-status`) and helper binaries (`slinit-tmpfiles`,
  `slinit-sysusers`, `slinit-binfmt`, `slinit-sysctl`, `slinit-nuke`,
  `slinit-mount`, `slinit-checkpath`, `slinit-seedrng`, `slinit-cgtop`,
  `slinit-notify`,
  `slinit-logouthookd`, `slinit-svc-value`, `slinit-shell-var`,
  `slinit-einfo`, `slinit-fstabinfo`, `slinit-mountinfo`,
  `slinit-start-stop-daemon`, `slinit-supervise-daemon`,
  `slinit-init-maker`, `slinit-resource`), and the D-Bus settings
  daemons with their CLIs (`slinit-timedated` / `slinit-timedatectl`,
  `slinit-hostnamed` / `slinit-hostnamectl`, `slinit-localed` /
  `slinit-localectl`).
- `pkg/` - Core packages: `autofs`, `bootmode`, `catalog`, `checkpath`,
  `config`, `control`, `dbusservice`, `dinitwire`, `dissect`, `einfo`,
  `eventloop`, `features`, `fstab`, `hooks`, `hostname`, `journal`,
  `journalbin`, `journald`, `locale`, `logging`, `machine`, `metrics`,
  `mounts`, `mountwatch`, `network`, `pathwatch`, `persist`,
  `platform`, `polkit`, `process`, `recovery`, `rng`, `rusage`,
  `seccomp`, `service`, `shutdown`, `snapshot`, `svcdirwatch`,
  `switchroot`, `timedate`, `utmp`, `watchdog` (42 total; run
  `ls pkg/` for the live list).
- `internal/` - `util` (path and parsing utilities), and test helpers:
  `dbustest` (a private dbus-daemon), `iouringtest`
- `completions/` - Shell completions (bash, zsh, fish)
- `tests/functional/` - QEMU integration tests (332 cases)
- `tests/acceptance/ssh/` - SSH-driven live-VM cases (218)
- `tests/performance/` - performance harnesses (93 SSH + 5 QEMU harnesses + runtime microbenchmarks)
- `tests/fuzz/` - Fuzz targets (27)
- `tests/container/` - slinit as PID 1 under Docker or Podman (34 cases + soak loop)
- `tests/k8s/` - the same image as a pod on a `kind` cluster (10 cases)
- `demo/` - QEMU demo environment
- `doc/man/` - pandoc-flavored markdown → roff via `go tool md2man`

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
