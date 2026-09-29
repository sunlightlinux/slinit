package main

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// Layout constants, in SVG user units (px at 1:1 zoom).
const (
	plotLeftPad  = 70   // the "0s" axis label sits left of the axis
	plotWidth    = 1000 // the time axis itself
	plotMinPad   = 90   // right margin even when every label is short
	plotRightPad = 440  // and the most one is allowed to grow to
	plotRowH     = 20
	plotBarH     = 12
	plotLineH    = 18  // one header line
	plotCharW    = 6.2 // DejaVu Sans Mono at 11px, for label overflow checks

	// kernelLaneRatio is how many times longer than userspace the kernel
	// span may be before it stops being drawn. At 3 the kernel takes at
	// most three quarters of the axis and the services stay legible.
	kernelLaneRatio = 3
)

// laneDraw is one rendered bar, computed before anything is written so
// the canvas can be sized to the labels it actually has to hold.
type laneDraw struct {
	class   string
	x, w    float64
	activeX float64 // the faint "still up" span; zero width when absent
	activeW float64
	label   string
}

// plotRow is one horizontal lane: a service, when it was asked to start,
// and when it reported started.
type plotRow struct {
	name    string
	reqNs   int64
	doneNs  int64 // 0 while still starting
	running bool  // currently STARTED, so an "active" span is real
}

// renderBootPlot turns boot timing data into an SVG timeline — the
// equivalent of `systemd-analyze plot`. Each service gets a lane showing
// the span between its start request and the moment it reported started,
// so a slow service is visible as a wide bar and, more usefully, a
// service *waiting* on one is visible as a bar that starts late.
//
// It is a pure function of the reply so it can be tested without a
// daemon; cmdAnalyzePlot does the I/O.
func renderBootPlot(info control.BootTimeInfo) (string, error) {
	// A service whose start was requested after the boot target was
	// reached is not part of the boot: it is an operator action or a
	// restart, and on a box with weeks of uptime it would stretch the
	// axis until the boot itself was a single pixel. Those are counted
	// and named in the header rather than silently dropped. While the
	// boot is still in progress there is no cutoff, so a hanging boot
	// plots everything — which is when the picture is worth most.
	cutoff := info.BootReadyNs
	var rows []plotRow
	hidden := 0
	haveStamps := false
	for _, s := range info.Services {
		if s.StartReqNs == 0 {
			continue
		}
		haveStamps = true
		if cutoff > 0 && s.StartReqNs > cutoff {
			hidden++
			continue
		}
		rows = append(rows, plotRow{
			name:    s.Name,
			reqNs:   s.StartReqNs,
			doneNs:  s.StartedNs,
			running: s.State == service.StateStarted,
		})
	}

	if !haveStamps {
		for _, s := range info.Services {
			if s.StartupNs > 0 {
				return "", fmt.Errorf("analyze plot: the daemon reports startup " +
					"durations but no start timestamps, so it predates this " +
					"subcommand (slinit 2.4.8); `analyze time` works against it")
			}
		}
		return "", fmt.Errorf("analyze plot: no service has been started yet — " +
			"nothing to plot")
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("analyze plot: every service with timing data was "+
			"started after the boot target (%d of them), so none belongs to the "+
			"boot window; `analyze time` lists them", hidden)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].reqNs != rows[j].reqNs {
			return rows[i].reqNs < rows[j].reqNs
		}
		return rows[i].name < rows[j].name
	})

	end := info.BootReadyNs
	for _, r := range rows {
		if r.doneNs > end {
			end = r.doneNs
		}
		if r.reqNs > end {
			end = r.reqNs
		}
	}

	// On a fresh boot the kernel span is real time that elapsed
	// immediately before slinit, so the timeline can start at power-on
	// and give the kernel a lane of its own.
	//
	// Two cases where that figure does not abut slinit's start, and
	// drawing it would be a lie told to scale:
	//
	//   - after a soft reboot it belongs to a boot that may be weeks old;
	//   - a user-mode manager reports the machine's uptime, so a session
	//     started an hour into uptime claims an hour of "kernel".
	//
	// The second cannot be recognised from the reply, but both share a
	// signature: a kernel figure out of all proportion to what slinit
	// did. Past kernelLaneRatio the lane would leave userspace a sliver
	// of the axis, so the figure moves to a header line instead — it is
	// still reported, just not drawn as if it were this boot.
	userspace := end - info.BootStartNs
	showKernel := info.SoftReboots == 0 && info.KernelUptimeNs > 0 &&
		info.BootStartNs > 0 &&
		(userspace > 0 && info.KernelUptimeNs <= kernelLaneRatio*userspace)

	origin := info.BootStartNs
	if showKernel {
		origin -= info.KernelUptimeNs
	}
	if origin == 0 {
		origin = rows[0].reqNs
	}
	// A service restored across a soft reboot can carry a request time
	// from the previous generation. Clamp rather than draw off-canvas.
	if rows[0].reqNs < origin {
		origin = rows[0].reqNs
		showKernel = false
	}

	if end <= origin {
		end = origin + int64(time.Second)
	}
	// Headroom so the last bar does not sit flush against the edge.
	span := (end - origin) * 103 / 100

	x := func(t int64) float64 {
		if t <= origin {
			return plotLeftPad
		}
		v := plotLeftPad + float64(t-origin)/float64(span)*plotWidth
		if v > plotLeftPad+plotWidth {
			return plotLeftPad + plotWidth
		}
		return v
	}

	// Lay the bars out first: the canvas has to be wide enough for the
	// labels hanging off their right-hand ends, and how far right a bar
	// ends is not known until the axis is scaled.
	var lanes []laneDraw
	if showKernel {
		lanes = append(lanes, laneDraw{
			class: "kernel",
			x:     plotLeftPad,
			w:     x(info.BootStartNs) - plotLeftPad,
			label: fmt.Sprintf("kernel (%s)", formatDuration(time.Duration(info.KernelUptimeNs))),
		})
	}
	for _, r := range rows {
		lane := laneDraw{class: "activating", x: x(r.reqNs)}
		endNs := r.doneNs
		if starting := endNs == 0; starting {
			// Still going: run the bar to the edge of the data.
			endNs = end
			lane.class = "starting"
			lane.label = r.name + " (starting)"
		} else {
			lane.label = fmt.Sprintf("%s (%s)", r.name,
				formatDuration(time.Duration(r.doneNs-r.reqNs)))
		}
		lane.w = x(endNs) - lane.x
		if lane.w < 1.5 {
			lane.w = 1.5
		}
		// The faint span after a bar says the service is still up. It is
		// drawn only for services that are STARTED right now: the reply
		// carries no stop instant, so for anything else the end of that
		// span would be invented.
		if r.running && r.doneNs != 0 {
			lane.activeX = lane.x + lane.w
			lane.activeW = x(end) - lane.activeX
		}
		lanes = append(lanes, lane)
	}

	head := plotHeader(info, showKernel, hidden, len(rows))
	headH := float64(len(head)+1)*plotLineH + 24
	rowsTop := headH + 22 // axis labels live just above the first lane

	need := float64(plotLeftPad + plotWidth + plotMinPad)
	for _, l := range lanes {
		if w := l.x + l.w + float64(len(l.label))*plotCharW + 13; w > need {
			need = w
		}
	}
	for _, line := range head {
		if w := plotLeftPad + float64(len(line))*plotCharW + 8; w > need {
			need = w
		}
	}
	totalW := int(need)
	if max := plotLeftPad + plotWidth + plotRightPad; totalW > max {
		totalW = max
	}
	totalH := int(rowsTop) + len(lanes)*plotRowH + 24

	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8" standalone="no"?>`+"\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" `+
		`viewBox="0 0 %d %d" version="1.1">`+"\n", totalW, totalH, totalW, totalH)
	b.WriteString(plotStyle)

	fmt.Fprintf(&b, `<rect class="bg" x="0" y="0" width="%d" height="%d"/>`+"\n",
		totalW, totalH)

	y := float64(plotLineH) + 8
	fmt.Fprintf(&b, `<text class="title" x="%d" y="%.0f">%s</text>`+"\n",
		plotLeftPad, y, html.EscapeString(head[0]))
	for _, line := range head[1:] {
		y += plotLineH
		fmt.Fprintf(&b, `<text class="sub" x="%d" y="%.0f">%s</text>`+"\n",
			plotLeftPad, y, html.EscapeString(line))
	}

	// Grid: vertical rules with a time label at the top of the plot area.
	gridBottom := float64(totalH - 10)
	step := plotGridStep(span)
	for t := int64(0); t <= span; t += step {
		gx := x(origin + t)
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f"/>`+"\n",
			gx, headH, gx, gridBottom)
		label := "0"
		if t > 0 {
			label = formatDuration(time.Duration(t))
		}
		fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.0f" text-anchor="middle">%s</text>`+"\n",
			gx, headH+14, label)
	}

	// The moment the boot target came up, which is what `analyze time`
	// reports as the userspace figure.
	if info.BootReadyNs > 0 {
		rx := x(info.BootReadyNs)
		fmt.Fprintf(&b, `<line class="ready" x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f"/>`+"\n",
			rx, headH, rx, gridBottom)
	}

	y = rowsTop
	for _, l := range lanes {
		if l.activeW > 0 {
			fmt.Fprintf(&b, `<rect class="active" x="%.1f" y="%.1f" width="%.1f" height="%d"/>`+"\n",
				l.activeX, y, l.activeW, plotBarH)
		}
		fmt.Fprintf(&b, `<rect class="%s" x="%.1f" y="%.1f" width="%.1f" height="%d"/>`+"\n",
			l.class, l.x, y, l.w, plotBarH)
		plotLabel(&b, l.x+l.w, y, totalW, l.label, l.x)
		y += plotRowH
	}

	fmt.Fprintf(&b, `<line class="axis" x1="%d" y1="%.0f" x2="%d" y2="%.0f"/>`+"\n",
		plotLeftPad, headH, plotLeftPad, gridBottom)
	b.WriteString("</svg>\n")
	return b.String(), nil
}

// plotLabel writes a bar's caption after its right edge, or before its
// left edge when there is not enough room — a long name on a late bar
// would otherwise run off the canvas.
func plotLabel(b *strings.Builder, barEnd, lane float64, totalW int, text string, barStart float64) {
	esc := html.EscapeString(text)
	ty := lane + plotBarH - 2
	if barEnd+float64(len(text))*plotCharW+8 > float64(totalW) {
		fmt.Fprintf(b, `<text class="lbl" x="%.1f" y="%.1f" text-anchor="end">%s</text>`+"\n",
			barStart-4, ty, esc)
		return
	}
	fmt.Fprintf(b, `<text class="lbl" x="%.1f" y="%.1f">%s</text>`+"\n", barEnd+5, ty, esc)
}

// plotHeader builds the text block above the timeline. It states the same
// facts as `analyze time`, so a plot pasted into a bug report stands on
// its own.
func plotHeader(info control.BootTimeInfo, showKernel bool, hidden, shown int) []string {
	kernel := time.Duration(info.KernelUptimeNs)
	head := []string{"slinit boot timeline"}

	switch {
	case info.BootReadyNs > 0:
		userspace := time.Duration(info.BootReadyNs - info.BootStartNs)
		if showKernel {
			head = append(head, fmt.Sprintf("Startup finished in %s (kernel) + %s (userspace) = %s",
				formatDuration(kernel), formatDuration(userspace),
				formatDuration(kernel+userspace)))
		} else {
			head = append(head, fmt.Sprintf("Startup finished in %s (userspace)",
				formatDuration(userspace)))
			// Say the number even though the lane is not drawn, so the
			// plot never looks like it lost data.
			if kernel > 0 && info.SoftReboots == 0 {
				head = append(head, fmt.Sprintf("Kernel reports %s before slinit started — "+
					"too far out of proportion to draw; the timeline starts at slinit.",
					formatDuration(kernel)))
			}
		}
		head = append(head, fmt.Sprintf("%s reached after %s in userspace.",
			info.BootSvcName, formatDuration(userspace)))
	default:
		head = append(head, fmt.Sprintf("Startup in progress: boot service %q has not "+
			"yet reached STARTED.", info.BootSvcName))
	}

	if info.SoftReboots > 0 {
		line := fmt.Sprintf("Soft reboot #%d — the timeline starts where this "+
			"generation of slinit did", info.SoftReboots)
		if info.StartUptimeNs > 0 {
			line += fmt.Sprintf(", %s into machine uptime",
				formatUptime(time.Duration(info.StartUptimeNs)))
		}
		head = append(head, line+".")
	}

	line := fmt.Sprintf("%d services in the boot window", shown)
	if hidden > 0 {
		line += fmt.Sprintf("; %d started after the boot target and are not shown "+
			"(see `slinitctl analyze time`)", hidden)
	}
	return append(head, line+".")
}

// plotGridStep picks a round interval that puts roughly ten rules on the
// axis, so the labels stay readable whether the boot took 300ms or an
// hour.
func plotGridStep(span int64) int64 {
	steps := []time.Duration{
		time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond,
		10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond,
		100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond,
		time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
		15 * time.Second, 30 * time.Second,
		time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
		15 * time.Minute, 30 * time.Minute,
		time.Hour, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour,
	}
	for _, s := range steps {
		if span/int64(s) <= 10 {
			return int64(s)
		}
	}
	return int64(24 * time.Hour)
}

const plotStyle = `<style type="text/css">
  text { font-family: "DejaVu Sans Mono", "Liberation Mono", monospace;
         font-size: 11px; fill: #202020; }
  text.title { font-size: 15px; font-weight: bold; }
  text.sub { fill: #505050; }
  text.axis { fill: #707070; font-size: 10px; }
  rect.bg { fill: #ffffff; }
  rect.kernel { fill: #c9c9ef; stroke: #7b7bbf; stroke-width: 0.5; }
  rect.activating { fill: #f0a860; stroke: #a86a1e; stroke-width: 0.5; }
  rect.active { fill: #9fd39f; stroke: none; opacity: 0.45; }
  rect.starting { fill: #f4d4d4; stroke: #bf4040; stroke-width: 1;
                  stroke-dasharray: 3 2; }
  line.grid { stroke: #dedede; stroke-width: 1; }
  line.axis { stroke: #8a8a8a; stroke-width: 1; }
  line.ready { stroke: #2e8b57; stroke-width: 1.5; stroke-dasharray: 4 3; }
</style>
`
