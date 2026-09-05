# slinit demo/QEMU performance benchmarks

Cold-boot time and PID-1 memory-footprint measurements from a
QEMU-driven slinit VM. Numbers here answer the same shape of
questions the systemd-alternatives comparison articles publish
("systemd 1.8s / dinit 0.7s / runit 0.4s cold boot") — collected on
comparable hardware so slinit can be plotted alongside.

## Requirements

- `qemu-system-x86_64` (KVM recommended; falls back to software emulation)
- A built `demo/_output/initramfs.cpio.gz` and `vmlinuz-virt`
  (run `./demo/build.sh` from repo root first)

## Harnesses

- **`cold-boot.sh`** — full-boot loop harness. Injects
  `perf-collect` (below) into `demo/services/`, rebuilds the
  initramfs, runs `demo/run.sh --no-monitor` N times headless
  (default 5), greps a `PERF-METRICS` marker line from serial
  stdout, and reports median + p95 for boot time + PID-1
  footprint. Restores the tree on exit via `trap`. Args:
  optional iteration count as `$1`.

  ```
  ./tests/performance/demo/cold-boot.sh 10
  ```

  Prints benchstat-compatible lines suitable for
  `docs/PERFORMANCE.md` and cross-commit diffs.

- **`perf-collect`** — slinit service description (scripted).
  Depends on `all-services` so it fires only after the full boot
  reaches STARTED. Reads `slinitctl boot-time`,
  `/proc/1/status`, and `/sbin/slinit` size; emits ONE marker
  line to serial console; then `slinitctl shutdown poweroff` for
  a clean exit that lets `cold-boot.sh` complete the wall-clock
  cycle. Not part of the default demo boot bundle — only
  injected temporarily by `cold-boot.sh`.

  Marker format:
  ```
  PERF-METRICS boot_ns=<int> kernel_ns=<int> userspace_ns=<int> \
    pid1_rss_kb=<int> pid1_vmpeak_kb=<int> pid1_threads=<int> \
    pid1_fds=<int> slinit_bytes=<int>
  ```

### Planned follow-ons

- `fork-exec-throughput.sh` — bring up N `type=process,
  command=/bin/true` mock services and measure wall-clock to
  reach all-STARTED. Isolates fork-exec cost from the
  ServiceSet microbenchmarks in `../runtime/`.
- `minimal-boot.sh` — same shape as `cold-boot.sh` but strips
  the boot bundle down to a single SSH-alike service, so
  numbers align with the systemd-alternatives comparison
  literature (which measures "single SSH service" cold boot).

## Output format

Each harness prints one line per measurement in
`benchstat`-compatible form so results can be diffed across commits:

    BenchmarkColdBootMinimal    10   980ms ± 5%
    BenchmarkPID1RSSMinimal      1   4.2MiB ± 0%

Results checked into `docs/PERFORMANCE.md` for external publication.
