package journald

// tail.go — the hand-over between slinit-journald and PID 1 at shutdown.
//
// slinit-journald persists what slinit emits, but it stops before the
// shutdown ends: whatever is logged after it — the final services going
// down, the SIGTERM sweep, a stop that runs into the emergency timeout —
// stayed only in PID 1's ring buffer and was gone after the reboot.
//
// journald records how far it got: after every fsync, and when it
// exits, it writes the boot and monotonic time of the last event it has
// made durable to PersistedMarkerPath. At the end of a shutdown, PID 1
// reads that and writes the events of this boot that came after it into
// a file of its own beside journald's — never into journald's file,
// which may be sealed with an FSS key PID 1 does not have. Without a
// marker for the running boot PID 1 writes nothing: journald never
// persisted this boot, and guessing would mean writing a whole boot or
// duplicating one. A journald that dies between two fsyncs leaves a
// marker a little behind, so a few events may be written twice; none
// are lost.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

// PersistedMarkerPath is where slinit-journald (default namespace only)
// records the last event it has made durable.
const PersistedMarkerPath = "/run/slinit-journald.persisted"

// PersistedMarker says how far journald's persistent journal goes.
type PersistedMarker struct {
	BootID string // boot of the last persisted event
	Mts    int64  // its monotonic time, ns
	Dir    string // directory journald writes to
	Format string // "binary" or "jsonl"
}

// WritePersistedMarker replaces the marker at path atomically.
func WritePersistedMarker(path string, m PersistedMarker) error {
	body := fmt.Sprintf("boot=%s\nmts=%d\ndir=%s\nformat=%s\n", m.BootID, m.Mts, m.Dir, m.Format)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadPersistedMarker reads the marker at path.
func ReadPersistedMarker(path string) (PersistedMarker, error) {
	var m PersistedMarker
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "boot":
			m.BootID = v
		case "mts":
			m.Mts, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return m, fmt.Errorf("%s: bad mts %q", path, v)
			}
		case "dir":
			m.Dir = v
		case "format":
			m.Format = v
		}
	}
	if err := sc.Err(); err != nil {
		return m, err
	}
	if m.BootID == "" || m.Dir == "" {
		return m, fmt.Errorf("%s: incomplete marker", path)
	}
	return m, nil
}

// WriteShutdownTail writes the events of boot that come after the marker
// into a new file in the marker's directory and returns how many it
// wrote. Nothing is written, and no file created, when the marker is for
// another boot or no event is newer.
func WriteShutdownTail(m PersistedMarker, events []*journal.Event, boot, machine string, now time.Time) (int, error) {
	if m.BootID != boot {
		return 0, nil
	}
	var tail []*journal.Event
	for _, e := range events {
		if e.BootID == boot && e.Mts > m.Mts {
			tail = append(tail, e)
		}
	}
	if len(tail) == 0 {
		return 0, nil
	}
	short := boot
	if len(short) > 8 {
		short = short[:8]
	}
	base := filepath.Join(m.Dir, "shutdown-"+now.UTC().Format("20060102-150405")+"-"+short)
	switch m.Format {
	case "binary", "":
		w, err := journalbin.NewWriter(base+".journal", boot, machine)
		if err != nil {
			return 0, err
		}
		for _, e := range tail {
			if _, err := w.Append(e); err != nil {
				w.Close()
				return 0, err
			}
		}
		return len(tail), w.Close()
	case "jsonl":
		f, err := os.OpenFile(base+".jsonl", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
		if err != nil {
			return 0, err
		}
		bw := bufio.NewWriter(f)
		for _, e := range tail {
			data, err := json.Marshal(e)
			if err != nil {
				f.Close()
				return 0, err
			}
			bw.Write(append(data, '\n'))
		}
		err = errors.Join(bw.Flush(), f.Sync(), f.Close())
		return len(tail), err
	default:
		return 0, fmt.Errorf("unknown journal format %q", m.Format)
	}
}
