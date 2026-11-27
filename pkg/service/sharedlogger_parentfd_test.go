package service

import (
	"io"
	"path/filepath"
	"testing"
	"time"
)

// A shared-logger producer gets a fresh pipe from the mux on every
// start, but PID 1 never closed its own copy of the write end after
// the fork. Two effects: the mux's reader for that producer never saw
// EOF, so it outlived the producer's process; and every start left one
// more fd open until a GC ran the old *os.File's finalizer. Measured
// with GOGC=off: 30 start/stop cycles, 33 pipe fds open in slinit, the
// same for process, scripted and bgprocess producers.
//
// Each case starts a producer whose process writes a line and exits (or,
// for bgprocess, leaves a daemon with its output elsewhere), then waits
// for the mux's reader to see EOF, which it only can once slinit's copy
// of the write end is closed.
func TestSharedLoggerProducerPipeClosedInParent(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	cases := []struct {
		name string
		mk   func(set *ServiceSet) Service
	}{
		{"process", func(set *ServiceSet) Service {
			s := NewProcessService(set, "prod")
			s.SetCommand([]string{"/bin/sh", "-c", "echo hi"})
			s.SetLogType(LogToPipe)
			return s
		}},
		{"scripted", func(set *ServiceSet) Service {
			s := NewScriptedService(set, "prod")
			s.SetStartCommand([]string{"/bin/sh", "-c", "echo hi"})
			s.SetLogType(LogToPipe)
			return s
		}},
		{"bgprocess", func(set *ServiceSet) Service {
			s := NewBGProcessService(set, "prod")
			s.SetCommand([]string{"/bin/sh", "-c",
				"echo hi; sleep 30 >/dev/null 2>&1 & echo $! > " + pidFile})
			s.SetPIDFile(pidFile)
			s.SetLogType(LogToPipe)
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			mux, err := set.GetOrCreateSharedLogMux("central", SharedLogMuxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer set.RemoveSharedLogMux("central")
			go io.Copy(io.Discard, mux.InputPipe()) //nolint:errcheck

			svc := tc.mk(set)
			svc.Record().SetSharedLoggerName("central")
			set.AddService(svc)
			set.StartService(svc)
			defer func() {
				set.StopService(svc)
				waitState(t, svc, StateStopped, 5*time.Second)
			}()

			deadline := time.Now().Add(5 * time.Second)
			var done chan struct{}
			for done == nil && time.Now().Before(deadline) {
				mux.mu.Lock()
				if p := mux.producers["prod"]; p != nil {
					done = p.doneCh
				}
				mux.mu.Unlock()
				time.Sleep(5 * time.Millisecond)
			}
			if done == nil {
				t.Fatal("producer never registered with the mux")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Errorf("mux reader still running after the producer's output closed: " +
					"slinit holds the write end of the producer pipe")
			}
		})
	}
}
