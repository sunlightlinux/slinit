# slinit SSH-driven performance benchmarks

End-to-end latency measurements against a live slinit VM over SSH.
Each case is a small shell script that runs REMOTELY on the target,
times its own operations with `date +%s%N`, and prints a
`benchstat`-compatible summary line. Cases isolate slinit's own
control-socket / IPC / journal-read latencies — not the SSH
round-trip.

Same env-var contract as [../../acceptance/ssh/](../../acceptance/ssh/),
and `lib/ssh.sh` is a symlink into the acceptance harness, so the
same running VM serves both suites.

## Usage

```
VERBOSE=1 ACCEPTANCE_HOST=ceres.ionutnechita.ro \
    ACCEPTANCE_PORT=40003 ACCEPTANCE_USER=root \
    ./tests/performance/ssh/run.sh
```

- `ACCEPTANCE_HOST` / `ACCEPTANCE_PORT` / `ACCEPTANCE_USER` — required
- `ACCEPTANCE_SSH_KEY` — optional explicit identity file
- `VERBOSE=1` — echo the SSH invocation per case
- `ITERS=N` — iterations per case (default 30)
- `KEEP_REMOTE=1` — leave `/tmp/slinit-perf.$$` on target for inspection

Run a subset by passing case paths:

```
./run.sh cases/30-ctl-status.sh cases/50-journalctl-fetch-n100.sh
```

## Cases (v2.2.6)

Numbered with zero-padding so lexical sort matches numeric order.

| Case | Measures |
|------|----------|
| `010-ctl-version`              | `slinitctl --version` — CLI fork/exec baseline (no socket) |
| `020-ctl-ls`                   | `slinitctl ls` — socket connect + N-service enumerate |
| `030-ctl-status`               | `slinitctl status boot` — dep-tree walk on aggregate |
| `040-ctl-boot-time`            | `slinitctl boot-time` — server-side aggregation + render |
| `050-journalctl-fetch-n100`    | `slinit-journalctl -n 100` — journal decode + format |
| `060-parallel-status-8`        | 8 concurrent `slinitctl status` per iteration |
| `061-parallel-status-32`       | 32 concurrent — per-client cost vs `060` reveals lock contention |
| `062-parallel-status-128`      | 128 concurrent — knee detector for accept-loop / fd-limit |
| `070-ctl-status-every-svc`     | Status on every service from `slinitctl ls` — worst-per-service |
| `080-journal-fetch-scaling`    | Fetch N ∈ {10, 100, 1000, 5000} — decode-path scaling |
| `090-journal-burst-write`      | Burst 200 `logger` writes, then fetch tag-filtered — write-path |
| `100-ctl-hammer-throughput`    | 200 sequential status calls — per-op amortised throughput |
| `110-journal-fetch-during-write` | Fetch racing 500-write emitter — reader/writer contention |
| `120-mixed-workload`           | Interleaved status/ls/boot-time/journalctl 40 ops — operator sim |
| `130-drift-detection`          | 10 batches × 50 status — per-batch drift detector |
| `140-rss-delta-under-load`     | PID-1 RSS before/after 2500 status + 500 journalctl |
| `141-rss-scaling`              | RSS curve at 500 / 2500 / 10000 / 12500 ops — leak vs arena |
| `150-long-tail-latency`        | 500 status — p50/p95/p99/max distribution |
| `160-journal-scan-vs-size`     | Fetch(N=100) before + after 5k-pump — reveals full-scan bugs |
| `170-ls-long-tail`             | 500x `slinitctl ls` — p50/p95/p99/max for enumeration path |
| `180-boot-time-long-tail`      | 500x `slinitctl boot-time` — heavier aggregation code path |
| `190-slinit-check-offline`     | `slinit-check /etc/slinit.d/boot` — offline parser latency |
| `200-journalctl-output-json`   | short-vs-json format — encode cost |
| `210-journalctl-tag-filter`    | tag-filtered vs unfiltered — scan-time push-down check |
| `220-journalctl-priority-filter` | `-p err` vs unfiltered — filter-push-down check |
| `230-parallel-journalctl-8`    | 8 concurrent journalctl fetches — reader-side scaling |
| `240-mixed-heavy-load`         | 16 status + 8 journalctl + 4 boot-time parallel — peak admin sim |
| `250-fair-share-latency`       | 1 light client's p99 while 4 heavy fetches run — no-starvation check |
| `260-ls-drift-detection`       | 10 batches × 50 ls — enumeration-path drift |
| `270-slinitctl-graph`          | Full dep graph DOT export |
| `280-slinitctl-dependents`     | Reverse-dep query for one svc |
| `290-slinitctl-getallenv-global` | Global env dump |
| `300-list-v5-vs-list`          | Protocol v5 vs v7 for list — marshalling cost |
| `310-status-v5-vs-status`      | Protocol v5 vs v7 for status — marshalling cost |
| `320-slinitctl-catlog`         | Read service's buffered stdout |
| `330-slinitctl-service-dirs`   | Configured svc-dir list (floor check) |
| `340-slinitctl-platform`       | Platform detect via /proc + /sys |
| `350-slinitctl-signal-safe`    | Send SIGWINCH (ignored) — signal path |
| `360-slinitctl-reload`         | Re-read single svc config from disk |
| `370-slinitctl-reload-all`     | Bulk reload every loaded svc |
| `380-slinitctl-setenv-cycle`   | Set + unset per-svc env var — mutation path |
| `390-slinitctl-getallenv-svc`  | Per-svc env dump (vs 290's global) |
| `400-slinitctl-is-started`     | Minimal-payload up-check probe |
| `410-slinitctl-load-mech`      | Loader mechanism info — rare code path floor |
| `420-slinitctl-list-actions`   | Empty extra-actions list per svc |
| `430-slinitctl-add-rm-dep-cycle` | Runtime graph mutation (add + remove dep) |
| `440-parallel-different-svcs`  | 8 parallel status on 8 DIFFERENT svcs — cross-svc mutex check |
| `450-slinitctl-is-failed-negative` | Negative exit-code path (is-failed on running svc) |
| `460-throwaway-lifecycle`      | Full lifecycle: write file → start → stop → unload → rm |
| `470-concurrent-setenv-8`      | 8 parallel setenv on same svc, different keys — env mutex |
| `480-slinitctl-signal-hup-safe` | SIGHUP delivery (svc reloads on HUP but stays up) |
| `490-catlog-repeat-drift`      | 10×50 catlog batches — read-path drift (SKIPs if unsupported) |
| `500-status-during-reload-all` | Light status p99 while reload-all hammers — mutex-hold check |
| `510-journalctl-long-tail`     | 300x journalctl fetch — p50/p95/p99/max |
| `520-journalctl-compound-filter` | tag+priority AND vs each alone vs unfiltered |
| `530-throwaway-restart-cycle`  | Provision + 5 restarts per iter — restart cost |
| `540-throwaway-pause-continue` | pause+continue cycle on `type=process` throwaway |
| `550-provision-30-svcs-list`   | 30 sequential provisions — list cost at N=43 |
| `560-deep-dep-chain`           | 10-deep chain — status walk on tip |
| `570-boot-time-under-writes`   | boot-time under 200-logger write pressure |
| `580-parallel-lifecycle-4`     | **DISRUPTIVE** — crashes slinit PID 1 on v2.2.6, gated |
| `590-graph-before-after-provision` | Graph render at N=13 vs N=43 |
| `600-status-under-massive-lifecycle` | **DISRUPTIVE** — 20 concurrent lifecycles, gated |

### Disruptive cases (opt-in only)

Cases marked **DISRUPTIVE** are known to crash slinit as PID 1 on
the versions where they surfaced the bug. They stay in-tree so
they can validate the fix, but skip themselves by default unless
`SLINIT_ALLOW_DISRUPTIVE=1` is set in the case's environment.
Never run against a production target without a recovery plan
(console access + power-cycle capability).

Currently gated:
- **`580-parallel-lifecycle-4`** — 4 concurrent throwaway
  provision+start+stop+unload lifecycles panic slinit PID 1
  on v2.2.6.
- **`600-status-under-massive-lifecycle`** — same class, 20
  concurrent lifecycles fan out 5× harder than 580.

Root cause not yet identified — the parallel add/remove of
loaded services through the state machine appears to race
somewhere. Bug tracked as follow-up in [slinit issue tracker].

All cases are **read-only** or write to the journal (which is
designed to absorb high write volume). No case starts/stops real
services. Start/stop throughput cases should ship as separate
scripts that provision a throwaway `type=oneshot` service at setup
and remove it at teardown.

### Naming variables inside cases

POSIX sh has no `local` keyword. `perf_run_iters` in
`lib/remote-prelude.sh` uses variables prefixed with `_prf_` to
avoid clobbering case-body variables. A case that defines helper
functions using common names like `_i`, `_n`, `_cmd` is fine as
long as those don't collide with `_prf_*`. When in doubt,
prefix your case's inner variables with a short case-specific
tag (e.g. `_bw_` for burst-write).

## Output format

Bench-style lines with median + p95 + min in milliseconds:

    BenchmarkCtlVersion                 20  median=   1.017 ms  p95=   1.199 ms  min=   0.910 ms
    BenchmarkCtlLs                      20  median=   1.214 ms  p95=   1.735 ms  min=   1.046 ms

First measurement on ceres (real-hardware x86_64, v2.2.6, `ITERS=15`):

```
BenchmarkCtlVersion                 15  median=   1.057 ms
BenchmarkCtlLs                      15  median=   1.079 ms
BenchmarkCtlStatus_boot             15  median=   1.081 ms
BenchmarkCtlBootTime                15  median=   1.106 ms
BenchmarkJournalFetchN100           15  median=   1.739 ms
BenchmarkParallelStatus8            15  median=   2.131 ms  (0.266 ms/client)
BenchmarkParallelStatus32           15  median=   5.676 ms  (0.177 ms/client)
BenchmarkParallelStatus128          15  median=  21.124 ms  (0.165 ms/client)
BenchmarkCtlStatusAllSvc            15  median=  10.882 ms  (0.84 ms/svc, 13 svcs)
BenchmarkJournalFetchN10            15  median=   1.244 ms
BenchmarkJournalFetchN100           15  median=   1.568 ms
BenchmarkJournalFetchN1000          15  median=   4.170 ms
BenchmarkJournalFetchN5000          15  median=   6.385 ms
BenchmarkJournalBurstWriteFetch_200 15  median= 138.652 ms  (~0.7 ms/logger call)
BenchmarkCtlHammer_200x_boot        15  median= 172.487 ms  (0.86 ms/op)
BenchmarkJournalFetchUnderWrite_500 15  median=  96.237 ms
BenchmarkMixedOperatorWorkload_40   15  median=  36.721 ms  (0.92 ms/op)
```

Findings:

- **Slinit is CLI-fork/exec-bound, not IPC-bound.** `CtlVersion`
  at 1.057 ms is the noise floor; every socket op is within
  20–700 μs of it. The Go binary's startup dominates.
- **Concurrent scaling is flat.** 128 clients cost 0.165 ms each,
  the same as 32 clients (0.177 ms) — no mutex contention or
  accept-loop knee within operator-realistic fan-out.
- **Journal read is sublinear in N.** Per-record cost DROPS from
  N=100 to N=5000 — the fixed overhead per fetch amortises well.
- **Reader/writer coexistence is fine.** Fetch racing 500-writer
  emitter (`110`) is 96 ms — dominated by the writer's 500 fork/
  exec cost, not journal contention.
- **No drift over 500-op sequences.** Per-op cost across 10 batches
  of 50 status calls stays flat (0.82–0.97 ms), no monotonic
  slowdown.
- **Long-tail latency is tight.** p50 / p95 / p99 / max for 500
  sequential status ops = 1.16 / 1.78 / 1.98 / 2.09 ms — max
  within 2× p50, no GC pause spikes visible.
- **Journal fetch doesn't full-scan.** Fetch(N=100) cost is
  UNCHANGED after emitting 5000 additional entries (actually −9%
  from cache warmup) — reader seeks to tail correctly.
- **Journal filters push down to scan.** `-t tag` is 26% FASTER
  than unfiltered same-N (filter reduces records to decode);
  `-p err` is 50% FASTER (priority filter scans less). Both are
  implemented correctly at scan time, not post-decode.
- **JSON output is cheap.** `--output=json` adds only +8.5% vs
  the default short format for N=500 — encoder is not the
  hot path.
- **Concurrent readers scale.** 8 parallel journalctl fetches
  cost 0.41 ms each (vs 1.6 ms serial) — reader path is
  parallel-friendly, same as the control server.
- **Fair scheduling holds.** A light `slinitctl status` loop's
  p99 (1.94 ms) under sustained heavy `-n 5000` journal reads
  is essentially identical to the isolated baseline (1.98 ms) —
  slinit does not starve short queries under big-request load.
- **Aggregation is no slower than enumeration.** `boot-time`
  long-tail max = 1.96 ms, `ls` long-tail max = 2.01 ms — server-
  side start-timing aggregation costs nothing extra vs the
  cheap-copy list path.
- **Offline parser is free.** `slinit-check` median 1.07 ms
  matches CLI baseline exactly.
- **Bulk operations dramatically cheaper than N calls.**
  `reload-all` reloads every loaded service in 1.30 ms — only
  +14% over reloading a single service (1.11 ms) and 10× cheaper
  than the N-serial-reload alternative (13 × 1.1 = 14 ms). Scripts
  polling for state should prefer bulk endpoints where they
  exist — the fork/exec + connect cost dwarfs the actual server
  work.
- **v5 and v7 protocols cost the same.** `list` vs `list5`
  (1.133 vs 1.103 ms) and `status` vs `status5` (1.171 vs
  1.214 ms) show no meaningful marshalling-overhead gap between
  the two protocol variants — the extra fields v5 carries are
  encoded efficiently.
- **Signal delivery is essentially free.** `slinitctl signal
  WINCH <svc>` (1.080 ms) is within 20 μs of CtlVersion — the
  control-socket + kill(2) path adds nothing measurable.
- **Graph export + reverse-dep queries are lean.** Full DOT
  graph export = 1.53 ms; `dependents <svc>` = 1.13 ms. Even
  though both walk the full dep set, the render/filter work
  costs at most 500 μs on top of the baseline.
- **Full service lifecycle is sub-4 ms.** Write file to
  `/etc/slinit.d/` → `slinitctl start` → `stop` → `unload` → rm
  runs in 3.89 ms median. Provisioning + tearing down throwaway
  services in perf harnesses costs essentially nothing.
- **Cross-service concurrency has no mutex serialisation.** 8
  parallel status on 8 DIFFERENT services (`440`) cost 2.20 ms
  — matching 8 parallel status on the SAME service (`060` =
  2.13 ms). If cross-svc reads serialised on a global mutex,
  we'd see the different-svc case slower; the numbers match
  within jitter.
- **Reload-all doesn't stall reads.** Light `slinitctl status`
  under sustained `reload-all` hammer: p99 = 1.99 ms, max =
  2.00 ms — essentially identical to isolated baseline.
  reload-all's loader mutex is held per-service, not across
  the whole batch.
- **Runtime graph mutation is cheap.** `add-dep` +
  immediate `rm-dep` cycle = 2.50 ms (~1.25 ms per op), only
  ~150 μs over baseline per operation despite touching the
  service graph.
- **is-failed adds ~32% over is-started.** 1.45 ms vs 1.11 ms
  — the negative-check path is materially heavier. Worth a
  look if scripts poll this in tight loops; likely the CLI's
  exit-code translation not taking a shortcut on the fast
  path.

**Observed but not (yet) a defect**: RSS grows slowly under
sustained load. 12500 status ops added +556 kB to PID-1 RSS,
with a rate that decreases-then-upticks (80 → 44 → 25 → 96 kB
per 1000 ops). Not a linear leak (would grow much faster) — most
likely Go runtime heap arena growth batched with GC cycles. Rate
amortises to ~44 bytes/op, projecting ~1 MB/day at 1 op/sec — not
critical for an init but worth `pprof` follow-up before making
strong long-term-stability claims. Thread count stays flat (25),
so no goroutine leak into OS-thread scale-up.

## Adding a case

Create `cases/NN-name.sh` — one line if the operation is
non-parameterised:

```sh
# NN-name — one-line description of what this measures.
perf_run_iters "$ITERS" "SomeLabel" "some-command --args"
```

`perf_run_iters` is provided by `lib/remote-prelude.sh` (loaded by
`run.sh` before every case). It runs the command N times, records
each wall-clock delta via `date +%s%N`, prints median + p95 + min.
The command's own stdout/stderr are discarded so tty flushes don't
skew the timing.

## Planned follow-ons

- Start/stop throughput case using a `perf-throwaway` service.
- `enable`/`disable` round-trip (creates + removes a symlink under
  `all-services.d/`).
- `slinit-journalctl -f` streaming lag under sustained emit rate
  (needs a `logger`-driven producer + timestamped consumer).
- Concurrent-client scaling: N parallel `slinitctl status` from
  different SSH sessions, measure per-request p95 vs N.
