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
7. Commit with a clear message
8. Push to your fork and open a Pull Request

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

- **Unit tests**: `go test ./...` (~1956 tests across 69 Go dirs, 273 _test.go files)
- **Functional tests**: `./tests/functional/run-tests.sh` (218 QEMU-based cases)
- **Acceptance tests**: `./tests/acceptance/ssh/run.sh` (218 SSH-driven cases against a live VM)
- **Fuzz targets**: `go test -fuzz=FuzzConfigParse ./tests/fuzz` (21 targets across 4 files)
- Requires `qemu-system-x86_64` for functional tests

### Project Structure

- `cmd/` - Entry points (35 binaries total; run `ls cmd/` for the live
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
  `slinit-logouthookd`, `slinit-svc-value`, `slinit-shell-var`,
  `slinit-einfo`, `slinit-fstabinfo`, `slinit-mountinfo`,
  `slinit-start-stop-daemon`, `slinit-supervise-daemon`,
  `slinit-init-maker`, `slinit-resource`).
- `pkg/` - Core packages: `autofs`, `bootmode`, `catalog`, `checkpath`,
  `config`, `control`, `dissect`, `einfo`, `eventloop`, `features`,
  `fstab`, `journal`, `journalbin`, `journald`, `logging`, `mounts`,
  `pathwatch`, `persist`, `platform`, `process`, `recovery`, `rng`,
  `seccomp`, `service`, `shutdown`, `snapshot`, `svcdirwatch`, `utmp`,
  `watchdog` (29 total; run `ls pkg/` for the live list).
- `internal/util/` - Path and parsing utilities
- `completions/` - Shell completions (bash, zsh, fish)
- `tests/functional/` - QEMU integration tests (218 cases)
- `tests/acceptance/ssh/` - SSH-driven live-VM cases (218)
- `tests/fuzz/` - Fuzz targets (21)
- `tests/performance/` - Go benchmarks
- `demo/` - QEMU demo environment
- `doc/man/` - pandoc-flavored markdown → roff via `go tool md2man`

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
