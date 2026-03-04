# Functional Tests

Automated QEMU-based integration tests for slinit running as PID 1.

Each test boots a minimal Alpine Linux VM with slinit as init, runs a test
script inside the guest via a virtio-serial channel, and validates the output.

## Usage

```bash
# Run all tests
./tests/functional/run-tests.sh

# Run a single test
./tests/functional/run-tests.sh tests/functional/cases/01-boot-starts.sh

# Keep VM artifacts for debugging
KEEP_BUILD=1 ./tests/functional/run-tests.sh
```

## Requirements

- Go 1.22+
- `qemu-system-x86_64`
- `curl`, `cpio`, `gzip`
- KVM recommended (falls back to software emulation)

## Adding Tests

Create a new `tests/functional/cases/NN-name.sh` script. Each test script
runs inside the guest as a shell script. Use the `assert_*` helpers from
`lib/assert.sh` (sourced automatically). Exit 0 = pass, non-zero = fail.
