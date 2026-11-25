package main

import (
	"sync"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// shutdownGate is the control server's ShutdownFunc for the life of the
// process. The control server listens before the boot services are
// started, so a request can arrive while there is no event loop to
// carry it out: until one is handed over, a request is held, and it is
// forwarded to the loop the moment there is one.
//
// It is one value, installed before the server starts and never
// replaced, because the server's connection goroutines read the
// function without a lock. Swapping the function at hand-over was a
// data race, and it could lose a request: a handler that had loaded the
// holder just before the swap stored its request after main had already
// drained the held slot, and the client had its ACK for a shutdown that
// never came. Whether to hold or forward is now decided under the same
// lock that hand-over takes.
type shutdownGate struct {
	mu      sync.Mutex
	handler func(service.ShutdownType, uint8) // nil: no event loop, hold
	held    bool
	st      service.ShutdownType
	flags   uint8
	// wake is signalled when a request is held, for whoever waits
	// without an event loop (the boot-failure menus) to Take it.
	wake chan struct{}
	// logf reports a request that is not acted on. May be nil.
	logf func(format string, args ...any)
}

func newShutdownGate(logf func(format string, args ...any)) *shutdownGate {
	return &shutdownGate{logf: logf, wake: make(chan struct{}, 1)}
}

// Request is the ShutdownFunc: forward to the event loop, or hold.
func (g *shutdownGate) Request(st service.ShutdownType, flags uint8) {
	g.mu.Lock()
	if h := g.handler; h != nil {
		g.mu.Unlock()
		h(st, flags)
		return
	}
	defer g.mu.Unlock()
	if g.held {
		// The client has its ACK either way; at least say which of
		// the two will happen.
		if g.logf != nil {
			g.logf("Shutdown (%s) requested while a %s is already pending; the %s goes ahead",
				st, g.st, g.st)
		}
		return
	}
	g.st, g.flags, g.held = st, flags, true
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// Wake delivers after a request has been held. Pair it with Take.
func (g *shutdownGate) Wake() <-chan struct{} { return g.wake }

// Take removes and returns the held request, for a caller that has no
// event loop to hand it to and carries it out itself.
func (g *shutdownGate) Take() (service.ShutdownType, uint8, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-g.wake:
	default:
	}
	held := g.held
	g.held = false
	return g.st, g.flags, held
}

// HandOver makes h the request handler and passes it a request held
// until now. Called with each new event loop, before it runs.
func (g *shutdownGate) HandOver(h func(service.ShutdownType, uint8)) {
	g.mu.Lock()
	g.handler = h
	held, st, flags := g.held, g.st, g.flags
	g.held = false
	g.mu.Unlock()
	if held {
		h(st, flags)
	}
}

// Reclaim goes back to holding requests: the event loop has returned,
// and a request sent to it now would be acknowledged and dropped.
func (g *shutdownGate) Reclaim() {
	g.mu.Lock()
	g.handler = nil
	g.mu.Unlock()
}
