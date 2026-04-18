# CLAUDE.md

Behavioral guidelines for LLM-assisted work on **slinit** — a Go init system
(dinit-in-Go base + runit + s6-linux-init + OpenRC UX).

> Derived from [forrestchang/andrej-karpathy-skills](https://github.com/forrestchang/andrej-karpathy-skills)
> (MIT), based on [Andrej Karpathy's observations](https://x.com/karpathy/status/2015883857489522876)
> on LLM coding pitfalls. Adapted for slinit: Go idioms, init-system
> correctness, and dinit parity.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial
tasks (typo fixes, obvious one-liners) use judgment — not every change needs
the full rigor.

See [EXAMPLES.md](EXAMPLES.md) for concrete Go examples grounded in slinit
scenarios.

---

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them — don't pick silently.
  slinit has many near-synonyms (`start-timeout` vs `activation-timeout`,
  `wake` vs `start`, `release` vs `stop`, soft-dep vs hard-dep). Surface which
  is meant.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.
- **Check dinit parity first.** For anything that looks like a feature
  request, check whether dinit (`../dinit/src/`) already has the concept.
  If yes, match its semantics unless there's a documented reason to diverge.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios. Trust internal invariants;
  validate only at boundaries (user config, control socket, `/proc`).
- Three similar lines beat a premature generic helper.
- If you write 200 lines and it could be 50, rewrite.

**Go-specific:**
- Prefer `switch` over strategy-pattern struct hierarchies.
- Don't add interfaces for a single implementer. Add them when a second
  concrete type arrives.
- Error wrapping: `fmt.Errorf("op: %w", err)` is usually enough.
  Don't invent typed errors unless callers actually `errors.As` them.
- Don't sprinkle `context.Context` unless cancellation is genuinely needed.

Ask yourself: "Would a senior Go engineer say this is overcomplicated?"
If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently. slinit follows
  standard `gofmt` + `go vet`; don't introduce new idioms mid-file.
- If you notice unrelated dead code, mention it — don't delete it.
- Don't rename identifiers in passing. Renames should be their own commit.

When your changes create orphans:
- Remove imports/vars/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

**slinit-specific:**
- **Don't break dinit protocol/config compatibility** without an explicit
  ask. The control protocol (v4/v5) and config parser accept legacy forms
  on purpose.
- **Don't introduce import cycles.** `pkg/service` cannot import
  `pkg/config` — env-file parsing lives in `pkg/process` for this reason.

The test: every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass."
- "Fix the bug" → "Write a test that reproduces it, then make it pass."
- "Refactor X" → "Ensure `go test ./...` passes before and after."

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

**Verification commands in slinit:**
- `go build ./...` — full build, catches typos fast.
- `go vet ./...` — catches misuse before tests.
- `go test ./...` — ~751 unit tests across 21 packages.
- `go test -race ./pkg/service/... ./pkg/control/...` — concurrency sanity
  check for the state machine & control server.
- `./tests/functional/run-tests.sh` — 52 QEMU-based integration tests
  (requires `qemu-system-x86_64`).
- `go test -fuzz=FuzzConfigParse -fuzztime=30s ./tests/fuzz/` — fuzz a
  single target. 21 targets across 4 files.
- `./slinit-check /etc/slinit.d/<svc>` — offline config linter.

Strong success criteria let you loop independently. Weak criteria
("make it work") require constant clarification.

---

## slinit Project Context

### Positioning
- **dinit-in-Go base** + features from **runit**, **s6-linux-init**,
  **OpenRC**. See memory file `project_positioning.md` for how features
  from the 4 upstreams fit together without creating parallel subsystems.
- Config format: dinit-compatible `key=value`.
- Control CLI: `slinitctl` + OpenRC shims (`rc-service`, `rc-update`,
  `rc-status`).

### Architecture invariants
- Go-idiomatic: goroutines + channels replace dinit's dasynq event loop.
- `Service` interface + `ServiceRecord` embedded struct replace C++
  virtual methods.
- Two-phase state transitions (propagation + execution) preserved
  from dinit.
- ServiceSet is mutex-protected — concurrent control connections are
  normal.
- Config parser accepts both `=` and `:` (`:` for dependency keys).

### Key files (first places to look)
- `pkg/service/record.go` — state machine (~960 LOC core).
- `pkg/service/types.go` — all enum types. When adding a new state or
  flag, this is where it goes.
- `pkg/config/parser.go` — dinit-compatible text grammar.
- `pkg/control/{protocol,server,connection}.go` — binary Unix-socket
  protocol (v5).
- `pkg/process/exec.go` — fork/exec + child monitoring.
- `cmd/slinit/main.go` — PID 1 / container-mode entry point.
- `cmd/slinitctl/main.go` — 35 subcommands + 12 global flags.

### Reference sources
- **dinit** (C++): `../dinit/src/` — key files `service.{h,cc}`,
  `service-constants.h`, `load-service.h`. When in doubt about
  semantics, check dinit first.
- **runit**, **s6**, **OpenRC**: consulted for specific features;
  the base model stays dinit.

### Commit style
- **Bundle, don't over-split.** One commit per session of work, even
  across features. Project history uses bundled commits (e.g. one
  commit covering 6 related features). Don't `git restore` + re-edit
  to split overlapping files.
- Commit messages: `area: short summary` + a paragraph on *why*, not
  *what* (the diff already shows what).
