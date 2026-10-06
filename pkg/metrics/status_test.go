package metrics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// The HTTP endpoint served only the Prometheus text format and
// slinitctl has no JSON mode, so reading slinit's state from a script —
// or from a container that ships no slinitctl — meant parsing output
// written for people. /status answers it in a form jq can take.
func TestStatusDocumentReportsTheServices(t *testing.T) {
	set := service.NewServiceSet(nopLogger{})
	a := service.NewInternalService(set, "alpha")
	b := service.NewInternalService(set, "beta")
	set.AddService(a)
	set.AddService(b)
	set.StartService(a)

	var buf strings.Builder
	if err := WriteStatus(&buf, set, "v9.9.9"); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}

	var doc StatusDoc
	if err := json.Unmarshal([]byte(buf.String()), &doc); err != nil {
		t.Fatalf("the endpoint did not emit valid JSON: %v\n%s", err, buf.String())
	}

	if doc.Version != "v9.9.9" {
		t.Errorf("version = %q, want v9.9.9", doc.Version)
	}
	if len(doc.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(doc.Services))
	}
	// Sorted, so two polls differ only where the state differs.
	if doc.Services[0].Name != "alpha" || doc.Services[1].Name != "beta" {
		t.Errorf("services are not sorted by name: %+v", doc.Services)
	}
	if doc.Services[0].State != "STARTED" {
		t.Errorf("alpha state = %q, want STARTED", doc.Services[0].State)
	}
	if doc.Counts.Started != 1 {
		t.Errorf("counts.started = %d, want 1", doc.Counts.Started)
	}
	if doc.Counts.Stopped != 1 {
		t.Errorf("counts.stopped = %d, want 1", doc.Counts.Stopped)
	}
}

// uptime_seconds is reported for a started service and omitted
// otherwise, so a consumer never reads a zero that means "not running"
// as "running for no time".
func TestStatusUptimeOnlyForStartedServices(t *testing.T) {
	set := service.NewServiceSet(nopLogger{})
	svc := service.NewInternalService(set, "up")
	set.AddService(svc)
	set.StartService(svc)

	doc := BuildStatus(set, "v1", time.Now().Add(90*time.Second))
	if len(doc.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(doc.Services))
	}
	if doc.Services[0].UptimeSeconds < 89 {
		t.Errorf("uptime_seconds = %d, want about 90",
			doc.Services[0].UptimeSeconds)
	}

	set.StopService(svc)
	doc = BuildStatus(set, "v1", time.Now())
	if doc.Services[0].UptimeSeconds != 0 {
		t.Errorf("a stopped service reported uptime_seconds = %d",
			doc.Services[0].UptimeSeconds)
	}
	// And it must be absent from the JSON rather than present as 0.
	var buf strings.Builder
	if err := WriteStatus(&buf, set, "v1"); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	if strings.Contains(buf.String(), "uptime_seconds") {
		t.Errorf("uptime_seconds emitted for a stopped service:\n%s", buf.String())
	}
}

// An empty set must still be a valid document, not null or a crash: a
// scraper polling a rescue boot should read zeroes, not an error.
func TestStatusOnAnEmptySet(t *testing.T) {
	set := service.NewServiceSet(nopLogger{})
	var buf strings.Builder
	if err := WriteStatus(&buf, set, "v1"); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	var doc StatusDoc
	if err := json.Unmarshal([]byte(buf.String()), &doc); err != nil {
		t.Fatalf("invalid JSON for an empty set: %v\n%s", err, buf.String())
	}
	if doc.Counts.Started != 0 {
		t.Errorf("counts.started = %d on an empty set", doc.Counts.Started)
	}
}

type nopLogger struct{}

func (nopLogger) ServiceStarted(string)        {}
func (nopLogger) ServiceStopped(string)        {}
func (nopLogger) ServiceFailed(string, bool)   {}
func (nopLogger) Error(string, ...interface{}) {}
func (nopLogger) Info(string, ...interface{})  {}
