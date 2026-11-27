package service

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// benchJournal installs a process-wide journal emitter for the
// benchmark, with or without a datagram listener draining events.sock.
func benchJournal(b *testing.B, listener bool) {
	b.Helper()
	if err := journal.InitIDs("h"); err != nil {
		b.Skip(err)
	}
	p := filepath.Join(b.TempDir(), "ev.sock")
	if listener {
		l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: p, Net: "unixgram"})
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { l.Close() })
		_ = l.SetReadBuffer(8 << 20)
		go func() {
			buf := make([]byte, 70000)
			for {
				if _, err := l.Read(buf); err != nil {
					return
				}
			}
		}()
	}
	e := journal.NewEmitter(journal.NewEventBuffer(4096), p)
	journal.SetGlobal(e)
	b.Cleanup(func() {
		journal.SetGlobal(nil)
		e.Close()
	})
}

// BenchmarkFanStartStop starts and stops a target that waits-for 1000
// internal services: every transition emits a journal state event
// under queueMu.
func BenchmarkFanStartStop(b *testing.B) {
	for _, mode := range []string{"nojournal", "journal-nolistener", "journal-listener"} {
		b.Run(mode, func(b *testing.B) {
			if mode != "nojournal" {
				benchJournal(b, mode == "journal-listener")
			}
			set, _ := newTestSet()
			boot := NewInternalService(set, "boot")
			set.AddService(boot)
			for i := 0; i < 1000; i++ {
				s := NewInternalService(set, fmt.Sprintf("leaf-%d", i))
				set.AddService(s)
				boot.Record().AddDep(s, DepWaitsFor)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				set.StartService(boot)
				if boot.State() != StateStarted {
					b.Fatal(boot.State())
				}
				set.StopService(boot)
				if boot.State() != StateStopped {
					b.Fatal(boot.State())
				}
			}
		})
	}
}

// BenchmarkEmitJournalStateEventNoGlobal is the state-event emit of a
// process that never installed a journal emitter (tests, embedded).
func BenchmarkEmitJournalStateEventNoGlobal(b *testing.B) {
	set, _ := newTestSet()
	s := NewProcessService(set, "sshd")
	set.AddService(s)
	r := s.Record()
	r.invocationID = "0123456789abcdef0123456789abcdef"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.emitJournalStateEvent(EventStarted)
	}
}

// BenchmarkLogLine is one captured service output line through the
// log rotator's filters, file write and journal emit.
func BenchmarkLogLine(b *testing.B) {
	for _, mode := range []string{"nojournal", "journal-nolistener", "journal-listener"} {
		b.Run(mode, func(b *testing.B) {
			if mode != "nojournal" {
				benchJournal(b, mode == "journal-listener")
			}
			lr, err := NewLogRotator(LogRotatorConfig{
				ServiceName: "sshd",
				FilePath:    filepath.Join(b.TempDir(), "log"),
				LogLevelMax: -1,
				AlertLevel:  -1,
				GetPID:      func() int { return 1234 },
			})
			if err != nil {
				b.Fatal(err)
			}
			defer lr.Close()
			line := []byte("Accepted publickey for root from 10.0.0.1 port 51234 ssh2: ED25519 SHA256:abc\n")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				lr.processLine(line)
			}
		})
	}
}

// BenchmarkReleaseStopsLast is one stops-last check, run on every
// queue pass during a shutdown that pinned a stops-last service while
// another service is still stopping.
func BenchmarkReleaseStopsLast(b *testing.B) {
	for _, n := range []int{1000, 4000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			set, _ := newTestSet()
			var all []Service
			for i := 0; i < n; i++ {
				s := NewInternalService(set, fmt.Sprintf("s-%d", i))
				set.AddService(s)
				all = append(all, s)
			}
			last := all[n-1]
			set.stopsLast = []Service{last}
			for _, s := range all[1 : n-1] {
				s.Record().state.Store(StateStopped)
			}
			all[0].Record().state.Store(StateStopping)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				set.queueMu.Lock()
				set.releaseStopsLastLocked()
				set.queueMu.Unlock()
			}
		})
	}
}

// benchAsyncService is an internal service whose start completes only
// when the benchmark calls Started, as a process service's does when
// its readiness arrives.
type benchAsyncService struct{ InternalService }

func (s *benchAsyncService) BringUp() bool { return true }

// BenchmarkAsyncDepsStart: a target depends on N services that report
// Started one at a time, in declaration order (fwd) or reverse (rev).
func BenchmarkAsyncDepsStart(b *testing.B) {
	for _, n := range []int{250, 1000, 4000} {
		for _, order := range []string{"fwd", "rev"} {
			b.Run(fmt.Sprint(n, order), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					set, _ := newTestSet()
					boot := NewInternalService(set, "boot")
					set.AddService(boot)
					leaves := make([]*benchAsyncService, n)
					for j := 0; j < n; j++ {
						s := &benchAsyncService{}
						s.ServiceRecord = *NewServiceRecord(s, set, fmt.Sprintf("leaf-%d", j), TypeInternal)
						set.AddService(s)
						boot.Record().AddDep(s, DepRegular)
						leaves[j] = s
					}
					set.StartService(boot)
					b.StartTimer()
					for j := 0; j < n; j++ {
						k := j
						if order == "rev" {
							k = n - 1 - j
						}
						set.queueMu.Lock()
						leaves[k].Started()
						set.processQueuesLocked()
						set.queueMu.Unlock()
					}
					if boot.State() != StateStarted {
						b.Fatal(boot.State())
					}
				}
			})
		}
	}
}

// BenchmarkLogRotatorThroughput pushes service output through the
// pipe and the rotator's reader goroutine into a size-rotated logfile,
// the path a logfile with log-max-size takes in PID 1.
func BenchmarkLogRotatorThroughput(b *testing.B) {
	for _, mode := range []string{"nojournal", "journal-nolistener", "journal-listener"} {
		b.Run(mode, func(b *testing.B) {
			if mode != "nojournal" {
				benchJournal(b, mode == "journal-listener")
			}
			line := []byte("Accepted publickey for root from 10.0.0.1 port 51234 ssh2: ED25519 SHA256:abc\n")
			chunk := bytes.Repeat(line, 64<<10/len(line))
			const perOp = 4 << 20
			b.SetBytes(int64(perOp / len(chunk) * len(chunk)))
			dir := b.TempDir()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				os.RemoveAll(dir)
				os.MkdirAll(dir, 0o700)
				lr, err := NewLogRotator(LogRotatorConfig{
					ServiceName: "sshd",
					FilePath:    filepath.Join(dir, "log"),
					MaxSize:     1 << 20,
					MaxFiles:    3,
					FileUID:     -1,
					FileGID:     -1,
					LogLevelMax: -1,
					AlertLevel:  -1,
					GetPID:      func() int { return 1234 },
				})
				if err != nil {
					b.Fatal(err)
				}
				w, err := lr.CreatePipe()
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				lr.StartReader()
				for n := 0; n+len(chunk) <= perOp; n += len(chunk) {
					w.Write(chunk)
				}
				lr.CloseWriteEnd()
				<-lr.doneCh
				b.StopTimer()
				lr.Close()
				b.StartTimer()
			}
		})
	}
}
