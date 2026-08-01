// Package features is slinit's self-introspection surface. It answers
// "does slinit support X?" — for X = a service directive, control-
// protocol opcode, or CLI option — and where the feature originated
// (dinit / systemd / runit / s6 / OpenRC / slinit-native).
//
// The design is deliberately hybrid:
//
//   - **Canonical list** — auto-discovered from source via go/ast
//     (see discover.go). Whatever pkg/config/parser.go and
//     pkg/control/protocol.go actually accept IS the truth. Drift
//     between "docs say we support this" and "code accepts this" is
//     structurally impossible.
//
//   - **Provenance annotation** — hand-curated in provenance.go, a
//     one-row-per-feature table mapping every discovered name to
//     {Source, Category, DocURL, Notes}. Cannot be inferred from
//     code alone — knowing that `restart=` came from dinit while
//     `Restart=` from systemd requires human judgment.
//
// The two sides are reconciled by Merge (merge.go). A CI test fails
// if either side has an entry the other doesn't, forcing developers
// to annotate as they add code — no silent drift, no stale docs.
//
// Consumers:
//   - cmd/slinit-supports — operator-facing CLI (yes/no + provenance)
//   - doc/features.md — regenerated from `slinit-supports --format=markdown`
//   - future tooling: config linters, CI validators, migration
//     assistants that need machine-readable feature data
package features

// Kind categorizes what sort of feature an entry represents. Kept
// small on purpose — three top-level kinds cover everything a user
// might ask about; sub-categorisation goes into Category.
type Kind string

const (
	KindDirective Kind = "directive" // service-description key (parsed by pkg/config/parser.go)
	KindOpcode    Kind = "opcode"    // control-protocol wire byte (defined in pkg/control/protocol.go)
	KindOption    Kind = "option"    // value under `options:` (per-service boolean flag)
)

// Source identifies where the feature originated. Not "which package
// implements it" — that's Category. Source answers "if I read
// documentation from project X, will I find this feature?".
type Source string

const (
	SourceDinit       Source = "dinit"        // dinit-wire-compat or lifted directly from dinit-service.5
	SourceSystemd     Source = "systemd"      // parity with systemd [Service]/[Unit] directive semantics
	SourceRunit       Source = "runit"        // runit / runsv / svlogd family
	SourceS6          Source = "s6"           // s6 / s6-log / s6-rc family
	SourceOpenRC      Source = "openrc"       // OpenRC init.d / conf.d / einfo family
	SourceUpstart     Source = "upstart"      // Upstart-derived (normal-exit, reload-signal, .override, script sugar)
	SourceSlinit      Source = "slinit"       // slinit-native (no upstream analog)
)

// Category groups features by internal subsystem so operators can
// browse related knobs together. Independent of Source — a systemd-
// parity feature and a dinit-native one may both live under "cgroup".
type Category string

const (
	CatServiceConfig   Category = "service-config"   // service description keys (command, args, workdir, env)
	CatLifecycle       Category = "lifecycle"        // restart, stop-timeout, ready-notification, activation
	CatDependency      Category = "dependency"       // depends-on, waits-for, before/after, chain
	CatLogging         Category = "logging"          // log-type, log-file, log-buffer, forwarders, journal
	CatCgroup          Category = "cgroup"           // memory-max, cpu-quota, PSI pressure, freezer
	CatSandbox         Category = "sandbox"          // seccomp, capabilities, no-new-privs, mount namespaces
	CatEnv             Category = "env"              // env-file, setenv, exported vars (DINIT_SERVICE etc)
	CatControlProto    Category = "control-protocol" // opcodes only
	CatShutdown        Category = "shutdown"         // reboot, poweroff, softreboot, kexec, wall notices
	CatObservability   Category = "observability"    // slinitctl status/list/graph, journal query
	CatSocket          Category = "socket-activation" // sd-socket, systemd sockets, listen-*
	CatOpenRCCompat    Category = "openrc-compat"    // rc-service / rc-update / rc-status / einfo / conf.d
)

// Feature is one row in the annotated feature surface. All strings
// UTF-8, no embedded newlines except in Notes (which may span for
// readability in the markdown output).
type Feature struct {
	Name     string   `json:"name"`
	Kind     Kind     `json:"kind"`
	Source   Source   `json:"source"`
	Category Category `json:"category"`
	// DocURL is the canonical documentation link. For dinit-derived
	// features, dinit's manpage; for systemd, systemd.service(5); for
	// slinit-native, our own doc/*.md. Empty when no external
	// authority exists yet.
	DocURL string `json:"doc_url,omitempty"`
	// Notes is one-to-a-few-sentence prose: quirks, deviations from
	// the source semantic, why-it-was-added. Rendered under the
	// feature entry in markdown output.
	Notes string `json:"notes,omitempty"`
	// Aliases are alternate names accepted by the parser (e.g.
	// `rlimit-as` alias for `rlimit-addrspace`; `cgroup` alias for
	// `run-in-cgroup`). Lookup by any alias returns the same entry.
	Aliases []string `json:"aliases,omitempty"`
	// Since is the slinit version that introduced this feature. Left
	// empty for pre-v1 features (majority) — populated for new
	// additions so operators know minimum slinit version.
	Since string `json:"since,omitempty"`
}

// Registry is the joined result of auto-discovery + provenance
// annotation, indexed by canonical name for fast lookup. Aliases
// resolve to the same *Feature. Zero-value Registry is unusable —
// call Load() (see merge.go).
type Registry struct {
	byName map[string]*Feature
	all    []*Feature
}

// Lookup returns the Feature for a given name (or alias), or nil
// when unknown. Case-sensitive — slinit's parser and opcode names
// are canonical case (Cmd... / snake-case-directive respectively).
func (r *Registry) Lookup(name string) *Feature {
	if r == nil {
		return nil
	}
	return r.byName[name]
}

// All returns every registered feature in a stable order (sorted by
// Kind, then Name). Safe to iterate concurrently — the slice is
// not modified after Load.
func (r *Registry) All() []*Feature {
	if r == nil {
		return nil
	}
	return r.all
}

// Filter returns every feature matching the predicate. Convenience
// for CLI --list-directives / --group-by=source flags.
func (r *Registry) Filter(pred func(*Feature) bool) []*Feature {
	if r == nil {
		return nil
	}
	var out []*Feature
	for _, f := range r.all {
		if pred(f) {
			out = append(out, f)
		}
	}
	return out
}
