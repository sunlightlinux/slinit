package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// --cpu-affinity must pin all of slinit, not the one thread that parsed
// it: with the thread reserve in place, `slinit -u -cpu-affinity 0` on 12
// CPUs left 39 of 40 threads on every CPU, and whatever they forked with
// them.
func TestSetDaemonAffinityPinsEveryThread(t *testing.T) {
	var orig unix.CPUSet
	if err := unix.SchedGetaffinity(0, &orig); err != nil {
		t.Skip(err)
	}
	if orig.Count() < 2 {
		t.Skip("needs at least 2 CPUs")
	}
	first := -1
	for c := 0; first < 0; c++ {
		if orig.IsSet(c) {
			first = c
		}
	}
	defer setDaemonAffinity(&orig)

	reserveThreads(20)
	var one unix.CPUSet
	one.Set(first)
	if err := setDaemonAffinity(&one); err != nil {
		t.Fatal(err)
	}
	want := strconv.Itoa(first)

	tasks, _ := filepath.Glob("/proc/self/task/*/status")
	if len(tasks) < 20 {
		t.Fatalf("only %d threads", len(tasks))
	}
	bad := 0
	for _, st := range tasks {
		if got := cpusAllowed(t, st); got != "" && got != want {
			bad++
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d threads not on CPU %s", bad, len(tasks), want)
	}

	// Children copy the forking thread's mask, whichever thread that is.
	var wg sync.WaitGroup
	var mu sync.Mutex
	badKids := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := exec.Command("/bin/sleep", "5")
			if err := c.Start(); err != nil {
				t.Error(err)
				return
			}
			got := cpusAllowed(t, filepath.Join("/proc", strconv.Itoa(c.Process.Pid), "status"))
			c.Process.Kill()
			c.Wait()
			if got != want {
				mu.Lock()
				badKids++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if badKids > 0 {
		t.Errorf("%d of 20 children not on CPU %s", badKids, want)
	}
}

// cpusAllowed returns the Cpus_allowed_list of a /proc status file.
func cpusAllowed(t *testing.T, status string) string {
	b, err := os.ReadFile(status)
	if err != nil {
		return "" // the thread exited
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("%s: no Cpus_allowed_list", status)
	return ""
}
