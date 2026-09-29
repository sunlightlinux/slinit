package main

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// svgRect and svgDoc pull the geometry back out of the rendered file, so
// the tests can assert where a bar landed rather than only that its name
// appears somewhere in the text.
type svgRect struct {
	Class string  `xml:"class,attr"`
	X     float64 `xml:"x,attr"`
	Y     float64 `xml:"y,attr"`
	W     float64 `xml:"width,attr"`
}

type svgDoc struct {
	Rects []svgRect `xml:"rect"`
	Texts []string  `xml:"text"`
}

func parseSVG(t *testing.T, svg string) svgDoc {
	t.Helper()
	var doc svgDoc
	if err := xml.Unmarshal([]byte(svg), &doc); err != nil {
		t.Fatalf("rendered SVG is not well-formed XML: %v", err)
	}
	return doc
}

// plotSample is a boot that took 900ms of userspace after 550ms of
// kernel, with three services at known offsets.
func plotSample(base int64) control.BootTimeInfo {
	ms := func(n int) int64 { return int64(time.Duration(n) * time.Millisecond) }
	return control.BootTimeInfo{
		KernelUptimeNs: ms(550),
		BootStartNs:    base,
		BootReadyNs:    base + ms(900),
		BootSvcName:    "boot",
		Services: []control.BootTimeEntry{
			{Name: "late", State: service.StateStarted,
				StartReqNs: base + ms(600), StartedNs: base + ms(880)},
			{Name: "early", State: service.StateStarted,
				StartReqNs: base + ms(10), StartedNs: base + ms(60)},
			{Name: "middle", State: service.StateStarted,
				StartReqNs: base + ms(100), StartedNs: base + ms(590)},
		},
	}
}

func TestRenderBootPlotWellFormed(t *testing.T) {
	svg, err := renderBootPlot(plotSample(time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if !strings.HasPrefix(svg, `<?xml version="1.0"`) {
		t.Errorf("missing XML declaration, got %.40q", svg)
	}
	if !strings.HasSuffix(svg, "</svg>\n") {
		t.Errorf("SVG not closed, tail is %q", svg[len(svg)-20:])
	}

	doc := parseSVG(t, svg)
	joined := strings.Join(doc.Texts, "\n")
	for _, want := range []string{"early", "middle", "late", "kernel"} {
		if !strings.Contains(joined, want) {
			t.Errorf("label %q missing from the plot", want)
		}
	}
	if !strings.Contains(joined, "1.450s") {
		t.Errorf("header should total kernel+userspace as 1.450s, got:\n%s", joined)
	}
}

// TestRenderBootPlotGeometry is the test that matters: a timeline is
// only useful if position encodes time. A bar chart sorted by name would
// pass every string assertion above.
func TestRenderBootPlotGeometry(t *testing.T) {
	svg, err := renderBootPlot(plotSample(time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	doc := parseSVG(t, svg)

	var lanes []svgRect
	var kernel *svgRect
	for i, r := range doc.Rects {
		switch r.Class {
		case "activating":
			lanes = append(lanes, r)
		case "kernel":
			kernel = &doc.Rects[i]
		}
	}
	if len(lanes) != 3 {
		t.Fatalf("expected 3 service bars, got %d", len(lanes))
	}
	if kernel == nil {
		t.Fatal("a fresh boot should get a kernel lane")
	}
	if kernel.X != plotLeftPad {
		t.Errorf("kernel lane should start at the axis (%d), got %.1f", plotLeftPad, kernel.X)
	}

	// Lanes are emitted top to bottom in start order.
	for i := 1; i < len(lanes); i++ {
		if lanes[i].Y <= lanes[i-1].Y {
			t.Errorf("lane %d is not below lane %d (%.1f vs %.1f)", i, i-1, lanes[i].Y, lanes[i-1].Y)
		}
		if lanes[i].X < lanes[i-1].X {
			t.Errorf("lanes are not ordered by start time: %.1f then %.1f", lanes[i-1].X, lanes[i].X)
		}
	}

	// "middle" is asked to start before "late" but takes longer, so it
	// must be the wider bar despite starting further left. That ordering
	// is exactly what the list form of `analyze time` cannot show.
	early, middle, late := lanes[0], lanes[1], lanes[2]
	if !(middle.W > late.W && late.W > early.W) {
		t.Errorf("bar widths do not track durations: early=%.1f middle=%.1f late=%.1f",
			early.W, middle.W, late.W)
	}
	if late.X <= middle.X+middle.W-1 {
		t.Errorf("'late' starts at %.1f, which is not after 'middle' ends at %.1f",
			late.X, middle.X+middle.W)
	}
	// Nothing may escape the plot area.
	for _, r := range lanes {
		if r.X < plotLeftPad || r.X+r.W > plotLeftPad+plotWidth+0.5 {
			t.Errorf("bar escapes the axis: x=%.1f w=%.1f", r.X, r.W)
		}
	}
}

// A service started after the boot target is an operator action, not
// part of the boot; it is dropped so it cannot stretch the axis, and the
// header says how many went.
func TestRenderBootPlotExcludesPostBoot(t *testing.T) {
	base := time.Now().UnixNano()
	info := plotSample(base)
	info.Services = append(info.Services, control.BootTimeEntry{
		Name:       "restarted-days-later",
		State:      service.StateStarted,
		StartReqNs: base + int64(72*time.Hour),
		StartedNs:  base + int64(72*time.Hour) + int64(time.Second),
	})

	svg, err := renderBootPlot(info)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if strings.Contains(svg, "restarted-days-later") {
		t.Error("a service started after the boot target must not be plotted")
	}
	joined := strings.Join(parseSVG(t, svg).Texts, "\n")
	if !strings.Contains(joined, "3 services in the boot window") {
		t.Errorf("header should count the plotted services, got:\n%s", joined)
	}
	if !strings.Contains(joined, "1 started after the boot target") {
		t.Errorf("header should account for what was left out, got:\n%s", joined)
	}
}

// While the boot is still running there is no target to cut at, so
// everything is plotted — that is when the picture is worth the most.
func TestRenderBootPlotDuringBoot(t *testing.T) {
	base := time.Now().UnixNano()
	info := plotSample(base)
	info.BootReadyNs = 0
	info.Services = append(info.Services, control.BootTimeEntry{
		Name:       "stuck",
		State:      service.StateStarting,
		StartReqNs: base + int64(120*time.Millisecond),
	})

	svg, err := renderBootPlot(info)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	doc := parseSVG(t, svg)
	joined := strings.Join(doc.Texts, "\n")
	if !strings.Contains(joined, "stuck (starting)") {
		t.Errorf("a service still starting should be labelled as such, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Startup in progress") {
		t.Errorf("header should say the boot is unfinished, got:\n%s", joined)
	}

	var open int
	for _, r := range doc.Rects {
		if r.Class == "starting" {
			open++
			if r.X+r.W < plotLeftPad+plotWidth*0.9 {
				t.Errorf("an unfinished bar should run to the end of the data, got x=%.1f w=%.1f", r.X, r.W)
			}
		}
	}
	if open != 1 {
		t.Errorf("expected exactly one open-ended bar, got %d", open)
	}
}

// After a soft reboot the kernel figure belongs to a boot that may be
// weeks old, so it must not be drawn as if it ran just before this
// generation started.
func TestRenderBootPlotSoftRebootHasNoKernelLane(t *testing.T) {
	info := plotSample(time.Now().UnixNano())
	info.SoftReboots = 2
	info.StartUptimeNs = int64(6 * time.Hour)

	svg, err := renderBootPlot(info)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	for _, r := range parseSVG(t, svg).Rects {
		if r.Class == "kernel" {
			t.Error("a soft-rebooted generation must not draw a kernel lane")
		}
	}
	joined := strings.Join(parseSVG(t, svg).Texts, "\n")
	if !strings.Contains(joined, "Soft reboot #2") {
		t.Errorf("header should name the generation, got:\n%s", joined)
	}
	if strings.Contains(joined, "(kernel)") {
		t.Errorf("header should not claim a kernel figure for this boot, got:\n%s", joined)
	}
}

// Talking to a daemon that predates the instants: it still sends
// durations, so the failure must name that rather than claim nothing has
// started.
func TestRenderBootPlotOldDaemon(t *testing.T) {
	info := control.BootTimeInfo{
		BootStartNs: time.Now().UnixNano(),
		BootSvcName: "boot",
		Services: []control.BootTimeEntry{
			{Name: "hello", StartupNs: int64(30 * time.Millisecond),
				State: service.StateStarted},
		},
	}
	_, err := renderBootPlot(info)
	if err == nil {
		t.Fatal("expected an error when the daemon sends no instants")
	}
	if !strings.Contains(err.Error(), "predates") {
		t.Errorf("error should explain the daemon is too old, got: %v", err)
	}
}

func TestRenderBootPlotNothingStarted(t *testing.T) {
	info := control.BootTimeInfo{
		BootStartNs: time.Now().UnixNano(),
		BootSvcName: "boot",
		Services:    []control.BootTimeEntry{{Name: "idle"}},
	}
	_, err := renderBootPlot(info)
	if err == nil || !strings.Contains(err.Error(), "no service has been started") {
		t.Fatalf("expected a 'nothing started' error, got: %v", err)
	}
}

// Service names come from filenames and may contain XML metacharacters.
// An unescaped '&' turns the whole plot into an unopenable file.
func TestRenderBootPlotEscapesNames(t *testing.T) {
	base := time.Now().UnixNano()
	info := plotSample(base)
	info.Services[0].Name = `a&b<c>"d"`

	svg, err := renderBootPlot(info)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if strings.Contains(svg, `a&b`) {
		t.Error("'&' in a service name was written raw into the SVG")
	}
	joined := strings.Join(parseSVG(t, svg).Texts, "\n")
	if !strings.Contains(joined, `a&b<c>"d"`) {
		t.Errorf("name did not survive escaping, got:\n%s", joined)
	}
}

func TestPlotGridStep(t *testing.T) {
	cases := []struct {
		span time.Duration
		want time.Duration
	}{
		{300 * time.Millisecond, 50 * time.Millisecond},
		{1500 * time.Millisecond, 200 * time.Millisecond},
		{12 * time.Second, 2 * time.Second},
		{90 * time.Second, 10 * time.Second},
		{40 * time.Minute, 5 * time.Minute},
	}
	for _, c := range cases {
		got := time.Duration(plotGridStep(int64(c.span)))
		if got != c.want {
			t.Errorf("span %s: got step %s, want %s", c.span, got, c.want)
		}
		if n := c.span / got; n > 10 {
			t.Errorf("span %s: %d rules is too many", c.span, n)
		}
	}
}

// A user-mode manager reports the machine's uptime as "kernel", so a
// session started an hour into uptime claims an hour of kernel time. Drawn
// to scale that leaves every service a sliver, so the lane is dropped —
// but the figure still has to appear, or the plot looks like it lost data.
func TestRenderBootPlotDisproportionateKernel(t *testing.T) {
	info := plotSample(time.Now().UnixNano())
	info.KernelUptimeNs = int64(36 * time.Minute)

	svg, err := renderBootPlot(info)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	doc := parseSVG(t, svg)
	for _, r := range doc.Rects {
		if r.Class == "kernel" {
			t.Error("a kernel figure out of proportion must not be drawn to scale")
		}
	}
	joined := strings.Join(doc.Texts, "\n")
	if !strings.Contains(joined, "36m 0s") && !strings.Contains(joined, "2160.000s") {
		t.Errorf("the kernel figure must still be reported, got:\n%s", joined)
	}

	// With the kernel lane gone the services must use the whole axis
	// instead of being crushed against the left edge.
	var widest float64
	for _, r := range doc.Rects {
		if r.Class == "activating" && r.W > widest {
			widest = r.W
		}
	}
	if widest < plotWidth*0.25 {
		t.Errorf("services are still compressed: widest bar is %.1fpx of %d", widest, plotWidth)
	}
}

// The counterpart: a genuinely slow kernel on a fast userspace is real,
// and must keep its lane.
func TestRenderBootPlotSlowKernelKeepsLane(t *testing.T) {
	info := plotSample(time.Now().UnixNano())
	info.KernelUptimeNs = int64(2500 * time.Millisecond) // ~2.7x userspace

	svg, err := renderBootPlot(info)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	var found bool
	for _, r := range parseSVG(t, svg).Rects {
		if r.Class == "kernel" {
			found = true
		}
	}
	if !found {
		t.Error("a slow but proportionate kernel figure should keep its lane")
	}
}
