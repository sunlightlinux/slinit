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

- **Unit tests**: `go test ./...` (~751 tests across 21 packages)
- **Functional tests**: `./tests/functional/run-tests.sh` (52 QEMU-based tests)
- **Fuzz targets**: `go test -fuzz=FuzzParseConfig ./tests/fuzz` (18 targets across 4 files)
- Requires `qemu-system-x86_64` for functional tests

### Project Structure

- `cmd/` - Entry points (slinit, slinitctl, slinit-check, slinit-monitor,
  slinit-shutdown, slinit-init-maker, slinit-nuke, slinit-mount, plus
  OpenRC shims: rc-service, rc-update, rc-status)
- `pkg/` - Core packages (service, config, control, shutdown, process,
  eventloop, logging, utmp, autofs, checkpath, platform)
- `internal/util/` - Path and parsing utilities
- `completions/` - Shell completions (bash, zsh, fish)
- `tests/functional/` - QEMU integration tests
- `tests/fuzz/` - Fuzz targets
- `demo/` - QEMU demo environment

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
