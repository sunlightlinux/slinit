package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestShutdownGateHoldsUntilHandOver(t *testing.T) {
	var warned int
	g := newShutdownGate(func(string, ...any) { warned++ })
	g.Request(service.ShutdownPoweroff, 1)
	g.Request(service.ShutdownReboot, 0)
	if warned != 1 {
		t.Errorf("second held request: %d warnings, want 1", warned)
	}
	var got []service.ShutdownType
	var gotFlags uint8
	g.HandOver(func(st service.ShutdownType, flags uint8) { got = append(got, st); gotFlags = flags })
	if len(got) != 1 || got[0] != service.ShutdownPoweroff || gotFlags != 1 {
		t.Fatalf("hand-over delivered %v flags %d, want [poweroff] flags 1", got, gotFlags)
	}
	g.Request(service.ShutdownHalt, 0)
	if len(got) != 2 || got[1] != service.ShutdownHalt {
		t.Fatalf("after hand-over: %v, want the request forwarded", got)
	}
	g.Reclaim()
	g.Request(service.ShutdownReboot, 0)
	if len(got) != 2 {
		t.Fatalf("after Reclaim the request went to the old handler: %v", got)
	}
	g.HandOver(func(st service.ShutdownType, _ uint8) { got = append(got, st) })
	if len(got) != 3 || got[2] != service.ShutdownReboot {
		t.Fatalf("held across Reclaim: %v, want reboot handed to the next handler", got)
	}
}

// A request racing the hand-over is either held and passed on by
// HandOver, or forwarded by Request; never held after the hand-over
// has looked, which is how one ACKed request used to be lost.
func TestShutdownGateHandOverRace(t *testing.T) {
	for i := 0; i < 2000; i++ {
		g := newShutdownGate(nil)
		var calls atomic.Int32
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Request(service.ShutdownPoweroff, 0)
		}()
		g.HandOver(func(service.ShutdownType, uint8) { calls.Add(1) })
		wg.Wait()
		if n := calls.Load(); n != 1 {
			t.Fatalf("iteration %d: request delivered %d times, want 1", i, n)
		}
	}
}

func TestShutdownGateWakeAndTake(t *testing.T) {
	g := newShutdownGate(nil)
	if _, _, ok := g.Take(); ok {
		t.Fatal("Take on an empty gate returned a request")
	}
	g.Request(service.ShutdownPoweroff, 2)
	select {
	case <-g.Wake():
	default:
		t.Fatal("holding a request did not signal Wake")
	}
	g.Request(service.ShutdownReboot, 0) // second: logged, dropped
	st, flags, ok := g.Take()
	if !ok || st != service.ShutdownPoweroff || flags != 2 {
		t.Fatalf("Take = %v %d %v, want poweroff 2 true", st, flags, ok)
	}
	select {
	case <-g.Wake():
		t.Fatal("Wake still signalled after Take")
	default:
	}
	if _, _, ok := g.Take(); ok {
		t.Fatal("request taken twice")
	}
}

// A request that loaded the handler just before the event loop returned
// is still being applied when main calls Reclaim. Reclaim must wait for
// it: main reads the loop's shutdown type right after, and a poweroff
// that lands later was missed, so the collapse menu came up and its
// timeout rebooted.
func TestShutdownGateReclaimWaitsForInFlight(t *testing.T) {
	g := newShutdownGate(nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var applied atomic.Bool
	g.HandOver(func(service.ShutdownType, uint8) {
		close(entered)
		<-release
		applied.Store(true)
	})
	go g.Request(service.ShutdownPoweroff, 0)
	<-entered
	reclaimed := make(chan struct{})
	go func() {
		g.Reclaim()
		close(reclaimed)
	}()
	select {
	case <-reclaimed:
		t.Fatal("Reclaim returned while a handler call was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-reclaimed
	if !applied.Load() {
		t.Fatal("Reclaim returned before the in-flight request was applied")
	}
}

// A remain (stop every service, keep running) is only for an event
// loop: held, it does not wake a boot-failure menu, a real shutdown
// sent after it takes its place, and with no menu involved it is
// handed over as before.
func TestShutdownGateRemain(t *testing.T) {
	g := newShutdownGate(nil)
	if g.Ready() {
		t.Fatal("Ready before any hand-over")
	}
	g.Request(service.ShutdownRemain, 0)
	select {
	case <-g.Wake():
		t.Fatal("a held remain woke the menu")
	default:
	}
	g.Request(service.ShutdownPoweroff, 0)
	select {
	case <-g.Wake():
	default:
		t.Fatal("a poweroff behind a held remain did not wake the menu")
	}
	if st, _, ok := g.Take(); !ok || st != service.ShutdownPoweroff {
		t.Fatalf("Take = %v %v, want poweroff", st, ok)
	}

	g.Request(service.ShutdownRemain, 0)
	var got []service.ShutdownType
	g.HandOver(func(st service.ShutdownType, _ uint8) { got = append(got, st) })
	if len(got) != 1 || got[0] != service.ShutdownRemain {
		t.Fatalf("hand-over delivered %v, want [remain]", got)
	}
	if !g.Ready() {
		t.Fatal("not Ready after hand-over")
	}
	g.Reclaim()
	if g.Ready() {
		t.Fatal("Ready after Reclaim")
	}
}

// HandOver passes a held request on; the wake it raised must go with it,
// or a later boot-failure menu is interrupted with nothing to carry out
// (the collapse menu then reboots).
func TestShutdownGateHandOverClearsWake(t *testing.T) {
	g := newShutdownGate(nil)
	g.Request(service.ShutdownPoweroff, 0)
	g.HandOver(func(service.ShutdownType, uint8) {})
	select {
	case <-g.Wake():
		t.Fatal("wake still signalled after HandOver passed the request on")
	default:
	}
}
