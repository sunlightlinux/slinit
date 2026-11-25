package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/journal"
)

// startFakeJournalServer serves a control socket that answers journal
// queries the way pkg/control/journal.go does — buf.Query(filter,
// Limit) — over twenty entries alternating cron/sshd, "msg 0".."msg 19".
// A subscription gets its backlog and then the connection closes, so a
// follow session ends. Returns the socket path.
func startFakeJournalServer(t *testing.T) string {
	t.Helper()
	buf := journal.NewEventBuffer(1000)
	base := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 20; i++ {
		id := "sshd"
		if i%2 == 0 {
			id = "cron"
		}
		buf.Push(&journal.Event{Ts: base.Add(time.Duration(i) * time.Minute).UnixNano(), Mts: int64(i+1) * 1e9,
			BootID: testBoot1, Unit: id + ".service", SyslogIdentifier: id, Msg: fmt.Sprintf("msg %d", i), Prio: 6})
	}
	return serveJournalBuffer(t, buf)
}

// serveJournalBuffer is startFakeJournalServer over buf, which the test
// may go on filling.
func serveJournalBuffer(t *testing.T, buf *journal.EventBuffer) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					typ, body, err := control.ReadPacket(c)
					if err != nil {
						return
					}
					var req control.JournalQueryRequest
					_ = json.Unmarshal(body, &req)
					evs, _ := buf.Query(req.ToFilter(), req.Limit)
					for _, e := range evs {
						d, _ := e.MarshalJSONL()
						control.WritePacket(c, control.RplyJournalEntry, d)
					}
					if typ == control.CmdJournalSubscribe {
						return
					}
					control.WritePacket(c, control.RplyJournalDone, nil)
				}
			}(c)
		}
	}()
	return sock
}

// captureStdout runs fn with os.Stdout redirected and returns what it
// printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	ferr := fn()
	os.Stdout = saved
	w.Close()
	out := <-done
	r.Close()
	return string(out), ferr
}

// queryLive runs one live query against sock, as runQuery would after
// parsing args.
func queryLive(t *testing.T, sock string, args ...string) (string, error) {
	t.Helper()
	opts, err := parseArgs(append([]string{"--socket-path", sock, "-o", "cat"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return captureStdout(t, func() error { return runQuery(opts) })
}
