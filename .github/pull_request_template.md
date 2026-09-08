## Description

Brief description of the changes.

## Type of Change

- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update

## Testing

- [ ] Unit tests pass (`go test ./...` — ~2033 tests)
- [ ] Race check clean (`go test -race ./pkg/service/... ./pkg/control/...`)
- [ ] Functional tests pass (`./tests/functional/run-tests.sh` — 218 QEMU cases; skip if no local qemu)
- [ ] Acceptance tests pass against a VM (`./tests/acceptance/ssh/run.sh` — 219 SSH cases; skip if no live target)
- [ ] Fuzz targets exercised for touched parsers (`go test -fuzz=Fuzz... -fuzztime=30s ./tests/fuzz/`)
- [ ] New tests added for new functionality (unit + at least one of functional/acceptance for user-facing changes)

## Checklist

- [ ] Code follows Go conventions (`gofmt`, `go vet`, no import cycles — see CONTRIBUTING.md)
- [ ] Changes are focused and minimal — every changed line traces to the described feature/fix
- [ ] Documentation updated if needed (README.md, doc/man/*.md, CHANGELOG.md's `[Unreleased]` section)
- [ ] Dinit parity considered — for anything on the state-machine / config / control-protocol surface, checked upstream (`../dinit/src/`) first
- [ ] Signed off (`git commit -s`) — DCO enforced
- [ ] No security vulnerabilities introduced (see SECURITY.md for reporting)
