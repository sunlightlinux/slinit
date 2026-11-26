package journald

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenFileSinkWithFallback tries to open a FileSink at primaryDir
// first; on any error (mkdir, probe write, first jsonl open) it
// retries at fallbackDir and returns (sink, dir_used, primaryErr).
// primaryErr is the underlying failure from the first attempt —
// nil when primary succeeded.
//
// Empty fallbackDir → fall back skipped; the primary error is
// returned as the caller's error and sink is nil.
//
// Typical wiring:
//
//	sink, dir, degraded := journald.OpenFileSinkWithFallback(
//	    journald.DefaultJournalDir, journald.DefaultVolatileDir, opts)
//	if degraded != nil {
//	    log.Warn("journal: %s unwritable (%v), using volatile %s", …)
//	}
func OpenFileSinkWithFallback(primaryDir, fallbackDir string, opts FileSinkOptions) (*FileSink, string, error) {
	return OpenWithFallback(primaryDir, fallbackDir, func(dir string) (*FileSink, error) {
		return NewFileSinkWithOptions(dir, opts)
	})
}

// OpenWithFallback is OpenFileSinkWithFallback for any sink: open is
// called with the directory it is to write in, so anything bound to a
// directory (a vacuum hook) is bound to the one actually used.
func OpenWithFallback[S any](primaryDir, fallbackDir string, open func(dir string) (S, error)) (S, string, error) {
	var zero S
	if err := probeWritable(primaryDir); err == nil {
		fs, ferr := open(primaryDir)
		if ferr == nil {
			return fs, primaryDir, nil
		}
		// Primary probe passed but sink open failed (rare — race
		// with a chmod, quota exhaustion). Fall through so the
		// fallback still gets a shot.
		if fallbackDir == "" {
			return zero, "", ferr
		}
		fs, ferr2 := open(fallbackDir)
		if ferr2 != nil {
			return zero, "", fmt.Errorf("journald: both primary (%v) and fallback (%v) failed",
				ferr, ferr2)
		}
		return fs, fallbackDir, ferr
	} else {
		if fallbackDir == "" {
			return zero, "", err
		}
		fs, ferr := open(fallbackDir)
		if ferr != nil {
			return zero, "", fmt.Errorf("journald: primary %s unwritable (%v) and fallback %s failed (%v)",
				primaryDir, err, fallbackDir, ferr)
		}
		return fs, fallbackDir, err
	}
}

// probeWritable checks whether dir exists (creating it if needed) and
// accepts a write. Returns nil when a probe file was successfully
// created, written, and removed — the strongest guarantee cheap to
// obtain that a FileSink open will succeed.
func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("probe create: %w", err)
	}
	name := probe.Name()
	if _, err := probe.Write([]byte("ok")); err != nil {
		_ = probe.Close()
		_ = os.Remove(name)
		return fmt.Errorf("probe write: %w", err)
	}
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("probe close: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("probe remove: %w", err)
	}
	// Sanity: dir should now hold no probe files.
	if _, err := filepath.Abs(dir); err != nil {
		return err
	}
	return nil
}
