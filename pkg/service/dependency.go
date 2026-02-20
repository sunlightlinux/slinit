package service

// ServiceDep represents a dependency relationship between two services.
// The 'From' service depends on the 'To' service.
//
// Dependency type behaviors:
//
//   REGULAR (hard): From waits for To to start. If To fails, From fails too.
//     If To stops, From must stop. Stop/failure propagates through the chain.
//
//   SOFT: From starts in parallel with To. If To fails or stops, From is
//     unaffected. The link is broken on non-restart stop, retained on restart.
//
//   WAITS_FOR: Like SOFT, but From waits for To to start or fail before
//     proceeding. If To fails, From continues (no cascade). Link semantics
//     same as SOFT.
//
//   MILESTONE: Hard until satisfied, then soft. While From is waiting (WaitingOn
//     true), behaves like REGULAR. Once To starts and WaitingOn clears, behaves
//     like SOFT - subsequent stop of To does not affect From.
//
//   BEFORE: Ordering constraint only. From starts before To. Does not
//     participate in require/release semantics or stop propagation.
//
//   AFTER: Ordering constraint only. From starts after To. Same semantics
//     as BEFORE regarding require/release.
type ServiceDep struct {
	From Service
	To   Service

	// Whether the 'from' service is waiting for the 'to' service to start
	WaitingOn bool
	// Whether the 'from' service is holding an acquire on the 'to' service
	HoldingAcq bool

	DepType DependencyType
}

// NewServiceDep creates a new dependency from one service to another.
func NewServiceDep(from, to Service, depType DependencyType) *ServiceDep {
	return &ServiceDep{
		From:    from,
		To:      to,
		DepType: depType,
	}
}

// IsHard returns true if this is a hard dependency (REGULAR or MILESTONE still waiting).
func (d *ServiceDep) IsHard() bool {
	return d.DepType == DepRegular ||
		(d.DepType == DepMilestone && d.WaitingOn)
}

// IsOnlyOrdering returns true if this is just an ordering constraint (BEFORE or AFTER).
func (d *ServiceDep) IsOnlyOrdering() bool {
	return d.DepType == DepBefore || d.DepType == DepAfter
}

// PrelimDep holds preliminary dependency information used during service loading.
type PrelimDep struct {
	To      string
	DepType DependencyType
}
