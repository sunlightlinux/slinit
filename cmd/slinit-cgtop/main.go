// slinit-cgtop is a top-like viewer for cgroup v2 resource consumption.
// systemd-cgtop equivalent, scoped to what a slinit-managed system
// actually exposes: per-cgroup task count, CPU%, memory RSS, and IO
// bytes/sec. Reads only /sys/fs/cgroup/**, no privileged operations.
//
// Usage:
//
//	slinit-cgtop [--delay=1s] [--iterations=N] [--depth=3] [--sort=cpu|mem|tasks|path] [--once]
//
// Sorting defaults to cpu descending. --once prints one delta after
// --delay and exits, which is what scripts want. Without --once the
// tool loops until Ctrl-C.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cgroupRoot = "/sys/fs/cgroup"

// sample is a single point-in-time snapshot of the metrics we care
// about for one cgroup directory.
type sample struct {
	path       string // relative to cgroupRoot; "" is the root itself
	tasks      int
	cpuUsageUS uint64
	memCurrent uint64
	ioRead     uint64
	ioWrite    uint64
}

// row is the display-ready delta between two samples: rate-based
// fields (CPU%, IO) are computed against the sampling interval.
type row struct {
	path        string
	tasks       int
	cpuPct      float64
	mem         uint64
	ioReadRate  uint64
	ioWriteRate uint64
}

func main() {
	delay := flag.Duration("delay", time.Second, "refresh interval")
	iterations := flag.Int("iterations", 0, "number of refreshes (0 = infinite)")
	depth := flag.Int("depth", 3, "max cgroup tree depth (root = 0)")
	sortBy := flag.String("sort", "cpu", "sort column: cpu | mem | tasks | path")
	once := flag.Bool("once", false, "print one delta snapshot and exit (script-friendly)")
	all := flag.Bool("all", false, "include cgroups with zero tasks and zero memory")
	flag.Parse()

	// cgroup v2 is required — the field names (memory.current,
	// cpu.stat, io.stat) don't exist on v1. Modern systemd-cgtop made
	// the same call after v250 dropped its v1/hybrid fallback path, so
	// we're aligned with upstream. The presence of a legacy per-
	// controller subtree (e.g. /sys/fs/cgroup/memory/) surfaces a
	// clearer diagnostic than the bare stat failure.
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		if _, v1 := os.Stat(filepath.Join(cgroupRoot, "memory", "memory.usage_in_bytes")); v1 == nil {
			fmt.Fprintf(os.Stderr,
				"slinit-cgtop: cgroup v1 hierarchy detected at %s — this tool needs the unified v2 hierarchy.\n"+
					"Boot with `systemd.unified_cgroup_hierarchy=1` (or the kernel default on a modern distro) and retry.\n",
				cgroupRoot)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "slinit-cgtop: cgroup v2 not mounted at %s: %v\n", cgroupRoot, err)
		os.Exit(1)
	}

	// SIGINT / SIGTERM: exit cleanly without leaving the terminal in
	// an alt-screen state.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Print("\r\n")
		os.Exit(0)
	}()

	prev, err := takeAll(*depth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-cgtop: initial scan: %v\n", err)
		os.Exit(1)
	}
	time.Sleep(*delay)

	iter := 0
	for {
		curr, err := takeAll(*depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-cgtop: scan: %v\n", err)
			os.Exit(1)
		}
		rows := diff(prev, curr, *delay, *all)
		sortRows(rows, *sortBy)

		if !*once {
			clearScreen()
		}
		printHeader(*delay, *sortBy)
		printRows(rows)

		iter++
		if *once || (*iterations > 0 && iter >= *iterations) {
			return
		}
		prev = curr
		time.Sleep(*delay)
	}
}

// takeAll walks /sys/fs/cgroup up to the requested depth and returns
// one sample per cgroup directory. Errors on individual entries are
// swallowed (permission-denied on a subtree the operator can't read
// shouldn't abort the whole run) — the sample just carries whatever
// fields we did manage to read.
func takeAll(maxDepth int) (map[string]sample, error) {
	out := make(map[string]sample)
	err := filepath.WalkDir(cgroupRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// EACCES on a subtree is common; skip it.
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(cgroupRoot, path)
		if rel == "." {
			rel = ""
		}
		if depthOf(rel) > maxDepth {
			return filepath.SkipDir
		}
		s := sample{path: rel}
		s.tasks = countLines(filepath.Join(path, "cgroup.procs"))
		s.cpuUsageUS = readKV(filepath.Join(path, "cpu.stat"), "usage_usec")
		s.memCurrent = readUint(filepath.Join(path, "memory.current"))
		s.ioRead, s.ioWrite = readIOStat(filepath.Join(path, "io.stat"))
		out[rel] = s
		return nil
	})
	return out, err
}

func depthOf(rel string) int {
	if rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

// countLines returns the number of non-blank lines in a kernfs virtual
// file. Used for cgroup.procs, whose stat size is always 0.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

// readKV reads a key/value flat file (cpu.stat) and returns the value
// for the named key, or 0 when absent.
func readKV(path, key string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != key {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		return v
	}
	return 0
}

func readUint(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	// memory.current can be "max" on unbounded cgroups; treat as 0.
	s := strings.TrimSpace(string(data))
	if s == "max" || s == "" {
		return 0
	}
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

// readIOStat sums rbytes / wbytes across all devices in io.stat.
// Format per line: "<major:minor> rbytes=N wbytes=N rios=N wios=N ..."
func readIOStat(path string) (rb, wb uint64) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, tok := range strings.Fields(sc.Text()) {
			k, v, ok := strings.Cut(tok, "=")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			switch k {
			case "rbytes":
				rb += n
			case "wbytes":
				wb += n
			}
		}
	}
	return rb, wb
}

// diff computes per-cgroup rates over the sampling window. When a
// cgroup vanished between samples it's dropped; when it just appeared
// its rate-based fields default to 0 (single point can't produce a
// rate).
func diff(prev, curr map[string]sample, window time.Duration, includeIdle bool) []row {
	winSec := window.Seconds()
	if winSec <= 0 {
		winSec = 1
	}
	cpuTicksPerCgroup := float64(len(currCPUCores()))
	_ = cpuTicksPerCgroup // reserved for a future per-core-normalised %

	out := make([]row, 0, len(curr))
	for path, c := range curr {
		p, hadPrev := prev[path]
		var cpuUS uint64
		if hadPrev && c.cpuUsageUS >= p.cpuUsageUS {
			cpuUS = c.cpuUsageUS - p.cpuUsageUS
		}
		var rr, wr uint64
		if hadPrev {
			if c.ioRead >= p.ioRead {
				rr = uint64(float64(c.ioRead-p.ioRead) / winSec)
			}
			if c.ioWrite >= p.ioWrite {
				wr = uint64(float64(c.ioWrite-p.ioWrite) / winSec)
			}
		}
		// CPU% = CPU-seconds consumed / wall-seconds elapsed * 100.
		// The denominator is (winSec * ncpu) if we want per-system
		// utilisation; the simpler "% of one core" form matches
		// systemd-cgtop's default column so we use that.
		cpuPct := float64(cpuUS) / (winSec * 1_000_000.0) * 100.0
		if !includeIdle && c.tasks == 0 && c.memCurrent == 0 && cpuPct == 0 {
			continue
		}
		out = append(out, row{
			path:        c.path,
			tasks:       c.tasks,
			cpuPct:      cpuPct,
			mem:         c.memCurrent,
			ioReadRate:  rr,
			ioWriteRate: wr,
		})
	}
	return out
}

// currCPUCores best-effort probes /sys/devices/system/cpu/online.
// Falls back to a single "0" range on any error — the CPU% column
// still stays meaningful because we normalise to a single core anyway.
func currCPUCores() []int {
	data, err := os.ReadFile("/sys/devices/system/cpu/online")
	if err != nil {
		return []int{0}
	}
	var out []int
	for _, part := range strings.Split(strings.TrimSpace(string(data)), ",") {
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, _ := strconv.Atoi(lo)
			b, _ := strconv.Atoi(hi)
			for i := a; i <= b; i++ {
				out = append(out, i)
			}
		} else if n, err := strconv.Atoi(part); err == nil {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return []int{0}
	}
	return out
}

func sortRows(rows []row, sortBy string) {
	switch sortBy {
	case "path":
		sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })
	case "tasks":
		sort.Slice(rows, func(i, j int) bool { return rows[i].tasks > rows[j].tasks })
	case "mem", "memory":
		sort.Slice(rows, func(i, j int) bool { return rows[i].mem > rows[j].mem })
	default: // cpu (default)
		sort.Slice(rows, func(i, j int) bool { return rows[i].cpuPct > rows[j].cpuPct })
	}
}

func clearScreen() {
	// Cursor home + clear from cursor to end of screen. Doesn't switch
	// to the alt screen — we want the last snapshot to remain visible
	// after Ctrl-C.
	fmt.Print("\x1b[H\x1b[2J")
}

func printHeader(delay time.Duration, sortBy string) {
	fmt.Printf("slinit-cgtop  refresh=%v  sort=%s  ts=%s\n",
		delay.Round(time.Millisecond), sortBy, time.Now().Format("15:04:05"))
	fmt.Printf("%-45s %6s %7s %10s %11s %11s\n",
		"CGROUP", "TASKS", "%CPU", "MEM", "IO-READ/s", "IO-WRITE/s")
}

func printRows(rows []row) {
	for _, r := range rows {
		p := r.path
		if p == "" {
			p = "/"
		}
		if len(p) > 45 {
			p = "..." + p[len(p)-42:]
		}
		fmt.Printf("%-45s %6d %6.1f%% %10s %11s %11s\n",
			p, r.tasks, r.cpuPct, humanBytes(r.mem),
			humanBytes(r.ioReadRate), humanBytes(r.ioWriteRate))
	}
}

func humanBytes(n uint64) string {
	if n == 0 {
		return "-"
	}
	const K = 1024
	if n < K {
		return fmt.Sprintf("%dB", n)
	}
	if n < K*K {
		return fmt.Sprintf("%.1fK", float64(n)/K)
	}
	if n < K*K*K {
		return fmt.Sprintf("%.1fM", float64(n)/(K*K))
	}
	if n < K*K*K*K {
		return fmt.Sprintf("%.1fG", float64(n)/(K*K*K))
	}
	return fmt.Sprintf("%.1fT", float64(n)/(K*K*K*K))
}
