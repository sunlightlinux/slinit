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

All four harnesses share `_lib.sh` (block extraction, `/proc`
parsing, median/p95 math, summary emit, `perf-collect` service
generator). Each harness owns only its own service-tree wiring
(what boot depends on, whether `perf-collect` sleeps before the
dump, etc.); driver + parsing is identical.

- **`cold-boot.sh`** — full-boot loop harness. Injects
  `perf-collect` (below) into `demo/services/`, appends
  `waits-for: perf-collect` to `demo/services/boot` so the
  aggregate holds STARTED until measurement finishes, rebuilds
  the initramfs, runs `demo/run.sh --no-monitor` N times
  headless (default 5), extracts a raw-data block from serial
  stdout, and reports median + p95. Restores the demo tree on
  exit via `trap`.

  ```
  ./tests/performance/demo/cold-boot.sh 10
  ```

  Prints benchstat-compatible lines:

  ```
  BenchmarkColdBoot_Demo           N   <median>ms (p95 <p95>ms)
  BenchmarkPID1RSS_Demo            N   <median>kB
  BenchmarkPID1VmPeak_Demo         N   <median>kB
  BenchmarkSlinitBinarySize        1   <size>kB (<size>bytes)
  ```

- **`perf-collect`** — generated on the fly by
  `_lib.sh:perf_write_collector` (no standalone file). scripted
  slinit service that dumps `/proc/uptime`, `/proc/1/status`,
  `ls /proc/1/fd | wc -l`, `stat` of `/sbin/slinit` to
  `/tmp/perf.out`, then `cat`s the whole block to `/dev/console`
  in one write (minimises interleave with slinit's
  `[OK]/[STOPPD]` output). Finishes with `slinitctl shutdown
  poweroff` so QEMU exits cleanly. Depends-on line is
  parameterised per harness (`all-services` / `system-init` /
  `mock-all`); optional pre-dump `sleep N` for steady-state
  measurement in `pid1-footprint.sh`.

  Block format the driver parses (raw data, no formatting):

  ```
  PERF-BEGIN
  UPTIME:
  <kernel_seconds> <idle_seconds>
  STATUS-BEGIN
  <verbatim /proc/1/status>
  STATUS-END
  FDS:
  <int>
  SLINIT_BYTES:
  <int>
  PERF-END
  ```

  Kept minimalist because slinit's config parser has no `\` line
  continuation, no backslash escapes in `command =`, and
  pre-expands `$VAR` at parse time — so `printf "\n"`, `awk
  '{print $2}'`, `sed 's/\(...\)/\1/'` all get eaten. Dumping
  raw contents and parsing in bash sidesteps every one of
  those.

- **`minimal-boot.sh`** — single-service cold-boot benchmark.
  Same shape as `cold-boot.sh` but temporarily replaces
  `demo/services/boot` with a stripped variant that depends only
  on `system-init` (mount /proc + /sys + cgroup v2) + waits on
  the injected `perf-collect`. Every other file under
  `demo/services/` stays on disk but is unreferenced by boot's
  deps, so slinit never starts it.

  Directly comparable to the systemd-alternatives comparison
  literature ("systemd 1.8s, dinit 0.7s, runit 0.4s single-SSH
  cold boot"), since the boot bundle is one-service-equivalent.

  ```
  ./tests/performance/demo/minimal-boot.sh 10
  ```

- **`fork-exec-throughput.sh`** — generates N minimal
  `type=scripted, command=/bin/true` services (default N=50)
  and a `mock-all` aggregate; measures total wall-clock to
  reach `mock-all` STARTED; subtracts the minimal-boot baseline
  (~800ms) to isolate per-service fork+exec+wait cost. Reports
  per-service microseconds and pure svc/s throughput. Args:
  `[NUM_SVCS] [ITERATIONS]` (defaults 50, 5).

  ```
  ./tests/performance/demo/fork-exec-throughput.sh 200 5
  ```

  Isolates the kernel-scheduler + slinit-SIGCHLD-reaper +
  state-machine cost from the ServiceSet microbenchmarks in
  `../runtime/`, which measure the state-machine alone with
  no exec.

- **`pid1-footprint.sh`** — minimal boot, but `perf-collect`
  sleeps IDLE_SEC before the status dump so Go's GC settles and
  transient startup allocations get released. Reports
  steady-state RSS + VmPeak (compare with `cold-boot.sh`'s
  boot-STARTED-moment RSS to see how much was transient). Args:
  `[IDLE_SEC] [ITERATIONS]` (defaults 10, 3).

  ```
  ./tests/performance/demo/pid1-footprint.sh 10 5
  ```

## Output format

Each harness prints one line per measurement in a
`benchstat`-compatible shape so results survive commit-to-commit
diffs and can be pasted straight into `docs/PERFORMANCE.md`. A
sample from a laptop-class KVM host (Linux 7.1 lowlatency,
x86_64):

    === Demo summary (n=5/5) ===
    BenchmarkColdBoot_Demo             5   3020.0 ms (p95 3050.0 ms)
    BenchmarkPID1RSS_Demo              5   9228 kB
    BenchmarkPID1VmPeak_Demo           5   1263476 kB
    BenchmarkSlinitBinarySize          1   5164 kB (5288098 bytes)

    === Minimal summary (n=5/5) ===
    BenchmarkColdBoot_Minimal          5   800.0 ms (p95 810.0 ms)
    BenchmarkPID1RSS_Minimal           5   6636 kB
    BenchmarkPID1VmPeak_Minimal        5   1263220 kB

    === fork-exec-throughput summary (n=5/5, N=200) ===
    BenchmarkForkExec_TotalBoot_N200    5   915.0 ms
    BenchmarkForkExec_PerSvc_N200        5   575 us (over 800 ms baseline)
    BenchmarkForkExec_PureThroughput_N200 5   1739 svc/s

    === pid1-footprint summary (n=5/5, idle=10s) ===
    BenchmarkPID1RSS_Steady            5   6644 kB
    BenchmarkPID1VmPeak_Steady         5   1263220 kB

Numbers here are noise-limited by `/proc/uptime` jiffies (10ms
resolution at HZ=100). Boot times below ~50ms are dominated by
kernel init; use `fork-exec-throughput.sh` with large N to see
sub-jiffie per-service cost through averaging.
