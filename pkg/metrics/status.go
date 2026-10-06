package metrics

import (
	"encoding/json"
	"io"
	"sort"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// The HTTP endpoint served only /metrics, in the Prometheus text
// format, and slinitctl has no JSON mode. So reading slinit's state
// from a script, or from a container that ships no slinitctl, meant
// either parsing the exposition format or parsing `slinitctl status`
// output that exists to be read by people.
//
// /status answers the same question as `slinitctl list` in a form a
// script can take:
//
//	curl --unix-socket /run/slinit/metrics.sock http:/status | jq
//
// Deliberately the same shape as the metrics endpoint: read from the
// service set at request time, remember nothing, sample nothing. It is
// a view, not a second source of truth, and every field here already
// exists for `slinitctl` or the exposition format — nothing is invented
// to fill the JSON out.

// StatusDoc is the /status document. Field names are lowercase and
// stable: a scraper or script that reads them is a consumer of this
// surface, so they follow STABILITY.md like any other.
type StatusDoc struct {
	Version   string `json:"version"`
	BootReady bool   `json:"boot_ready"`
	// Counts mirrors the same five buckets the exposition format uses,
	// named the way `slinitctl list` prints them.
	Counts   StatusCounts    `json:"counts"`
	Services []StatusService `json:"services"`
}

// StatusCounts is the per-state tally.
type StatusCounts struct {
	Started  int `json:"started"`
	Starting int `json:"starting"`
	Stopping int `json:"stopping"`
	Stopped  int `json:"stopped"`
	Failed   int `json:"failed"`
}

// StatusService is one service's line.
type StatusService struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Type      string `json:"type"`
	PID       int    `json:"pid,omitempty"`
	Restarts  int    `json:"restarts"`
	StartFail bool   `json:"start_failed,omitempty"`
	// UptimeSeconds is how long the service has been STARTED, omitted
	// when it is not. Seconds rather than a timestamp because the
	// consumer is a shell reading one number, and because slinit's own
	// boot-time clock guard means an absolute time can move under it.
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
}

// BuildStatus reads the current state. Separated from the HTTP handler
// so it can be tested without a socket, and so the document is one
// function rather than scattered through response writing.
func BuildStatus(ss *service.ServiceSet, version string, now time.Time) StatusDoc {
	doc := StatusDoc{
		Version:   version,
		BootReady: !ss.BootReadyTime().IsZero(),
	}
	c := ss.CountByState()
	doc.Counts = StatusCounts{
		Started:  c.Active,
		Starting: c.Starting,
		Stopping: c.Stopping,
		Stopped:  c.Stopped,
		Failed:   c.Failed,
	}

	for _, svc := range ss.ListServices() {
		rec := svc.Record()
		s := StatusService{
			Name:      svc.Name(),
			State:     svc.State().String(),
			Type:      rec.Type().String(),
			Restarts:  int(rec.RestartCount()),
			StartFail: rec.DidStartFail(),
		}
		if pid := svc.PID(); pid > 0 {
			s.PID = pid
		}
		if svc.State() == service.StateStarted {
			if started := rec.StartedTime(); !started.IsZero() {
				if d := now.Sub(started); d > 0 {
					s.UptimeSeconds = int64(d / time.Second)
				}
			}
		}
		doc.Services = append(doc.Services, s)
	}
	// Sorted so a diff between two polls is a diff in the state and not
	// in the map iteration order ListServices inherits.
	sort.Slice(doc.Services, func(i, j int) bool {
		return doc.Services[i].Name < doc.Services[j].Name
	})
	return doc
}

// WriteStatus renders the document as indented JSON. Indented because
// the reader is as often a person with curl as a script with jq, and
// jq does not care either way.
func WriteStatus(w io.Writer, ss *service.ServiceSet, version string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(BuildStatus(ss, version, time.Now()))
}
