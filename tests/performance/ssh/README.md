# slinit SSH-driven performance benchmarks

End-to-end latency and throughput measurements against a live slinit
VM over SSH. Same harness shape as [../../acceptance/ssh/](../../acceptance/ssh/)
so operators only need one running VM; each harness ssh's in, drives
`slinitctl`/`slinit-journalctl`, times the round-trip.

Answers the "how does it feel to an operator?" questions the runtime
microbenchmarks can't — network + control-socket + service-machine
end-to-end latency including all the queuing, propagation, and OS
scheduling in between.

## Requirements

- ssh access to a slinit VM (same one used by
  `tests/acceptance/ssh/run.sh`)
- `SSH_HOST`, `SSH_PORT`, `SSH_USER` env vars, or a matching entry
  in `~/.ssh/config`

## Planned harnesses

Not yet implemented — files below are the target shape:

- `ctl-latency.sh` — measure wall-clock for common `slinitctl`
  operations against a live VM: `start`, `stop`, `status`,
  `ls`, `enable`, `disable`. Reports median + p95 over N iterations.
- `journalctl-throughput.sh` — inject a burst of events via
  `logger`, measure `slinit-journalctl -n N` fetch latency, then
  `-f` streaming lag under sustained emit rate.
- `enable-disable.sh` — round-trip `enable`+`disable` through a
  demo service, verifying both symlink lifecycle and target-state
  convergence times.

## Output format

Each harness prints `benchstat`-compatible lines so results
survive commit-to-commit comparison:

    BenchmarkCtlStatus         100   3.1ms ± 4%
    BenchmarkJournalctlFetch    50  22.7ms ± 8%
