package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// timerRow is one line of list-timers: a loaded service with
// start-on-calendar.
type timerRow struct {
	name, expr string
	next, last time.Time
	persistent bool
	delay      string
}

// cmdListTimers is systemctl list-timers for start-on-calendar: every
// loaded service with one, by the time it next runs. The next time is
// reckoned here from the expression (a randomized delay comes on top);
// the last start is known for a persistent one, whose record slinit
// keeps.
func cmdListTimers(conn net.Conn, now time.Time, user bool) error {
	if user {
		// A user daemon keeps its records under the user's state
		// directory, not the system's.
		if d := service.UserCronPersistDir(); d != "" {
			service.SetCronPersistDir(d)
		}
	}
	// One buffered reader for the whole exchange: the list reply is a
	// packet per service, and the shows follow on the same stream.
	conn = bufferedConn{conn, bufio.NewReaderSize(conn, 64<<10)}
	if err := control.WritePacket(conn, control.CmdListServices, nil); err != nil {
		return err
	}
	var names []string
	for {
		rply, payload, err := readReply(conn)
		if err != nil {
			return err
		}
		if rply == control.RplyListDone {
			break
		}
		if rply != control.RplySvcInfo {
			return fmt.Errorf("unexpected reply: %d", rply)
		}
		entry, _, err := control.DecodeSvcInfo(payload)
		if err != nil {
			return err
		}
		names = append(names, entry.Name)
	}
	var rows []timerRow
	for _, name := range names {
		body, err := showBody(conn, name)
		if errors.Is(err, errNotLoaded) {
			continue // unloaded meanwhile
		}
		if err != nil {
			return err
		}
		if r, ok := timerRowFrom(name, body, now); ok {
			rows = append(rows, r)
		}
	}
	printTimers(os.Stdout, rows, now)
	return nil
}

var errNotLoaded = errors.New("not loaded")

// showBody shows a loaded service. It finds the service rather than
// loads it: a listing must not load from disk one another client
// unloaded since.
func showBody(conn net.Conn, name string) (string, error) {
	if err := control.WritePacket(conn, control.CmdFindService, control.EncodeServiceName(name)); err != nil {
		return "", err
	}
	rply, payload, err := readReply(conn)
	if err != nil {
		return "", err
	}
	if rply == control.RplyNoService {
		return "", errNotLoaded
	}
	if rply != control.RplyServiceRecord || len(payload) < 5 {
		return "", fmt.Errorf("find %s: unexpected reply %d", name, rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])
	if err := control.WritePacket(conn, control.CmdServiceShow, control.EncodeHandle(handle)); err != nil {
		return "", err
	}
	rply, payload, err = readReply(conn)
	if err != nil {
		return "", err
	}
	if rply != control.RplyServiceShow {
		return "", fmt.Errorf("show: unexpected reply %d", rply)
	}
	return string(payload), nil
}

// timerRowFrom reads a show body; ok is false for a service with no
// start-on-calendar.
func timerRowFrom(name, body string, now time.Time) (timerRow, bool) {
	r := timerRow{name: name}
	for _, line := range strings.Split(body, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "OnCalendar":
			r.expr = v
		case "OnCalendarPersistent":
			r.persistent = v == "yes"
		case "OnCalendarRandomizedDelay":
			r.delay = v
		}
	}
	if r.expr == "" {
		return r, false
	}
	if spec, err := service.ParseCalendar(r.expr); err == nil {
		r.next = spec.NextAfter(now)
	}
	if r.persistent {
		r.last, _ = service.ReadCalendarStamp(name)
	}
	return r, true
}

func printTimers(w io.Writer, rows []timerRow, now time.Time) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No loaded service has start-on-calendar.")
		return
	}
	// By next run; one with none (an expression past its last match) last.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].next, rows[j].next
		if a.IsZero() != b.IsZero() {
			return b.IsZero()
		}
		return a.Before(b)
	})
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NEXT\tLEFT\tLAST\tSERVICE\tCALENDAR")
	for _, r := range rows {
		next, left := "-", "-"
		if !r.next.IsZero() {
			next = r.next.Format("Mon 2006-01-02 15:04:05")
			left = roughDuration(r.next.Sub(now))
			if r.delay != "" {
				left += " (+ up to " + r.delay + ")"
			}
		}
		last := "-"
		if !r.last.IsZero() {
			last = r.last.Local().Format("Mon 2006-01-02 15:04:05")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", next, left, last, r.name, r.expr)
	}
	tw.Flush()
}

// roughDuration is a duration to the minute, as list-timers shows it:
// "2d 3h", "5h 12min", "40s".
func roughDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	days := int(d / (24 * time.Hour))
	h := int(d/time.Hour) % 24
	m := int(d/time.Minute) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dmin", h, m)
	}
	return fmt.Sprintf("%dmin", m)
}
