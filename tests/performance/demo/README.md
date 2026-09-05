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

## Planned harnesses

Not yet implemented — files below are the target shape:

- `cold-boot.sh` — repeat `demo/run.sh` N times (with a minimal-service
  variant of the boot bundle), capture `slinitctl boot-time` output,
  report median + p95. Args: `--services=minimal|full`, `--iterations=10`.
- `pid1-footprint.sh` — after `boot` is STARTED, ssh in and cat
  `/proc/1/status` → extract VmRSS, VmPeak, Threads, FDSize. Also
  captures `ls -la /sbin/slinit` for on-disk binary size.
- `fork-exec-throughput.sh` — bring up N `type=process,
  command=/bin/true` mock services and measure wall-clock to reach
  all-STARTED. Compares against the ServiceSet microbenchmarks in
  `../runtime/` to separate fork-exec cost from state-machine cost.

## Output format

Each harness prints one line per measurement in
`benchstat`-compatible form so results can be diffed across commits:

    BenchmarkColdBootMinimal    10   980ms ± 5%
    BenchmarkPID1RSSMinimal      1   4.2MiB ± 0%

Results checked into `docs/PERFORMANCE.md` for external publication.
