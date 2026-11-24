package main

import (
	"github.com/sunlightlinux/slinit/pkg/persist"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/snapshot"
)

// pinStopsBeforeBoot loads and pins stopped every service that the
// restore snapshot at snapPath (if any) or the persisted intents mark
// pinned-stopped. It runs before the boot services are started: the
// full replay of both comes after, and by then a pinned-stopped service
// in the boot graph had already been started, and was left STARTED with
// the pin set. Read and load errors are left for that replay to report.
func pinStopsBeforeBoot(set *service.ServiceSet, snapPath string, pins *persist.PinStore) {
	var names []string
	if snapPath != "" {
		if snap, err := snapshot.Read(snapPath); err == nil {
			for _, e := range snap.Services {
				if e.PinnedStop && e.Name != "" {
					names = append(names, e.Name)
				}
			}
		}
	}
	if intents, err := pins.Load(); err == nil {
		for name, intent := range intents {
			if intent == persist.IntentPinnedStopped {
				names = append(names, name)
			}
		}
	}
	for _, name := range names {
		var svc service.Service
		var err error
		set.WithGraphLock(func() {
			svc, err = set.LoadService(name)
		})
		if err == nil {
			svc.PinStop()
		}
	}
}
