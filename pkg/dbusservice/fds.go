package dbusservice

import (
	"reflect"
	"sync"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// CloseReceivedFDs returns the options that make a connection close the
// file descriptors it receives. godbus turns each descriptor a message
// carries into a dbus.UnixFD and never closes it, whether a method takes
// it, ignores it or is refused, so any local user could fill a daemon's
// descriptor table by sending some with any call. Once the table was
// full godbus dropped the bus connection, and the daemon stayed up with
// no name on the bus. sd-bus frees a message's descriptors with the
// message; this does the same.
//
// The descriptors of a method call stay open until its reply or error is
// sent, which godbus does once the method has returned, so a method may
// use them during the call; one that keeps a descriptor beyond the call
// must duplicate it, and none may close one. A call that wants no reply,
// or that reuses the serial of one still in flight, never sees its
// descriptors: they are closed on arrival and its arguments dropped, so
// it is refused. Descriptors in signals and replies are closed on
// arrival; no daemon here takes one from either.
//
// A descriptor the message counts but its body does not name is closed
// by nothing: godbus does not hand it over at all.
func CloseReceivedFDs() []dbus.ConnOption {
	c := &fdCloser{pending: map[callKey][]int{}}
	return []dbus.ConnOption{
		dbus.WithIncomingInterceptor(c.incoming),
		dbus.WithOutgoingInterceptor(c.outgoing),
	}
}

type callKey struct {
	sender string
	serial uint32
}

type fdCloser struct {
	mu      sync.Mutex
	pending map[callKey][]int // a call's descriptors, until it is answered
}

// incoming runs on godbus's reader goroutine, before the message is
// dispatched.
func (c *fdCloser) incoming(msg *dbus.Message) {
	fds := collectFDs(reflect.ValueOf(msg.Body), nil)
	if len(fds) == 0 {
		return
	}
	if msg.Type == dbus.TypeMethodCall && msg.Flags&dbus.FlagNoReplyExpected == 0 {
		sender, _ := msg.Headers[dbus.FieldSender].Value().(string)
		k := callKey{sender, msg.Serial()}
		c.mu.Lock()
		_, dup := c.pending[k]
		if !dup {
			c.pending[k] = fds
		}
		c.mu.Unlock()
		if !dup {
			return
		}
	}
	closeFDs(fds)
	if msg.Type != dbus.TypeSignal {
		msg.Body = nil
	}
}

// outgoing closes a call's descriptors as its answer goes out.
func (c *fdCloser) outgoing(msg *dbus.Message) {
	if msg.Type != dbus.TypeMethodReply && msg.Type != dbus.TypeError {
		return
	}
	serial, _ := msg.Headers[dbus.FieldReplySerial].Value().(uint32)
	dest, _ := msg.Headers[dbus.FieldDestination].Value().(string)
	k := callKey{dest, serial}
	c.mu.Lock()
	fds := c.pending[k]
	delete(c.pending, k)
	c.mu.Unlock()
	closeFDs(fds)
}

var (
	unixFDType  = reflect.TypeOf(dbus.UnixFD(0))
	variantType = reflect.TypeOf(dbus.Variant{})
)

// collectFDs appends every descriptor in v, at any depth, to fds; a
// descriptor named twice is listed once.
func collectFDs(v reflect.Value, fds []int) []int {
	switch {
	case !v.IsValid():
	case v.Type() == unixFDType:
		fd := int(v.Int())
		for _, have := range fds {
			if have == fd {
				return fds
			}
		}
		fds = append(fds, fd)
	case v.Type() == variantType:
		fds = collectFDs(reflect.ValueOf(v.Interface().(dbus.Variant).Value()), fds)
	default:
		switch v.Kind() {
		case reflect.Interface:
			fds = collectFDs(v.Elem(), fds)
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				fds = collectFDs(v.Index(i), fds)
			}
		case reflect.Map:
			it := v.MapRange()
			for it.Next() {
				fds = collectFDs(it.Key(), fds)
				fds = collectFDs(it.Value(), fds)
			}
		}
	}
	return fds
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}
