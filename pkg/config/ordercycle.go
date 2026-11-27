package config

import (
	"fmt"
	"os"
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
// and it is removed again. A loop with no ordering edge on it was refused
// when it was loaded, and is only reported.

// orderingCycleWarn reports a removed edge. Separate so tests can watch.
var orderingCycleWarn = func(msg string) {
	fmt.Fprintf(os.Stderr, "slinit: %s\n", msg)
}

// breakOrderingCycles removes ordering edges until none of roots is on a
// start-order loop, then brings the depth of a systemd unit's before:
// targets up to date, which loading put off while a loop could stand.
func (dl *DirLoader) breakOrderingCycles(roots []service.Service) {
	defer func() {
		var updater service.DepDepthUpdater
		for _, r := range roots {
			if !dl.systemdLoaded[r.Name()] {
				continue
			}
			for _, d := range ownedDeps(r) {
				if d.depType == service.DepBefore {
					updater.AddPotentialUpdate(d.to)
				}
			}
		}
		if err := updater.ProcessUpdates(); err != nil {
			updater.Rollback()
			orderingCycleWarn(fmt.Sprintf("dependency depth: %v", err))
		} else {
			updater.Commit()
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
