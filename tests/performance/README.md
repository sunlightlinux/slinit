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

Not yet populated — plan: `cold-boot.sh`, `pid1-footprint.sh`,
`fork-exec-throughput.sh`.

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

Requires: ssh access to a slinit VM. Not yet populated — plan:
`ctl-latency.sh`, `journalctl-throughput.sh`, `enable-disable.sh`.
