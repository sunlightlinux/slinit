package journal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// KmsgPath is the kernel ring-buffer device that produces one line
// per kernel log message. Reading it never blocks unless SEEK_END is
// used — the reader positions itself at the tail of the buffer
// (bypassing history that's already been aged out) and then blocks
// on read for new messages, like tail -f on a growing file.
const KmsgPath = "/dev/kmsg"

// KmsgReader consumes /dev/kmsg and emits each kernel message as an
// Event with Transport=kernel. Run in a background goroutine; call
// Stop or cancel the context to shut down cleanly.
//
// Line format per kernel documentation (Documentation/ABI/testing/dev-kmsg):
//
//	PRIORITY,SEQNUM,MICROSECONDS,FLAG[,KEY=VALUE...];MESSAGE
//	 CONTINUATION LINE (starts with space)
//
// Where:
//   - PRIORITY is the combined facility + severity (facility 0 = kern,
//     severity 0..7 as usual)
//   - SEQNUM is monotonic message index
//   - MICROSECONDS is monotonic time since boot × 1000
//   - FLAG is 'c' for a message continued to next line, '-' otherwise
//   - The optional KEY=VALUE pairs (rare; e.g. SUBSYSTEM= / DEVICE=)
//     are ignored for now — slinit-journalctl users rarely filter on
//     these. Room to extend in v2.x.
//   - MESSAGE is the free-form log text.
type KmsgReader struct {
	emitter *Emitter
	file    *os.File
	stop    chan struct{}
	done    chan struct{}
}

// NewKmsgReader opens /dev/kmsg for reading. Returns an error if the
// device is missing (unusual — kmsg has been available since Linux
// 3.5) or if we lack permission (need CAP_SYSLOG or read access; PID
// 1 has both). Callers that treat kmsg as optional should silently
// ignore ENOENT / EACCES here.
func NewKmsgReader(emitter *Emitter) (*KmsgReader, error) {
	if emitter == nil {
		return nil, errors.New("journal: kmsg reader needs emitter")
	}
	f, err := os.OpenFile(KmsgPath, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("journal: open %s: %w", KmsgPath, err)
	}

	// Default position on /dev/kmsg is the FIRST record still in the
	// kernel's ring buffer — reading from open replays the whole
	// current-boot dmesg and then blocks (EAGAIN → poll) for new
	// entries. This matches systemd-journald's --dmesg behaviour and
	// is what operators expect from `slinit-journalctl -k`.
	//
	// (The prior version seeked to SEEK_END here, which skipped the
	// entire boot log. Slinit starts after the kernel's noisy init
	// phase, so on a static system nothing NEW arrives to fill the
	// -k output — the buffer looked empty.)

	return &KmsgReader{
		emitter: emitter,
		file:    f,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// Run blocks reading kmsg until ctx is done or Stop is called. Each
// line is parsed and Emit'ed. Errors on individual lines are logged
// to stderr (no facility to plumb to slinit's logger from a package
// this deep) but don't stop the reader — kmsg occasionally emits
// binary noise during firmware handoff and we don't want to give up
// on the whole stream.
func (r *KmsgReader) Run(ctx context.Context) {
	defer close(r.done)

	// Use a scanner with generous buffer — kernel messages can be up
	// to LOG_LINE_MAX (1024 by default) but with continuation lines
	// might extend beyond.
	scanner := bufio.NewScanner(r.file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	// We can't set a read deadline on /dev/kmsg cleanly (it's not a
	// regular socket). Instead poll with a short timeout via
	// nonblocking reads + select on stop channels. The nonblocking
	// file open above means read returns EAGAIN when nothing is
	// available.
	pollTick := time.NewTicker(200 * time.Millisecond)
	defer pollTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		default:
		}

		if scanner.Scan() {
			line := scanner.Text()
			if evt := parseKmsgLine(line); evt != nil {
				_ = r.emitter.Emit(evt)
			}
			continue
		}
		if err := scanner.Err(); err != nil {
			// EAGAIN means no data yet; other errors we surface.
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, io.EOF) {
				// Reset scanner state — bufio.Scanner won't resume
				// after error unless we reconstitute it.
				scanner = bufio.NewScanner(r.file)
				scanner.Buffer(make([]byte, 64*1024), 64*1024)
				select {
				case <-ctx.Done():
					return
				case <-r.stop:
					return
				case <-pollTick.C:
					continue
				}
			}
			// Non-recoverable read error. Bail so caller sees Run
			// return; caller can restart if desired.
			fmt.Fprintf(os.Stderr, "journal: kmsg read error: %v\n", err)
			return
		}
		// scanner.Scan() returned false with no error → EOF-like
		// state on nonblocking fd. Wait for more data.
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-pollTick.C:
			scanner = bufio.NewScanner(r.file)
			scanner.Buffer(make([]byte, 64*1024), 64*1024)
		}
	}
}

// Stop signals Run to return. Idempotent — safe to call multiple
// times. Blocks until Run has fully exited so a caller can rely on
// no more Emit calls happening from this reader after Stop returns.
func (r *KmsgReader) Stop() {
	select {
	case <-r.stop:
		// already closed
	default:
		close(r.stop)
	}
	<-r.done
	_ = r.file.Close()
}

// parseKmsgLine turns one raw /dev/kmsg line into an Event. Continuation
// lines (starting with space or tab) are treated as their own entries
// for now — a follow-up can merge them with the previous entry's Msg
// once we track scanner state across calls. Returns nil for lines
// that fail to parse (silently dropped rather than emitting garbage).
func parseKmsgLine(line string) *Event {
	// Continuation lines start with whitespace — no header to parse.
	if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		return &Event{
			Msg:       strings.TrimSpace(line),
			Prio:      PriorityInfo,
			Transport: TransportKernel,
		}
	}

	// Split header/message on the first ';'.
	semi := strings.IndexByte(line, ';')
	if semi < 0 {
		return nil
	}
	header := line[:semi]
	msg := line[semi+1:]

	// Header is comma-separated: PRIORITY,SEQNUM,USEC,FLAG[,KV...]
	// We only need PRIORITY and USEC.
	parts := strings.Split(header, ",")
	if len(parts) < 3 {
		return nil
	}

	pri64, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return nil
	}
	sev := Priority(pri64 & 0x7)

	// USEC = monotonic microseconds since boot. Not used to overwrite
	// Ts (that's wall-clock) but could inform Mts if we care. For
	// now we let Emit stamp Mts freshly — the delta between kernel
	// boot and event should be within a few ms for near-real-time
	// events.
	_ = parts[2]

	return &Event{
		Msg:       msg,
		Prio:      sev,
		Transport: TransportKernel,
	}
}
