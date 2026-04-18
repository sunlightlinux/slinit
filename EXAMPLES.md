# EXAMPLES.md

Concrete Go examples for each of the four principles in [CLAUDE.md](CLAUDE.md),
grounded in real slinit scenarios (config parser, state machine, control
protocol, service manager).

> Derived from [forrestchang/andrej-karpathy-skills](https://github.com/forrestchang/andrej-karpathy-skills)
> (MIT). Examples rewritten for Go + slinit.

---

## 1. Think Before Coding

### Example 1 — Ambiguous Feature Request

**User request:** "Add a timeout for service startup."

**What LLMs often do (wrong assumption):**

```go
// Silently invents a new config key + struct field.
type ProcessService struct {
    ServiceRecord
    StartupTimeout time.Duration
}

func (p *ProcessService) BringUp() {
    timer := time.AfterFunc(p.StartupTimeout, func() {
        p.failedToStart(ReasonTimeout)
    })
    // ...
}
```

**Problem:** slinit already has **three** timeout-shaped concepts:

- `start-timeout` — how long exec-wait before declaring start-failure
  (ProcessService).
- `activation-timeout` — how long for a `ready-notification` fd to fire.
- `stop-timeout` — how long after SIGTERM before escalating to SIGKILL.

Picking one silently will either duplicate an existing knob or change
its semantics.

**What should happen (surface the choice):**

> slinit already has `start-timeout`, `activation-timeout`, and
> `stop-timeout` (all parsed in `pkg/config/parser.go`). Which semantic
> do you mean?
>
> 1. Exec-wait cap on the initial fork+exec (`start-timeout`, already
>    present for ProcessService, missing for BGProcessService).
> 2. Cap on a service reporting *ready* via `ready-notification`
>    (`activation-timeout`, already present).
> 3. A new knob I haven't spotted?
>
> Also: should this apply to scripted + bgprocess, or only process?

### Example 2 — Multiple Interpretations

**User request:** "Make service restarts faster."

**What LLMs often do (pick silently):**

```go
// Halves the restart-delay default without asking.
const defaultRestartDelay = 100 * time.Millisecond // was 200ms
```

**What should happen:**

> "Faster restarts" could mean any of:
>
> 1. **Lower `restart-delay`** — currently 200ms default, aimed at avoiding
>    thrash on crash-looping services. Lowering it makes recovery faster
>    but risks pegging a core on a broken service.
> 2. **Parallelize the stop → start transition** — the state machine
>    currently serializes these through `ServiceSet.ProcessQueues`.
>    Meaningful change; bigger blast radius.
> 3. **Raise `--parallel-start-limit`** — affects many services starting
>    after a crash, not a single service restart.
>
> Which one matters for your case? A crashed system-critical service,
> or boot-storm recovery?

---

## 2. Simplicity First

### Example 1 — Over-abstraction

**User request:** "Add a helper to calculate the next fire time for a cron
service."

**What LLMs often do (overengineered):**

```go
type ScheduleStrategy interface {
    Next(now time.Time) time.Time
}

type IntervalSchedule struct{ Interval time.Duration }

func (s IntervalSchedule) Next(now time.Time) time.Time {
    return now.Add(s.Interval)
}

type DelayedSchedule struct {
    Inner ScheduleStrategy
    Delay time.Duration
}

func (s DelayedSchedule) Next(now time.Time) time.Time {
    return s.Inner.Next(now).Add(s.Delay)
}

type ScheduleFactory struct{}

func (ScheduleFactory) Build(cfg CronConfig) ScheduleStrategy { /* ... */ }
```

**Problem:** slinit has exactly **one** scheduler (fixed interval with
optional initial delay). No second implementation exists. The interface
+ factory + decorator are all speculative.

**What should happen (minimum code):**

```go
// nextFire returns the next scheduled time. Non-zero delay applies only
// on the first tick.
func (cr *CronRunner) nextFire(now time.Time, first bool) time.Time {
    if first && cr.delay > 0 {
        return now.Add(cr.delay)
    }
    return now.Add(cr.interval)
}
```

Seven lines, zero interfaces. Add a strategy when a second scheduler shows
up, not before.

### Example 2 — Speculative Features

**User request:** "Read `!import FOO BAR` from env-files."

**What LLMs often do (feature creep):**

```go
type EnvFileReader struct {
    Path          string
    Cache         map[string]map[string]string // path -> parsed result
    Validator     func(k, v string) error
    Observers     []func(path string, changes map[string]string)
    AllowCycles   bool // follow !include recursively
    StrictMode    bool
}

func (r *EnvFileReader) Read() (map[string]string, error) {
    // 200 lines with caching, change notification, cycle detection...
}
```

**Problem:** the ask was one meta-command. Caching, observers, cycle
detection, validators — nobody asked for any of it. slinit's actual
`pkg/process/envfile.go` is 148 lines total and handles all four
meta-commands (`!clear`, `!unset`, `!import`, `!include`).

**What should happen:**

Extend the existing switch in `ReadEnvFileWithOrigEnv`:

```go
case strings.HasPrefix(line, "!import"):
    for _, name := range strings.Fields(line[len("!import"):]) {
        if v, ok := orig[name]; ok {
            env[name] = v
        }
    }
```

Five lines. Matches dinit. No new types.

---

## 3. Surgical Changes

### Example 1 — Drive-by "Improvements"

**User request:** "Fix the bug where an empty `command =` line panics the
parser."

**What LLMs often do (too much):**

```diff
 func (p *Parser) parseCommand(key, value string) error {
-    cmd := strings.Fields(value)
-    p.svc.Command = cmd[0]
-    p.svc.Args = cmd[1:]
+    // Split the command, respecting quotes for paths with spaces.
+    cmd, err := shellquote.Split(value)
+    if err != nil {
+        return fmt.Errorf("command: invalid shell syntax: %w", err)
+    }
+    if len(cmd) == 0 {
+        return fmt.Errorf("command: empty")
+    }
+    p.svc.Command = cmd[0]
+    p.svc.Args = cmd[1:]
+    // Also validate the binary exists (saves time later).
+    if _, err := exec.LookPath(p.svc.Command); err != nil {
+        p.logger.Warnf("command %q not found in PATH", p.svc.Command)
+    }
     return nil
 }
```

**Problems:**
- Pulled in an external dependency for quoting (not asked).
- Added PATH lookup (not asked; `slinit-check` already does this).
- Changed error semantics (silently accepted → hard error).
- Fix-the-panic is one line; the diff is twelve.

**What should happen (surgical):**

```diff
 func (p *Parser) parseCommand(key, value string) error {
     cmd := strings.Fields(value)
+    if len(cmd) == 0 {
+        return fmt.Errorf("%s: empty command", key)
+    }
     p.svc.Command = cmd[0]
     p.svc.Args = cmd[1:]
     return nil
 }
```

Three lines. Every one traces to the panic.

### Example 2 — Style Drift

**User request:** "Log the reason when a service is skipped due to
platform keywords."

**What LLMs often do (reformat everything):**

```diff
- func (s *ServiceSet) Load(name string) (*ServiceRecord, error) {
-     cfg, err := s.loader.Load(name)
+ // Load reads the service config from disk and returns a ServiceRecord.
+ // If the service declares platform keywords that match the detected
+ // platform, Load returns ErrSkipped.
+ func (s *ServiceSet) Load(name string) (*ServiceRecord, error) {
+     cfg, err := s.loader.Load(name)
      if err != nil {
-         return nil, err
+         return nil, fmt.Errorf("load %q: %w", name, err)
      }
-     if platform.ShouldSkip(cfg.Keywords, s.platform) {
-         return nil, nil
+     if platform.ShouldSkip(cfg.Keywords, s.platform) {
+         s.logger.Infof("skipping %s: keyword matches platform %s",
+             name, s.platform)
+         return nil, ErrSkipped
      }
      // ...
 }
```

**Problems:**
- Added a docstring (not asked).
- Wrapped an error that other call sites may rely on being bare
  (API change, not asked).
- Replaced `nil, nil` with `nil, ErrSkipped` — semantic change that
  will break every caller.

**What should happen:**

```diff
     if platform.ShouldSkip(cfg.Keywords, s.platform) {
+        s.logger.Infof("skipping %s: keyword matches platform %s",
+            name, s.platform)
         return nil, nil
     }
```

Two lines. Nothing else changes.

---

## 4. Goal-Driven Execution

### Example 1 — Reproduce, Then Fix

**User request:** "The control server sometimes deadlocks when a client
disconnects mid-command."

**What LLMs often do (fix without reproducing):**

```go
// Adds a defer-recover + extra mutex-unlock on a hunch.
func (c *Connection) handle() {
    defer func() {
        if r := recover(); r != nil {
            c.logger.Errorf("panic: %v", r)
        }
    }()
    c.mu.Unlock() // "just in case"
    // ...
}
```

Result: the deadlock still happens, now with a `recover` hiding the
symptom.

**What should happen (test-first):**

```
Plan:
1. Write a test in pkg/control that:
   - starts a server
   - opens a connection
   - sends a partial command (e.g. type byte + length prefix, no payload)
   - closes the socket mid-read
   - asserts the server goroutine exits within 500ms
   Verify: test fails (reproduces the deadlock via `go test -run ... -race`).

2. Read connection.go handleCommand — find the blocking read that
   doesn't respect ctx or conn.SetReadDeadline.
   Verify: identify the exact line + why it blocks forever.

3. Fix: use SetReadDeadline OR check err == io.EOF before decoding.
   Verify: test now passes, 10 runs clean under -race.

4. Run full control suite with race detector.
   Verify: no new failures.
```

Each step has an independent pass/fail. No speculative "safety" code.

### Example 2 — Multi-Step Feature with Verification

**User request:** "Add a `slinitctl graph <svc>` command that prints the
dependency tree."

**What LLMs often do (all at once):**

300-line commit: new protocol message, server handler, CLI subcommand,
ASCII-art renderer, color support, JSON output format, max-depth flag —
with no intermediate verification.

**What should happen (incremental):**

```
Plan:
1. Protocol: add QUERYDEPS (reuses existing HANDLE mechanism).
   Verify:
   - pkg/control unit test: encode/decode round-trip.
   - go test ./pkg/control -race.

2. Server: implement handler that walks the dep graph.
   Verify:
   - Unit test against a fixture ServiceSet with 3 services, 2 deps.
   - Manually: slinit + `echo | nc -U /run/slinit/control`.

3. CLI: add `slinitctl graph <svc>` subcommand with plain-text output.
   Verify:
   - Integration test (tests/functional/NN-graph.sh): boot VM, load
     3-svc fixture, assert output matches fixture.
   - Completions updated (bash/zsh/fish).

4. Only after steps 1-3 green: add indentation/unicode tree rendering.
   Verify: golden-file test for two fixtures.
```

Skip step 4 if no one asks for it. Ship 1-3 as a usable feature.

---

## Anti-patterns Summary

| Principle | Anti-pattern | Fix |
|---|---|---|
| Think Before Coding | Silently invents a new `xxx-timeout` key | List the three existing timeouts; ask which |
| Simplicity First | Interface + factory for a single scheduler | One function, `switch` on cron field |
| Surgical Changes | Reformats comments, wraps errors, adds doc lines while fixing a panic | Add only the `len(cmd) == 0` check |
| Goal-Driven | `defer recover()` on a hunch for a deadlock | Write a failing test that reproduces it, fix the actual blocking read |

## Key Insight

The "overcomplicated" examples aren't obviously wrong — they follow patterns
that look professional. The problem is **timing**: they add complexity
before it's needed, which:

- Makes code harder to audit (init-system correctness matters).
- Widens blast radius for bugs.
- Increases surface area for `-race` failures.
- Breaks dinit parity (implicit contract for config + protocol users).

The "simple" versions are easier to understand, faster to write, easier
to test, and can be refactored later — once a second concrete requirement
actually appears.

**Good slinit code is code that solves today's problem while preserving
dinit semantics, not tomorrow's problem prematurely.**
