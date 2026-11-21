package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journald"
)

// The marker PID 1 reads at shutdown is written only once the event is
// durable — after an fsync or on Close — never on Handle alone, or PID 1
// would skip events journald had not yet written.
func TestGuardedSinkMarksOnlyDurableEvents(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "persisted")
	g := &guardedSink{inner: &countingSink{}, flushDelay: time.Hour,
		format: "binary", markerPath: marker, currentDir: "/var/log/slinit-journal"}

	g.Handle(&journal.Event{BootID: "b1", Mts: 5e9, Msg: "x"})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker written before any fsync (err %v)", err)
	}
	if err := g.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := journald.ReadPersistedMarker(marker)
	if err != nil || m.BootID != "b1" || m.Mts != 5e9 || m.Format != "binary" || m.Dir != "/var/log/slinit-journal" {
		t.Fatalf("after Flush: %+v, %v", m, err)
	}
	g.Handle(&journal.Event{BootID: "b1", Mts: 7e9, Msg: "y"})
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if m, _ := journald.ReadPersistedMarker(marker); m.Mts != 7e9 {
		t.Errorf("after Close: mts %d, want 7e9", m.Mts)
	}
}

// Another namespace's journald must not write the default marker.
func TestMarkerOnlyForDefaultNamespace(t *testing.T) {
	if markerPathFor("") != journald.PersistedMarkerPath || markerPathFor("tenant") != "" {
		t.Errorf("markerPathFor: %q / %q", markerPathFor(""), markerPathFor("tenant"))
	}
}
