package service

// ServiceDep represents a dependency relationship between two services.
// The 'From' service depends on the 'To' service.
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
