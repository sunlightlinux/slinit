# slinit Performance Benchmarks

Three-tier benchmark suite. Each tier answers a different question and
runs on different infrastructure — separated so an operator running
just one tier doesn't drag the others along.

## `runtime/` — in-process microbenchmarks

Go `testing.B` benchmarks against the hot paths of slinit's core
libraries: config parser, service-set operations, dependency
resolution, control-protocol wire encoding. No external processes,
no fork/exec, no fs writes beyond `t.TempDir()`. Sub-microsecond
resolution.

Answers: "did this refactor slow the parser?", "does adding a new
directive add allocations?", "how does control-socket encoding scale
with N services?"

Run: `go test -bench=. -benchmem ./tests/performance/runtime/`

See [runtime/README.md](runtime/README.md) for the per-benchmark list.

## `demo/` — QEMU cold-boot + memory footprint

Bash scripts that drive `demo/run.sh` (or a minimal-service variant),
capture `slinitctl boot-time` output, and read `/proc/1/status` for
PID-1 memory footprint. Comparable numbers to the systemd-alternatives
literature ("systemd 1.8s / dinit 0.7s / runit 0.4s cold boot" etc.).

Answers: "how does slinit boot compared to systemd/dinit on the same
hardware?", "what's PID-1 RSS after boot with N services?", "does
tag X regress boot time vs tag Y?"

Requires: `qemu-system-x86_64`. Run from repo root:
`./tests/performance/demo/cold-boot.sh`

Four harnesses shipped:
- `cold-boot.sh` — full-demo boot (34 services, ~3020 ms median)
- `minimal-boot.sh` — single-service boot for parity-comparison with
  dinit / runit / s6 published numbers
- `fork-exec-throughput.sh` — N mock services (default N=50); per-svc
  fork+exec+wait cost
- `pid1-footprint.sh` — PID-1 RSS + VmPeak after N-second idle wait,
  for a steady-state (post-GC-settle) reading

See [demo/README.md](demo/README.md) for the full comparison table
and interpretation.

## `ssh/` — live-VM end-to-end latency

Bash scripts driven against a running VM over SSH (same pattern as
`tests/acceptance/ssh/`). Measures user-visible operation latency:
`slinitctl start`, `slinitctl status`, `slinit-journalctl -n 100`,
enable/disable round-trip. Uses the acceptance harness's VM
provisioning so operators only need one VM running.

Answers: "how fast does `slinitctl start` return after the service
reaches STARTED?", "does `slinit-journalctl -f` add latency to
concurrent emits?", "how does enable/disable round-trip compare to
`systemctl enable`?"

Requires: ssh access to a slinit VM (same env-var contract as
`tests/acceptance/ssh/`: `ACCEPTANCE_HOST` / `_PORT` / `_USER`).

Currently **93 cases** covering the full control-surface hot path —
all slinitctl read/write ops, journal read/write/filter variants,
concurrency scaling from 1 to 128 clients, lifecycle scaling
(1/2/4/8/16/20-way), long-tail latency, reader/writer racing,
fair-share latency under heavy background load, OpenRC compat
shims, specialised feature setup costs (PSI / cgroup / fd-store),
parser stress, and dep-tree scaling.

See [ssh/README.md](ssh/README.md) for the full case list and the
architectural findings extracted from them.

Two cases (`580` + `600`) are gated behind `SLINIT_ALLOW_DISRUPTIVE=1`
and one (`810` socket-activation-on-demand) is a documented SKIP
pending semantics review.
