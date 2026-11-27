package service

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestShutdownAfterRandomOps drives a small graph of test services, some
// of which start and stop asynchronously, through a random sequence of
// the operations the control socket offers (start, start --pin, stop,
// release, force stop, pin-stop, unpin, wake, once, restart, add-dep,
// rm-dep), with async completions interleaved, then begins a shutdown
// with StopAllServices and completes whatever is pending until nothing
// moves. Every service must end STOPPED and the active count at 0:
// anything else is a shutdown that waits out its emergency timeout.
//
// A failing sequence is minimised before it is reported.
// SHUTFUZZ_ITERS raises the iteration count, SHUTFUZZ_SEED picks another
// sequence.
func TestShutdownAfterRandomOps(t *testing.T) {
	iters := 5000
	if testing.Short() {
		iters = 500
	}
	if v, err := strconv.Atoi(os.Getenv("SHUTFUZZ_ITERS")); err == nil && v > 0 {
		iters = v
	}
	seed := int64(20261127)
	if v, err := strconv.ParseInt(os.Getenv("SHUTFUZZ_SEED"), 10, 64); err == nil {
		seed = v
	}
	for i := 0; i < iters; i++ {
		p := genShutdownProg(seed + int64(i))
		if msg := runShutdownProg(t, p); msg != "" {
			p = minimiseShutdownProg(t, p)
			t.Fatalf("seed %d: %s", seed+int64(i), runShutdownProg(t, p))
		}
	}
}

const shutFuzzN = 7

type shutOp struct {
	op   int
	a, b int
	typ  DependencyType
}

type shutProg struct {
	asyncUp, asyncDown [shutFuzzN]bool
	ops                []shutOp // initial edges are op 10 too
}

func genShutdownProg(seed int64) shutProg {
	rng := rand.New(rand.NewSource(seed))
	var p shutProg
	for i := 0; i < shutFuzzN; i++ {
		p.asyncUp[i] = rng.Intn(2) == 0
		p.asyncDown[i] = rng.Intn(2) == 0
	}
	depType := func() DependencyType {
		if rng.Intn(3) == 0 {
			return DepRegular
		}
		return DepWaitsFor
	}
	// A starting graph shaped like the stress harness.
	for i := 1; i < shutFuzzN; i++ {
		p.ops = append(p.ops, shutOp{op: 10, a: i, b: rng.Intn(i), typ: depType()})
	}
	nops := 5 + rng.Intn(40)
	for k := 0; k < nops; k++ {
		p.ops = append(p.ops, shutOp{op: rng.Intn(14), a: rng.Intn(shutFuzzN), b: rng.Intn(shutFuzzN), typ: depType()})
	}
	return p
}

// minimiseShutdownProg drops ops and async flags while the program still
// fails.
func minimiseShutdownProg(t *testing.T, p shutProg) shutProg {
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(p.ops); i++ {
			q := p
			q.ops = append(append([]shutOp(nil), p.ops[:i]...), p.ops[i+1:]...)
			if runShutdownProg(t, q) != "" {
				p, changed = q, true
				i--
			}
		}
		for i := 0; i < shutFuzzN; i++ {
			for _, down := range []bool{false, true} {
				q := p
				if down && q.asyncDown[i] {
					q.asyncDown[i] = false
				} else if !down && q.asyncUp[i] {
					q.asyncUp[i] = false
				} else {
					continue
				}
				if runShutdownProg(t, q) != "" {
					p, changed = q, true
				}
			}
		}
	}
	return p
}

func runShutdownProg(t *testing.T, p shutProg) string {
	set, _ := newTestSet()
	svcs := make([]*testSvc, shutFuzzN)
	for i := range svcs {
		svcs[i] = newTestSvc(t, set, fmt.Sprintf("s%d", i))
		svcs[i].autoStart = !p.asyncUp[i]
		svcs[i].autoStop = !p.asyncDown[i]
		set.AddService(svcs[i])
	}
	var async []string
	for i := range svcs {
		if p.asyncUp[i] || p.asyncDown[i] {
			async = append(async, fmt.Sprintf("s%d(up=%v,down=%v)", i, p.asyncUp[i], p.asyncDown[i]))
		}
	}

	var trace []string
	complete := func(s *testSvc) bool {
		done := false
		set.WithGraphLock(func() {
			switch {
			case s.upReqd && s.State() == StateStarting && !s.autoStart:
				s.Started()
				s.upReqd = false
				done = true
			case s.downReqd && s.State() == StateStopping && !s.autoStop:
				s.Stopped()
				done = true
			}
			if done {
				set.processQueuesLocked()
			}
		})
		return done
	}

	for _, o := range p.ops {
		a, b := svcs[o.a], svcs[o.b]
		switch o.op {
		case 0, 1:
			// As handleStartService.
			pin := o.op == 1
			trace = append(trace, fmt.Sprintf("start(pin=%v) %s", pin, a.Name()))
			if a.IsStopPinned() && a.State() != StateStarted {
				continue
			}
			set.StartServiceUnlessShuttingDown(a, pin)
		case 2, 4, 5, 9:
			// As handleStopService: 2 stop, 4 --force, 5 --pin, 9 restart.
			force, pin, restart := o.op == 4, o.op == 5, o.op == 9
			trace = append(trace, fmt.Sprintf("stop(force=%v,pin=%v,restart=%v) %s", force, pin, restart, a.Name()))
			if a.State() == StateStopped {
				set.StopService(a)
				if pin {
					set.PinStopService(a)
				}
				continue
			}
			if !force && a.IsStartPinned() {
				continue
			}
			if restart {
				set.MarkRestartingService(a)
			}
			if force {
				set.ForceStopService(a)
			} else {
				set.StopService(a)
			}
			if pin {
				set.PinStopService(a)
			}
			if restart {
				set.StartService(a)
			}
		case 3:
			trace = append(trace, "release "+a.Name())
			if a.State() != StateStopped {
				set.ReleaseService(a)
			}
		case 6:
			set.UnpinService(a)
			trace = append(trace, "unpin "+a.Name())
		case 7:
			trace = append(trace, "wake "+a.Name())
			if a.State() != StateStarted && !a.IsStopPinned() {
				set.WakeService(a)
			}
		case 8:
			set.StartOnceService(a)
			trace = append(trace, "once "+a.Name())
		case 10:
			err := set.AddDependency(a, b, o.typ)
			trace = append(trace, fmt.Sprintf("add-dep %s %v %s (%v)", a.Name(), o.typ, b.Name(), err))
		case 11:
			ok := set.RemoveDependency(a, b, o.typ)
			trace = append(trace, fmt.Sprintf("rm-dep %s %v %s (%v)", a.Name(), o.typ, b.Name(), ok))
		default:
			if complete(a) {
				trace = append(trace, "complete "+a.Name())
			}
		}
	}

	set.StopAllServices(ShutdownPoweroff)
	trace = append(trace, "SHUTDOWN")
	for moved := true; moved; {
		moved = false
		for _, s := range svcs {
			if complete(s) {
				moved = true
				trace = append(trace, "complete "+s.Name())
			}
		}
	}

	var up, all []string
	set.WithGraphLock(func() {
		for _, s := range svcs {
			if s.State() != StateStopped {
				up = append(up, s.Name())
			}
			all = append(all, describeForShutdownFuzz(s))
		}
	})
	active := set.CountActiveServices()
	if len(up) == 0 && active == 0 {
		return ""
	}
	return fmt.Sprintf("after shutdown: %d active, not stopped: %v\nasync: %v\nall:\n  %s\ntrace:\n  %s",
		active, up, async, strings.Join(all, "\n  "), strings.Join(trace, "\n  "))
}

func describeForShutdownFuzz(s *testSvc) string {
	r := s.Record()
	var deps []string
	for _, d := range r.Dependencies() {
		deps = append(deps, fmt.Sprintf("%s:%v(acq=%v,wait=%v)", d.To.Name(), d.DepType, d.HoldingAcq, d.WaitingOn))
	}
	return fmt.Sprintf("%s=%v target=%v reqBy=%d explicit=%v pinS=%v dptPin=%v pinStop=%v deps=%v",
		s.Name(), s.State(), s.TargetState(), r.RequiredBy(), r.IsMarkedActive(), r.pinnedStarted,
		r.deptPinnedStarted, r.pinnedStopped, deps)
}
