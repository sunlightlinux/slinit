package service

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/process"
)

// Cron output used to go to /dev/null, and slinit's own log line on failure
// never reached the journal, so a nightly job left no record of what it did or
// why it failed. Now each line of the command's stdout and stderr becomes a
// journal event of the service, and every run ends with one summary event.
var (
	// cronOutputDrain bounds how long a run waits, after the command
	// ended, for the rest of its output. A daemon the command left behind
	// can hold the pipe open for ever; its reader then keeps going on its
	// own, so its lines still arrive, but the run is not held up.
	cronOutputDrain = time.Second
	// cronOutputMaxLines caps the lines one run sends to the journal. The
	// rest is read and dropped, never left in the pipe, so a chatty
	// command cannot block on a full pipe and cannot flood the ring
	// buffer either. One event says how many were dropped.
	cronOutputMaxLines = 10000
)

// cronOutputMaxLine is the longest line sent whole; a longer one is split.
const cronOutputMaxLine = 4096

// cronOutput collects one run's output. The write end goes to the child as
// both stdout and stderr; a real pipe (not an io.Writer) keeps os/exec from
// starting a copier that its Wait would then wait on.
type cronOutput struct {
	r, w *os.File
	done chan struct{}
	pid  atomic.Int64 // set once the child runs; read by the reader
}

func startCronOutput(cmd *exec.Cmd, name string) (*cronOutput, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	co := &cronOutput{r: r, w: w, done: make(chan struct{})}
	cmd.Stdout = w
	cmd.Stderr = w
	go co.read(name)
	return co, nil
}

// finish closes slinit's copy of the write end once the command has ended, so
// the reader sees EOF when the child's copies are gone too, and waits for it
// for at most cronOutputDrain.
func (co *cronOutput) finish() {
	_ = co.w.Close()
	select {
	case <-co.done:
	case <-time.After(cronOutputDrain):
	}
}

func (co *cronOutput) read(name string) {
	defer close(co.done)
	defer co.r.Close()
	br := bufio.NewReaderSize(co.r, cronOutputMaxLine)
	sent, dropped := 0, 0
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			line = trimEOL(line)
			if sent < cronOutputMaxLines {
				if len(line) > 0 {
					emitJournalLogLine(name, extractSyslogLevel(line), line, int(co.pid.Load()))
					sent++
				}
			} else {
				dropped++
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			break
		}
	}
	if dropped > 0 {
		journal.Emit(&journal.Event{
			Unit:      name,
			Prio:      journal.PriorityWarning,
			Transport: journal.TransportDriver,
			Msg: fmt.Sprintf("cron-command: %d more output lines dropped (limit %d per run)",
				dropped, cronOutputMaxLines),
			Fields: map[string]string{"SLINIT_EVENT": "cron-output-dropped"},
		})
	}
}

func trimEOL(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

// emitCronRunResult records how one run ended. stopped means the runner was
// being stopped and ended the command; that is not the command's failure.
func emitCronRunResult(name string, pid int, took time.Duration, err error, stopped bool) {
	fields := map[string]string{
		"SLINIT_EVENT":       "cron-run",
		"SLINIT_CRON_RESULT": "success",
	}
	if pid > 0 {
		fields["SLINIT_TARGET_PID"] = strconv.Itoa(pid)
	}
	prio := journal.PriorityInfo
	took = took.Round(time.Millisecond)
	msg := fmt.Sprintf("cron-command finished in %v", took)
	code, haveCode := process.ExitCodeOf(err)
	switch {
	case stopped:
		prio = journal.PriorityNotice
		fields["SLINIT_CRON_RESULT"] = "stopped"
		msg = fmt.Sprintf("cron-command ended by stop after %v", took)
	case err == nil:
	case haveCode && code >= 0:
		// Not errors.As(*exec.ExitError): when PID 1's reaper gets the
		// child first, RunAdhoc hands back its own error type. A signal
		// death has code -1 and falls through to the message below.
		prio = journal.PriorityError
		fields["SLINIT_CRON_RESULT"] = "exit-code"
		fields["SLINIT_EXIT_STATUS"] = strconv.Itoa(code)
		msg = fmt.Sprintf("cron-command failed with exit status %d after %v", code, took)
	default:
		prio = journal.PriorityError
		fields["SLINIT_CRON_RESULT"] = "error"
		msg = fmt.Sprintf("cron-command failed after %v: %v", took, err)
	}
	journal.Emit(&journal.Event{
		Unit:      name,
		Prio:      prio,
		Transport: journal.TransportDriver,
		Msg:       msg,
		Fields:    fields,
	})
}
