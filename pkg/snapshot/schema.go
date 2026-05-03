// Package snapshot serializes and restores the operator-visible
// "intent" of a running slinit daemon — which services were explicitly
// activated, pinned, or triggered, and what global env vars were set.
//
// The use case is **soft-reboot of slinit itself**: when an operator
// upgrades the slinit binary on a long-running box (telco / appliance /
// no-reboot deployments) and runs `slinitctl shutdown soft-reboot`, the
// new daemon should come up with the same set of services running — not
// fall back to the boot graph and lose every ad-hoc `slinitctl start`.
//
// # Scope (Phase A)
//
// This package preserves *intent*, not running processes. After restore
// the daemon will re-call BringUp on every service that was activated;
// ProcessService children get a fresh fork+exec. PID re-attach (so an
// existing child survives the slinit re-exec) is **not** in scope here.
//
// # Forward-compat with PID re-attach (Phase B)
//
// The schema is JSON with named fields, so adding `pid` / `exec_stage`
// later is a pure additive change: snapshots written by Phase A simply
// have those fields absent (zero values), and a Phase B reader treats
// them as "no live PID, fall back to fresh start" — i.e. exactly what
// Phase A does today. Likewise an old daemon reading a Phase B snapshot
// ignores unknown fields. Format `Version` is bumped only for breaking
// changes.
package snapshot

// CurrentVersion is the schema version written by this build.
//
// Bump only for changes that an older reader cannot ignore (renamed
// field, dropped field, semantics change). Adding a new optional field
// is backwards-compatible and does not require a bump — that is the
// whole point of the JSON-with-named-fields format.
const CurrentVersion = 1

// Snapshot is the on-disk root document.
type Snapshot struct {
	// Version is CurrentVersion at the time of writing. A reader that
	// sees a higher version than it knows about should refuse to load
	// the file rather than guess at the new shape.
	Version int `json:"version"`

	// WrittenAt is an RFC 3339 timestamp set by Capture, useful for
	// diagnostics ("how stale is this snapshot?"). Not used for any
	// correctness decision.
	WrittenAt string `json:"written_at,omitempty"`

	// Services records per-service intent. Order is not significant;
	// restore looks up services by name.
	Services []ServiceSnapshot `json:"services,omitempty"`

	// GlobalEnv preserves environment variables set via
	// `slinitctl setenv-global`. Entries are stored as KEY=VALUE so the
	// format matches what slinit already passes around internally.
	GlobalEnv []string `json:"global_env,omitempty"`
}

// ServiceSnapshot captures the intent for a single service.
//
// All boolean fields default to false on absence, so old snapshots that
// never set (say) PinnedTarget round-trip cleanly through a newer
// reader. The zero value of ServiceSnapshot is "service exists by name
// but no special state to restore" — useful as a baseline.
type ServiceSnapshot struct {
	// Name matches the slinit service name. Required; entries with
	// empty Name are dropped on restore with a warning.
	Name string `json:"name"`

	// Activated mirrors ServiceRecord.IsMarkedActive() — the service
	// was explicitly started by the operator (or by an upstream
	// activation chain), as opposed to being pulled up only as a
	// dependency. On restore, Activated services get a fresh Start().
	Activated bool `json:"activated,omitempty"`

	// PinnedStart and PinnedStop mirror IsStartPinned/IsStopPinned.
	// Mutually exclusive in normal use; if both end up set on a read
	// the restore prefers PinnedStop (safer: don't auto-start a
	// service the operator pinned down).
	PinnedStart bool `json:"pinned_start,omitempty"`
	PinnedStop  bool `json:"pinned_stop,omitempty"`

	// Triggered captures the trigger latch on TriggeredService. Set
	// independently of Activated: an armed-but-not-yet-fired trigger
	// is a meaningful state the operator may have configured.
	Triggered bool `json:"triggered,omitempty"`

	// --- Reserved for Phase B (PID re-attach). Do not populate from
	// Phase A capture; readers ignore them when zero. ---
	//
	// PID       int `json:"pid,omitempty"`
	// ExecStage int `json:"exec_stage,omitempty"`
}
