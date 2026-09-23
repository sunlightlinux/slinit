// Package metrics renders slinit's state in the Prometheus text
// exposition format, so a fleet can scrape what an operator would
// otherwise read off `slinitctl list` and `slinitctl boot-time`.
//
// Only numbers slinit already keeps are exposed. Nothing here samples,
// estimates or remembers anything of its own: every value is read from
// the service set at render time, which keeps the endpoint honest and
// costs nothing when nobody scrapes.
//
// Counters are the ones that never decrease. The set's restart log is
// deliberately not one of them — it is a one-hour window for the
// heartbeat's rate signal, and a scraper computing its own rate over a
// window that prunes itself would read drops that never happened.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Render writes the current metrics for ss. version is reported as a
// label on slinit_build_info, the usual way to make a build visible in
// a dashboard without inventing a metric per field.
func Render(w io.Writer, ss *service.ServiceSet, version string) error {
	var b strings.Builder

	metric(&b, "slinit_build_info", "gauge",
		"Always 1; the version is in the label.")
	fmt.Fprintf(&b, "slinit_build_info{version=\"%s\"} 1\n", escape(version))

	// --- boot ---------------------------------------------------------
	metric(&b, "slinit_boot_kernel_seconds", "gauge",
		"Time from power-on to slinit starting, as reported by the kernel.")
	fmt.Fprintf(&b, "slinit_boot_kernel_seconds %s\n", secs(ss.KernelUptime()))

	ready := !ss.BootReadyTime().IsZero()
	var userspace time.Duration
	if ready && !ss.BootStartTime().IsZero() {
		userspace = ss.BootReadyTime().Sub(ss.BootStartTime())
	}
	metric(&b, "slinit_boot_userspace_seconds", "gauge",
		"Time from slinit starting to the boot target being up; 0 until it is.")
	fmt.Fprintf(&b, "slinit_boot_userspace_seconds %s\n", secs(userspace))

	metric(&b, "slinit_boot_ready", "gauge",
		"1 once the boot target has started, 0 before that.")
	fmt.Fprintf(&b, "slinit_boot_ready %d\n", boolVal(ready))

	// --- services, in aggregate ---------------------------------------
	c := ss.CountByState()
	metric(&b, "slinit_services", "gauge",
		"Loaded services by state.")
	for _, s := range []struct {
		state string
		n     int
	}{
		{"started", c.Active},
		{"starting", c.Starting},
		{"stopping", c.Stopping},
		{"stopped", c.Stopped},
		{"failed", c.Failed},
	} {
		fmt.Fprintf(&b, "slinit_services{state=%q} %d\n", s.state, s.n)
	}

	// --- services, one by one -----------------------------------------
	//
	// Per-service series are what tells a fleet which service is
	// flapping, which is the question these metrics exist to answer.
	// The cardinality is bounded by the service files on the host.
	// Sorted by name: ListServices walks a map, and an endpoint whose
	// line order changes between scrapes is unreadable by hand and
	// untestable by machine.
	svcs := ss.ListServices()
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name() < svcs[j].Name() })

	metric(&b, "slinit_service_up", "gauge",
		"1 while the service is started, 0 otherwise.")
	for _, svc := range svcs {
		fmt.Fprintf(&b, "slinit_service_up{service=\"%s\"} %d\n",
			escape(svc.Name()), boolVal(svc.State() == service.StateStarted))
	}

	metric(&b, "slinit_service_failed", "gauge",
		"1 while the service's last start attempt failed.")
	for _, svc := range svcs {
		fmt.Fprintf(&b, "slinit_service_failed{service=\"%s\"} %d\n",
			escape(svc.Name()), boolVal(svc.Record().DidStartFail()))
	}

	metric(&b, "slinit_service_startup_seconds", "gauge",
		"How long the service took to reach STARTED on its last start.")
	for _, svc := range svcs {
		fmt.Fprintf(&b, "slinit_service_startup_seconds{service=\"%s\"} %s\n",
			escape(svc.Name()), secs(svc.Record().StartupDuration()))
	}

	metric(&b, "slinit_service_restarts_total", "counter",
		"Supervisor-driven restarts of this service since slinit started.")
	for _, svc := range svcs {
		fmt.Fprintf(&b, "slinit_service_restarts_total{service=\"%s\"} %d\n",
			escape(svc.Name()), svc.Record().RestartCount())
	}

	// --- daemon-wide counters -----------------------------------------
	metric(&b, "slinit_restarts_total", "counter",
		"Supervisor-driven restarts across every service since slinit started.")
	fmt.Fprintf(&b, "slinit_restarts_total %d\n", ss.RestartsTotal())

	metric(&b, "slinit_watchdog_restarts_total", "counter",
		"Restarts caused by a service missing its watchdog deadline.")
	fmt.Fprintf(&b, "slinit_watchdog_restarts_total %d\n", ss.WatchdogMisses())

	_, err := io.WriteString(w, b.String())
	return err
}

// metric writes the HELP and TYPE lines a scraper reads before the
// samples. Prometheus tolerates their absence; humans reading a raw
// /metrics do not.
func metric(b *strings.Builder, name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// secs renders a duration in seconds, the only unit Prometheus wants.
func secs(d time.Duration) string {
	return fmt.Sprintf("%.6g", d.Seconds())
}

func boolVal(v bool) int {
	if v {
		return 1
	}
	return 0
}

// escape quotes a label value per the exposition format: backslash,
// double quote and newline. A service name is operator-supplied — it
// comes from a file name — so it cannot be trusted to be clean.
func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}
