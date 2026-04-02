# slinit Performance Benchmarks

Go benchmarks measuring core slinit subsystem performance.

## Running

```bash
# All benchmarks
go test -bench=. -benchmem ./tests/performance/

# Specific subsystem
go test -bench=BenchmarkParse -benchmem ./tests/performance/
go test -bench=BenchmarkServiceSet -benchmem ./tests/performance/
go test -bench=BenchmarkDependency -benchmem ./tests/performance/
go test -bench=BenchmarkControl -benchmem ./tests/performance/

# With custom duration
go test -bench=. -benchmem -benchtime=2s ./tests/performance/

# Save baseline for comparison
go test -bench=. -benchmem -count=5 ./tests/performance/ > baseline.txt

# Compare two runs (requires benchstat)
benchstat baseline.txt new.txt
```

## Benchmarks

### Config Parsing (`parse_bench_test.go`)

- **ParseService** — single service file with common directives
- **ParseServiceComplex** — service with 20+ directives (deps, rlimits, affinity)
- **ParseBatch** — N service files from disk (10/50/100/500)
- **ParseCPUAffinity** — cpu-affinity spec parsing (single/range/mixed)

### Service Engine (`service_bench_test.go`)

- **ServiceSetAdd** — adding N services to a ServiceSet (10/100/500/1000)
- **ServiceSetFind** — name lookup in sets of varying size
- **DependencyChain** — start propagation through a linear dep chain (depth 5-50)
- **DependencyFanOut** — start propagation with N parallel deps (5-100)
- **ServiceLoad** — end-to-end DirLoader with N services + dep resolution
- **ProcessQueues** — queue drain with N pending services

### Control Protocol (`control_bench_test.go`)

- **ControlRoundTrip** — list-services command latency (5/20/100 services)
- **ControlServiceStatus** — per-service status query via handle
- **WireEncoding** — raw packet write + handle encoding overhead

### Log Rotation (potential)

- **LogRotatorWrite** — line filtering + file write throughput
- **LogRotatorRotate** — rotation + old file cleanup latency
- **LogLineFilter** — regex include/exclude pattern matching overhead
