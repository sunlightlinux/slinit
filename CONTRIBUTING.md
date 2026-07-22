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
6. Commit with a clear message
7. Push to your fork and open a Pull Request

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

- **Unit tests**: `go test ./...` (~1640 tests across ~40 packages)
- **Functional tests**: `./tests/functional/run-tests.sh` (166 QEMU-based tests)
- **Acceptance tests**: `./tests/acceptance/ssh/run.sh` (169 SSH-driven cases against a live VM)
- **Fuzz targets**: `go test -fuzz=FuzzConfigParse ./tests/fuzz` (21 targets across 4 files)
- Requires `qemu-system-x86_64` for functional tests

### Project Structure

- `cmd/` - Entry points (slinit, slinitctl, slinit-runner, slinit-check,
  slinit-monitor, slinit-shutdown, slinit-init-maker, slinit-nuke,
  slinit-mount, slinit-checkpath, slinit-seedrng, slinit-cgtop,
  slinit-logouthookd, slinit-sysusers, slinit-tmpfiles, slinit-binfmt,
  slinit-sysctl, slinit-svc-value, slinit-shell-var, slinit-einfo,
  slinit-fstabinfo, slinit-mountinfo, slinit-start-stop-daemon,
  slinit-supervise-daemon, slinit-resource, plus OpenRC shims:
  rc-service, rc-update, rc-status)
- `pkg/` - Core packages (service, config, control, process, shutdown,
  eventloop, logging, seccomp, pathwatch, svcdirwatch, utmp, autofs,
  checkpath, platform, snapshot, watchdog, einfo, fstab, mounts,
  persist, rng)
- `internal/util/` - Path and parsing utilities
- `completions/` - Shell completions (bash, zsh, fish)
- `tests/functional/` - QEMU integration tests (166 cases)
- `tests/acceptance/ssh/` - SSH-driven live-VM cases (169)
- `tests/fuzz/` - Fuzz targets (21)
- `tests/performance/` - Go benchmarks
- `demo/` - QEMU demo environment
- `doc/man/` - pandoc-flavored markdown → roff via `go tool md2man`

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
