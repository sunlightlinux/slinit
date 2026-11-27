package config

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Start-order cycles through ordering edges.
//
// A service holding a dependency edge of any type waits while the other
// end is STARTING, ordering edges (after:, before:) included. A loop of
// such edges is therefore a start deadlock: every service on it sits in
// STARTING for good. The load-time cycle checks refuse a loop of
// dependency edges, but pass ordering edges over (a loop of them alone
// is usually two services saying the same order from both ends, which is
// harmless), and an edge closing a loop is often installed while the
// service at its other end is still loading and has none of its edges
// yet. So a loop mixing the two kinds, as real systemd units contain one
// (wickedd: Wants=wickedd-nanny; wickedd-nanny: After=wickedd), loaded
// without a word and hung.
//
// systemd breaks such a loop at runtime by dropping a job ordered on it,
// with a warning. slinit breaks it at load, once the whole tree a load or
// reload brought in is installed: from every service the call touched
// (any new loop passes through one of them), a search for a way back to
// it; on each loop found, one ordering edge is removed, one a systemd
// unit declared when there is one, and the loop is named in a warning.
// The description still declares the edge: a reload installs it again
// and it is removed again. A load refuses a loop with no such edge first
// (settleLoad); a reload has the cycle check of checkCycle, so what is
// left for it here is a slinit edge on a loop another reload closed,
// which is dropped the same way rather than leave a deadlock.

// What a load leaves behind is settled in settleLoad: services it loaded
// that hold an edge on a record a failure took out of the set are
// unloaded too (dropDangling); a loop with no edge a systemd unit
// declared refuses the load (a loop of dependencies, or a slinit
// description's after:/before:, as dinit refuses them); and the loops
// left are broken. Loading a before: target unmarks the service that
// declared it (resolveDependencies), which is what lets the first two
// happen at all: they are caught here, once the tree is in, rather than
// at the edge.

// settleLoad finishes LoadService: see above. svc and err are what
// loading returned; the error returned is the load's.
func (dl *DirLoader) settleLoad(svc service.Service, err error) error {
	dl.dropDangling()
	if err == nil {
		for _, r := range dl.touchedInSet() {
			path := findStartCycle(r)
			if path == nil || dl.systemdOrderingEdge(path) != nil {
				continue
			}
			err = &ServiceLoadError{ServiceName: r.Name(),
				Message: "cyclic dependency: " + cycleString(path)}
			for _, e := range path {
				if slices.Contains(dl.touched, e.From) {
					dl.unloadLoaded(e.From)
				}
			}
			dl.dropDangling()
			break
		}
	}
	if err == nil && dl.set.FindService(svc.Name(), false) != svc {
		// Unloaded above as part of, or behind, a refused loop.
		err = &ServiceLoadError{ServiceName: svc.Name(), Message: "a dependency of it was refused"}
	}
	dl.breakOrderingCycles(dl.touchedInSet())
	return err
}

// touchedInSet returns the services this call loaded that are still in
// the set.
func (dl *DirLoader) touchedInSet() []service.Service {
	var out []service.Service
	for _, s := range dl.touched {
		if dl.set.FindService(s.Name(), false) == s {
			out = append(out, s)
		}
	}
	return out
}

// dropDangling unloads each service this call loaded that holds an edge
// on a record no longer in the set (one a later failure took out while
// this service, loading as its before: target's dependency, could take
// an edge on it), and so on to what depends on those.
func (dl *DirLoader) dropDangling() {
	for changed := true; changed; {
		changed = false
		for _, s := range dl.touchedInSet() {
			for _, d := range s.Record().Dependencies() {
				if dl.set.FindService(d.To.Name(), false) != d.To {
					dl.unloadLoaded(s)
					changed = true
					break
				}
			}
		}
	}
}

// unloadLoaded takes a service this call loaded back out: its edges, on
// both ends, and its entry in the set. Under queueMu or not (chain-to
// loads under it), so not ServiceSet.UnloadService, which takes it; the
// service is new and STOPPED, with nothing to stop.
func (dl *DirLoader) unloadLoaded(s service.Service) {
	if dl.set.FindService(s.Name(), false) != s {
		return
	}
	dl.set.RemoveService(s)
	for _, d := range ownedDeps(s) {
		rmResolvedDep(s, d)
	}
	// What this call loaded behind it goes too; anything else holding
	// an edge on it (an ordering edge) only loses the edge.
	for _, d := range s.Record().Dependents() {
		if slices.Contains(dl.touched, d.From) && d.DepType != service.DepBefore {
			dl.unloadLoaded(d.From)
		} else {
			d.From.Record().RmDep(s, d.DepType)
		}
	}
	if dl.set.OnServiceUnloaded != nil {
		dl.set.OnServiceUnloaded(s)
	}
}

// systemdOrderingEdge returns an ordering edge of the loop that a
// systemd unit declared, or nil.
func (dl *DirLoader) systemdOrderingEdge(path []*service.ServiceDep) *service.ServiceDep {
	for _, e := range path {
		if e.IsOnlyOrdering() && dl.systemdLoaded[edgeDeclarer(e).Name()] {
			return e
		}
	}
	return nil
}

// orderingCycleWarn reports a removed edge. Separate so tests can watch.
var orderingCycleWarn = func(msg string) {
	fmt.Fprintf(os.Stderr, "slinit: %s\n", msg)
}

// breakOrderingCycles removes ordering edges until none of roots is on a
// start-order loop, then brings the depth of a systemd unit's before:
// targets up to date, which loading put off while a loop could stand.
func (dl *DirLoader) breakOrderingCycles(roots []service.Service) {
	defer func() {
		for _, r := range roots {
			if !dl.systemdLoaded[r.Name()] || dl.set.FindService(r.Name(), false) != r {
				continue
			}
			var updater service.DepDepthUpdater
			var before []resolvedDep
			for _, d := range ownedDeps(r) {
				if d.depType == service.DepBefore {
					updater.AddPotentialUpdate(d.to)
					before = append(before, d)
				}
			}
			if err := updater.ProcessUpdates(); err != nil {
				// A slinit description's before: is refused here; an
				// ordering edge of a unit is dropped, as the loop's is.
				updater.Rollback()
				for _, d := range before {
					rmResolvedDep(r, d)
				}
				orderingCycleWarn(fmt.Sprintf("%s: dropped its before: edges: %v", r.Name(), err))
			} else {
				updater.Commit()
			}
		}
	}()
	for _, r := range roots {
		if dl.set.FindService(r.Name(), false) != r {
			continue // replaced or unloaded since
		}
		// Each pass removes one edge, so the loop ends; the bound is a
		// guard against a graph changing under us.
		for i := 0; i < 1024; i++ {
			path := findStartCycle(r)
			if path == nil {
				break
			}
			e := dl.orderingEdgeToBreak(path)
			if e == nil {
				orderingCycleWarn(fmt.Sprintf("dependency cycle %s has no ordering edge to drop; its services cannot start",
					cycleString(path)))
				break
			}
			from, to, dt := e.From, e.To, e.DepType
			from.Record().RmDep(to, dt)
			var updater service.DepDepthUpdater
			updater.AddPotentialUpdate(from)
			if err := updater.ProcessUpdates(); err != nil {
				updater.Rollback()
			} else {
				updater.Commit()
			}
			msg := fmt.Sprintf("start-order cycle %s: dropped %s, as systemd drops an ordering job on a cycle; "+
				"the services start, in an order the cycle could not give", cycleString(path), edgeString(e))
			orderingCycleWarn(msg)
			if OnSystemdUnitWarning != nil && dl.systemdLoaded[edgeDeclarer(e).Name()] {
				OnSystemdUnitWarning(edgeDeclarer(e).Name(), "", "WARN", msg)
			}
		}
	}
}

// findStartCycle returns the edges of a loop from r back to r, following
// the edges a starting service waits on, or nil when there is none.
func findStartCycle(r service.Service) []*service.ServiceDep {
	visited := map[service.Service]bool{}
	var path []*service.ServiceDep
	var dfs func(s service.Service) bool
	dfs = func(s service.Service) bool {
		visited[s] = true
		for _, d := range s.Record().Dependencies() {
			path = append(path, d)
			if d.To == r {
				return true
			}
			if !visited[d.To] && dfs(d.To) {
				return true
			}
			path = path[:len(path)-1]
		}
		return false
	}
	if dfs(r) {
		return path
	}
	return nil
}

// edgeDeclarer is the service whose description declared e: the holder,
// except for a before: edge, which its declarer puts on the other end.
func edgeDeclarer(e *service.ServiceDep) service.Service {
	if e.DepType == service.DepBefore {
		return e.To
	}
	return e.From
}

// orderingEdgeToBreak picks the edge of a loop to remove: an ordering
// edge a systemd unit declared, else any ordering edge.
func (dl *DirLoader) orderingEdgeToBreak(path []*service.ServiceDep) *service.ServiceDep {
	var native *service.ServiceDep
	for _, e := range path {
		if !e.IsOnlyOrdering() {
			continue
		}
		if dl.systemdLoaded[edgeDeclarer(e).Name()] {
			return e
		}
		if native == nil {
			native = e
		}
	}
	return native
}

func edgeString(e *service.ServiceDep) string {
	if e.DepType == service.DepBefore {
		return fmt.Sprintf("%s's `before: %s`", e.To.Name(), e.From.Name())
	}
	return fmt.Sprintf("%s's `%s: %s`", e.From.Name(), depKeyword(e.DepType), e.To.Name())
}

func depKeyword(dt service.DependencyType) string {
	switch dt {
	case service.DepRegular:
		return "depends-on"
	case service.DepWaitsFor, service.DepSoft:
		return "waits-for"
	case service.DepMilestone:
		return "depends-ms"
	case service.DepPreparedBy:
		return "prepared-by"
	case service.DepAfter:
		return "after"
	}
	return dt.String()
}

// cycleString names the services of a loop in the order they wait:
// "a -> b -> c -> a".
func cycleString(path []*service.ServiceDep) string {
	names := []string{path[0].From.Name()}
	for _, e := range path {
		names = append(names, e.To.Name())
	}
	return strings.Join(names, " -> ")
}
