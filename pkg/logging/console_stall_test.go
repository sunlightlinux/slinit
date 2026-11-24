package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// stoppedPTY opens a pty and stops its output (TCOOFF): what a VT looks
// like after Ctrl-S or Scroll Lock, or a serial line held off by XOFF.
// It returns the master, the slave's path and a control fd on the slave
// for TCOON. Nothing reads the master until the test does.
func stoppedPTY(t *testing.T) (master *os.File, slavePath string, ctl *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("ptsname: %v", err)
	}
	slavePath = fmt.Sprintf("/dev/pts/%d", n)
	ctl, err = os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open slave: %v", err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err := unix.IoctlSetInt(int(ctl.Fd()), unix.TCXONC, unix.TCOOFF); err != nil {
		t.Fatalf("TCOOFF: %v", err)
	}
	return master, slavePath, ctl
}

// ptyReader collects what reaches a pty master.
type ptyReader struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func readPTY(master *os.File) *ptyReader {
	r := &ptyReader{}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := master.Read(b)
			r.mu.Lock()
			r.buf.Write(b[:n])
			r.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return r
}

func (r *ptyReader) waitFor(t *testing.T, s string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		got := r.buf.String()
		r.mu.Unlock()
		if strings.Contains(got, s) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never reached the console; it got %d bytes ending %q",
				s, len(got), got[max(0, len(got)-300):])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A secondary console that stops taking output (Ctrl-S on a VT, XOFF on
// a serial line) used to stop PID 1: ServiceStarted writes the "[ OK ]"
// line to it with the scheduling lock held, and the first write to a
// stopped console never returned. The console's output is dropped while
// it is blocked, and it is told how much once it takes output again.
func TestConsoleDupStoppedConsoleDoesNotBlock(t *testing.T) {
	master, path, ctl := stoppedPTY(t)
	cons, err := OpenConsole(path)
	if err != nil {
		t.Fatalf("OpenConsole: %v", err)
	}
	defer cons.Close()

	l := New(LevelInfo)
	l.SetOutput(io.Discard)
	l.SetBootConsole(true, false)
	l.SetConsoleDup(cons)

	var done atomic.Int64
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 2000; i++ {
			l.ServiceStarted(fmt.Sprintf("svc-%04d", i))
			done.Add(1)
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatalf("logging to a stopped secondary console blocked after %d of 2000 service starts", done.Load())
	}

	if err := unix.IoctlSetInt(int(ctl.Fd()), unix.TCXONC, unix.TCOON); err != nil {
		t.Fatalf("TCOON: %v", err)
	}
	out := readPTY(master)
	l.ServiceStarted("after-resume")
	got := out.waitFor(t, "[ OK ] after-resume")
	if !strings.Contains(got, "bytes of output to it were dropped") {
		t.Errorf("the console was not told that output was dropped: %q", got)
	}
}

// On a console that takes its output, OpenConsole writes exactly what it
// is given.
func TestOpenConsoleHealthyIsUnchanged(t *testing.T) {
	master, path, ctl := stoppedPTY(t)
	if err := unix.IoctlSetInt(int(ctl.Fd()), unix.TCXONC, unix.TCOON); err != nil {
		t.Fatalf("TCOON: %v", err)
	}
	// Raw output so the bytes on the master are the bytes written.
	tio, err := unix.IoctlGetTermios(int(ctl.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	tio.Oflag &^= unix.OPOST
	if err := unix.IoctlSetTermios(int(ctl.Fd()), unix.TCSETS, tio); err != nil {
		t.Fatal(err)
	}
	cons, err := OpenConsole(path)
	if err != nil {
		t.Fatalf("OpenConsole: %v", err)
	}
	defer cons.Close()
	out := readPTY(master)

	l := New(LevelInfo)
	l.SetOutput(io.Discard)
	l.SetBootConsole(true, false)
	l.SetConsoleDup(cons)
	var want strings.Builder
	for i := 0; i < 500; i++ {
		name := fmt.Sprintf("svc-%04d", i)
		l.ServiceStarted(name)
		want.WriteString("[ OK ] " + name + "\r\n")
	}
	if got := out.waitFor(t, "svc-0499\r\n"); got != want.String() {
		t.Errorf("console got %d bytes, want %d; differs from what was written", len(got), want.Len())
	}
}

// The catch-all's drain used to write each line to the console before
// reading the next: with the console stopped it blocked, the 64 KiB pipe
// filled, and PID 1 blocked writing its own stderr — during boot and
// shutdown, where every "[ OK ]" line goes that way. The drain keeps
// reading now; lines the console cannot take are dropped and counted.
func TestCatchAllStoppedConsoleDoesNotBlockWriters(t *testing.T) {
	master, path, ctl := stoppedPTY(t)
	cons, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &CatchAllLogger{pipeR: pr, pipeW: pw, console: cons, consoleEOL: "\r\n"}
	c.wg.Add(1)
	go c.drain()

	l := New(LevelInfo)
	l.SetOutput(pw)
	l.SetBootConsole(true, false)
	var done atomic.Int64
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 20000; i++ {
			l.ServiceStarted(fmt.Sprintf("svc-%05d", i))
			done.Add(1)
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatalf("writing to the catch-all pipe blocked after %d of 20000 service starts", done.Load())
	}

	if err := unix.IoctlSetInt(int(ctl.Fd()), unix.TCXONC, unix.TCOON); err != nil {
		t.Fatalf("TCOON: %v", err)
	}
	out := readPTY(master)
	// What was queued while the console was stopped goes out first.
	for deadline := time.Now().Add(5 * time.Second); c.consoleQueued.Load() > 0; {
		if time.Now().After(deadline) {
			t.Fatal("the console tee did not catch up after the console resumed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.ServiceStarted("after-resume")
	got := out.waitFor(t, "[ OK ] after-resume")
	if !strings.Contains(got, "lines of output to it were dropped") {
		t.Errorf("the console was not told that lines were dropped: %q", got[max(0, len(got)-300):])
	}
	pw.Close()
	c.wg.Wait()
}

// A stopped console does not hold up Stop either: soft reboot and the
// exit path wait for it.
func TestCatchAllStopDoesNotWaitForAStoppedConsole(t *testing.T) {
	_, path, _ := stoppedPTY(t)
	cons, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &CatchAllLogger{pipeR: pr, pipeW: pw, console: cons, consoleEOL: "\r\n"}
	c.wg.Add(1)
	go c.drain()
	fmt.Fprintln(pw, "a line the console will not take")
	pw.Close()
	waited := make(chan struct{})
	go func() { c.wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the drain waited for a stopped console without end")
	}
}

// slowPipeConsole is a console read at bytesPerSec: a container runtime's
// stdout, or a serial line. It returns the write end and a channel that
// yields everything read once the write end is closed.
func slowPipeConsole(t *testing.T, bytesPerSec int) (*os.File, <-chan []byte) {
	t.Helper()
	cr, cw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cr.Close(); cw.Close() })
	got := make(chan []byte, 1)
	go func() {
		var all []byte
		buf := make([]byte, 4096)
		t0 := time.Now()
		for {
			m, err := cr.Read(buf)
			all = append(all, buf[:m]...)
			if err != nil {
				got <- all
				return
			}
			due := time.Duration(len(all)) * time.Second / time.Duration(bytesPerSec)
			if d := due - time.Since(t0); d > 0 {
				time.Sleep(d)
			}
		}
	}()
	return cw, got
}

// A working console slower than the workload's output - a container
// runtime's stdout (docker logs), a serial line - is not a stopped one.
// The console tee dropped every line past a full queue, so it lost most
// of a burst; the drain now waits for a console that keeps taking lines,
// as it did when it wrote to it itself, and the console gets every byte.
func TestCatchAllSlowHealthyConsoleIsLossless(t *testing.T) {
	cw, got := slowPipeConsole(t, 5_000_000)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &CatchAllLogger{pipeR: pr, pipeW: pw, console: cw, consoleEOL: "\n"}
	c.wg.Add(1)
	go c.drain()
	var want bytes.Buffer
	const lines = 50000
	for i := 0; i < lines; i++ {
		line := fmt.Sprintf("workload line %06d %s\n", i, strings.Repeat("x", 54))
		want.WriteString(line)
		io.WriteString(pw, line)
	}
	pw.Close()
	c.wg.Wait()
	cw.Close()
	out := <-got
	if !bytes.Equal(out, want.Bytes()) {
		t.Errorf("a slow but working console got %d of %d lines (%d of %d bytes)",
			bytes.Count(out, []byte{'\n'}), lines, len(out), want.Len())
	}
}

// Stop - soft reboot, exit - waits for a slow console to write
// what it is owed for at most catchAllConsoleFlushMax, however much was
// queued for it.
func TestCatchAllStopFlushIsBounded(t *testing.T) {
	cw, _ := slowPipeConsole(t, 11_520) // 115200 baud
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &CatchAllLogger{pipeR: pr, pipeW: pw, console: cw, consoleEOL: "\n"}
	c.wg.Add(1)
	go c.drain()
	wrote := make(chan struct{})
	go func() {
		// ~400 KB: about 35 s of output at this rate.
		for i := 0; i < 5000; i++ {
			fmt.Fprintf(pw, "shutdown line %05d %s\n", i, strings.Repeat("y", 60))
		}
		close(wrote)
	}()
	time.Sleep(200 * time.Millisecond)
	t0 := time.Now()
	c.beginFlush()
	<-wrote
	pw.Close()
	c.wg.Wait()
	took := time.Since(t0)
	t.Logf("Stop took %v", took.Round(time.Millisecond))
	if took > catchAllConsoleFlushMax+time.Second {
		t.Errorf("Stop waited %v for a slow console, past the %v bound", took, catchAllConsoleFlushMax)
	}
}
