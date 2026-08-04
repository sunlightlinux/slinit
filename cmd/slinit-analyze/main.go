// slinit-analyze — post-boot timeline analysis for slinit
// (systemd-analyze parity, scoped to the queries most operators run
// after a slow boot).
//
// Subcommands:
//
//	time    total userspace boot time + time-to-boot-STARTED
//	blame   per-service time from boot start to STARTED, sorted desc
//
// Data source: slinit's control socket (CmdJournalQuery) — same wire
// slinit-journalctl uses. Filters client-side for state-transition
// events (Fields["SLINIT_EVENT"] == "STARTED") emitted by
// ProcessService's notifyListeners → emitJournalStateEvent path.
//
// Scope note: `critical-chain` and `plot` (SVG timeline) are
// intentionally out of scope for MVP — both need dependency-graph
// info that isn't in the journal today. They'd need to reparse
// /etc/slinit.d service files (like slinit-check does), which is a
// separate ~300 LOC of grammar work.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/journal"
)

const defaultSocket = "/run/slinit.socket"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "time":
		os.Exit(runTime(args))
	case "blame":
		os.Exit(runBlame(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "slinit-analyze: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `slinit-analyze — boot-time timeline analysis (systemd-analyze parity)

Usage:
  slinit-analyze time    [--socket=PATH]
  slinit-analyze blame   [--socket=PATH] [-n N]

Subcommands:
  time    Total userspace boot span + time-to-boot-STARTED.
  blame   Per-service time from boot start to STARTED, sorted desc.
          -n N caps output to the N slowest services (0 = all).

Common flags:
  --socket=PATH   slinit control socket (default /run/slinit.socket)

Note: critical-chain and plot (SVG timeline) are not implemented —
they require dependency-graph info not present in the journal.
`)
}

// fetchEvents opens the control socket, queries all buffered journal
// entries, and returns them. Same wire slinit-journalctl uses; no
// server-side filter — we filter for SLINIT_EVENT client-side because
// the current CmdJournalQuery schema doesn't expose a Fields matcher.
func fetchEvents(sockPath string) ([]*journal.Event, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", sockPath, err)
	}
	defer conn.Close()
	payload, err := json.Marshal(control.JournalQueryRequest{})
	if err != nil {
		return nil, err
	}
	if err := control.WritePacket(conn, control.CmdJournalQuery, payload); err != nil {
		return nil, fmt.Errorf("send query: %w", err)
	}
	var events []*journal.Event
	for {
		typ, body, err := control.ReadPacket(conn)
		if err != nil {
			return nil, fmt.Errorf("read reply: %w", err)
		}
		switch typ {
		case control.RplyJournalEntry:
			e, err := journal.UnmarshalEvent(body)
			if err != nil {
				return nil, fmt.Errorf("decode entry: %w", err)
			}
			events = append(events, e)
		case control.RplyJournalDone:
			return events, nil
		case control.RplyJournalErr:
			return nil, fmt.Errorf("server: %s", string(body))
		case control.RplyBadReq:
			return nil, errors.New("server rejected CmdJournalQuery (older slinit without journal support?)")
		default:
			return nil, fmt.Errorf("unexpected reply type %d", typ)
		}
	}
}

// serviceStartedEvents keeps only slinit-internal SLINIT_EVENT=STARTED
// records and returns them sorted by monotonic timestamp (earliest
// first). Mts (monotonic) is preferred over Ts (wall) because boot
// analysis must survive NTP-induced clock jumps mid-boot.
func serviceStartedEvents(events []*journal.Event) []*journal.Event {
	var started []*journal.Event
	for _, e := range events {
		if e.Fields["SLINIT_EVENT"] != "STARTED" {
			continue
		}
		started = append(started, e)
	}
	sort.SliceStable(started, func(i, j int) bool {
		return started[i].Mts < started[j].Mts
	})
	return started
}

func runTime(args []string) int {
	fs := flag.NewFlagSet("time", flag.ExitOnError)
	sock := fs.String("socket", defaultSocket, "slinit control socket")
	_ = fs.Parse(args)

	events, err := fetchEvents(*sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(events) == 0 {
		fmt.Println("No events available (journal empty or slinit just started?).")
		return 0
	}
	first, last := events[0].Mts, events[0].Mts
	for _, e := range events {
		if e.Mts < first {
			first = e.Mts
		}
		if e.Mts > last {
			last = e.Mts
		}
	}
	total := time.Duration(last - first)
	fmt.Printf("Userspace event span:  %v (%d events)\n", total.Round(time.Millisecond), len(events))
	started := serviceStartedEvents(events)
	for _, e := range started {
		if e.Unit == "boot" {
			bootTime := time.Duration(e.Mts - first)
			fmt.Printf("Time to boot=STARTED:  %v\n", bootTime.Round(time.Millisecond))
			break
		}
	}
	if len(started) > 0 {
		lastStarted := started[len(started)-1]
		lastTime := time.Duration(lastStarted.Mts - first)
		fmt.Printf("Last service STARTED:  %v (%s)\n", lastTime.Round(time.Millisecond), lastStarted.Unit)
	}
	return 0
}

func runBlame(args []string) int {
	fs := flag.NewFlagSet("blame", flag.ExitOnError)
	sock := fs.String("socket", defaultSocket, "slinit control socket")
	top := fs.Int("n", 0, "show only top-N slowest (0 = all)")
	_ = fs.Parse(args)

	events, err := fetchEvents(*sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	started := serviceStartedEvents(events)
	if len(started) == 0 {
		fmt.Println("No service-started events available.")
		return 0
	}
	// Metric: cumulative time from boot start (first STARTED) to each
	// service's own STARTED. Not identical to systemd's blame (which
	// measures individual unit activation time) — slinit doesn't emit
	// a STARTING event today, so we can't compute per-svc activation
	// duration. Cumulative still surfaces the slowest-to-boot svcs.
	firstTs := started[0].Mts
	type row struct {
		name    string
		elapsed time.Duration
	}
	rows := make([]row, 0, len(started))
	for _, e := range started {
		rows = append(rows, row{e.Unit, time.Duration(e.Mts - firstTs)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].elapsed > rows[j].elapsed })
	limit := len(rows)
	if *top > 0 && *top < limit {
		limit = *top
	}
	for i := 0; i < limit; i++ {
		fmt.Printf("%12v  %s\n", rows[i].elapsed.Round(time.Millisecond), rows[i].name)
	}
	return 0
}
