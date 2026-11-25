package main

import (
	"sync/atomic"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// watcherCtrlServer is the control server, published once it exists, so
// the services-dir watcher (started before it) can ask whether a client
// holds a service it is about to unload.
var watcherCtrlServer atomic.Pointer[control.Server]

// dirUnloadOutcome is what the services-dir watcher did with a service
// whose file disappeared.
type dirUnloadOutcome int

const (
	dirUnloadNotLoaded  dirUnloadOutcome = iota // nothing by that name
	dirUnloadRunning                            // not STOPPED: left loaded
	dirUnloadReferenced                         // a dependent or a client holds it: left loaded
	dirUnloadDone                               // unloaded
)

// unloadDisappeared unloads the service named by a removed file, under
// the same gate as the unload control command: STOPPED, no dependent
// but ordering ones (HasLoneRef), and no control client holding a
// handle. Skipping the gate stripped the depends-on edge from every
// dependent, which then started without its hard dependency.
func unloadDisappeared(set *service.ServiceSet, name string, heldByClient func(service.Service) bool) dirUnloadOutcome {
	svc := set.FindService(name, false)
	if svc == nil {
		return dirUnloadNotLoaded
	}
	if svc.Record().State() != service.StateStopped {
		return dirUnloadRunning
	}
	if heldByClient(svc) || !set.UnloadServiceIfLone(svc) {
		return dirUnloadReferenced
	}
	return dirUnloadDone
}
