package service

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// A service's output pipe is read line by line. bufio.Scanner gives up
// on a line longer than its buffer — Scan returns false with
// ErrTooLong — and the reader goroutine returned, so nothing drained
// the pipe again: once the kernel's pipe buffer filled, the service
// blocked in write(2) for good. One long line (a JSON document, a
// base64 blob) was enough. These tests write such a line and then
// more output than a pipe holds, and require it all to get through.

// writeAll writes data to w in the background and reports when it
// returns, so a test can tell a drained pipe from a wedged writer.
func writeAll(w *os.File, data []byte) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := w.Write(data)
		w.Close()
		done <- err
	}()
	return done
}

func longOutput() []byte {
	var b bytes.Buffer
	b.WriteString(strings.Repeat("x", 100<<10)) // one 100 KiB line
	b.WriteByte('\n')
	for i := 0; i < 20000; i++ { // ~200 KiB more, past any pipe buffer
		b.WriteString("after the long line\n")
	}
	b.WriteString("LAST\n")
	return b.Bytes()
}

func TestSharedLogMuxSurvivesALongLine(t *testing.T) {
	mux, err := NewSharedLogMux()
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	pw, err := mux.AddProducer("svc")
	if err != nil {
		t.Fatal(err)
	}

	sawLast := make(chan struct{})
	go func() {
		r := bufio.NewReaderSize(mux.InputPipe(), 1<<20)
		for {
			line, err := r.ReadString('\n')
			if strings.HasSuffix(line, "] LAST\n") {
				close(sawLast)
				return
			}
			if err != nil {
				return
			}
		}
	}()

	done := writeAll(pw, longOutput())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the service's write blocked: nothing drains its pipe after a long line")
	}
	select {
	case <-sawLast:
	case <-time.After(5 * time.Second):
		t.Fatal("output after the long line never reached the logger")
	}
}

func TestForwardOnlyLoopSurvivesALongLine(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("udp: %v", err)
	}
	defer pc.Close()
	fw, err := NewSyslogForwarder(pc.LocalAddr().String(), "rfc3164", 3, "svc", "svc", quietLogger{})
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()

	sawLast := make(chan struct{})
	go func() {
		buf := make([]byte, 1<<16)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if bytes.HasSuffix(bytes.TrimRight(buf[:n], "\n"), []byte("LAST")) {
				close(sawLast)
				return
			}
		}
	}()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &ProcessService{}
	go s.forwardOnlyLoop(pr, fw)

	done := writeAll(pw, longOutput())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the service's write blocked: nothing drains its pipe after a long line")
	}
	select {
	case <-sawLast:
	case <-time.After(5 * time.Second):
		t.Fatal("output after the long line was never forwarded")
	}
}

type quietLogger struct{}

func (quietLogger) Info(string, ...interface{})  {}
func (quietLogger) Error(string, ...interface{}) {}

func TestReadLinesPieces(t *testing.T) {
	in := "ab\n" + strings.Repeat("y", 40) + "\nlast-no-newline"
	var got []string
	readLines(strings.NewReader(in), 16, func(l []byte) bool {
		got = append(got, string(l))
		return true
	})
	want := []string{"ab", strings.Repeat("y", 16), strings.Repeat("y", 16), strings.Repeat("y", 8), "last-no-newline"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}

	n := 0
	readLines(strings.NewReader("a\nb\nc\n"), 16, func([]byte) bool { n++; return n < 2 })
	if n != 2 {
		t.Errorf("fn returning false: called %d times, want 2", n)
	}
}
