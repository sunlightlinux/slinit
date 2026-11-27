package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// cycleLoader loads services from slinit files in dir and systemd units
// in units, and collects the start-order cycle warnings.
func cycleLoader(t *testing.T, root string) (*service.ServiceSet, *DirLoader, *[]string) {
	t.Helper()
	var warned []string
	orig := orderingCycleWarn
	orderingCycleWarn = func(m string) { warned = append(warned, m) }
	t.Cleanup(func() { orderingCycleWarn = orig })
	ss := service.NewServiceSet(&testConsumerLogger{})
	dl := NewDirLoader(ss, []string{filepath.Join(root, "slinit.d")})
	dl.SetSystemdDirs([]string{filepath.Join(root, "units")})
	ss.SetLoader(dl)
	return ss, dl, &warned
}

func waitStarted(t *testing.T, svcs ...service.Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, s := range svcs {
		for s.State() != service.StateStarted {
			if time.Now().After(deadline) {
				t.Fatalf("%s is %v, want STARTED: a start-order cycle hangs it", s.Name(), s.State())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// wickedd has Wants=wickedd-nanny, wickedd-nanny After=wickedd: as a
// waits-for and an after:, the two wait for each other. The after: is
// dropped at load with a warning, and both start.
func TestOrderingCycleOfSystemdUnitsIsBroken(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "wickedd.service", "[Unit]\nWants=nanny.service\n[Service]\nExecStart=/bin/sleep 60\n")
	writeUnit(t, units, "nanny.service", "[Unit]\nAfter=wickedd.service\n[Service]\nExecStart=/bin/sleep 60\n")
	ss, dl, warned := cycleLoader(t, root)
	w, err := dl.LoadService("wickedd")
	if err != nil {
		t.Fatal(err)
	}
	n := ss.FindService("nanny", false)
	if findStartCycle(w) != nil {
		t.Fatalf("a start-order cycle is still in the graph")
	}
	if len(*warned) != 1 || !strings.Contains((*warned)[0], "nanny's `after: wickedd`") {
		t.Fatalf("warnings %q, want one naming nanny's after: wickedd", *warned)
	}
	ss.StartService(w)
	defer func() { ss.StopService(w); ss.StopService(n) }()
	waitStarted(t, w, n)
}

// The udevd pattern: udevd Wants=creds, creds Before=udevd. Both edges
// put udevd after creds: no loop, nothing dropped.
func TestConsistentOrderFromBothEndsIsKept(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "udevd.service", "[Unit]\nWants=creds.service\n[Service]\nExecStart=/bin/sleep 60\n")
	writeUnit(t, units, "creds.service", "[Unit]\nBefore=udevd.service\n[Service]\nExecStart=/bin/sleep 60\n")
	ss, dl, warned := cycleLoader(t, root)
	u, err := dl.LoadService("udevd")
	if err != nil {
		t.Fatal(err)
	}
	if len(*warned) != 0 {
		t.Fatalf("warnings %q, want none", *warned)
	}
	var before bool
	for _, d := range u.Record().Dependencies() {
		before = before || (d.To.Name() == "creds" && d.DepType == service.DepBefore)
	}
	if !before {
		t.Errorf("creds' before: udevd was dropped: %v", u.Record().Dependencies())
	}
	c := ss.FindService("creds", false)
	ss.StartService(u)
	defer func() { ss.StopService(u); ss.StopService(c) }()
	waitStarted(t, u, c)
}

// One unit with Wants= and Before= on the same unit (wickedd and
// wickedd-nanny): it waits for nanny and orders nanny after itself. The
// before: goes; the unit loads rather than being refused.
func TestWantsAndBeforeOnOneUnitLoads(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "a.service", "[Unit]\nWants=b.service\nBefore=b.service\n[Service]\nExecStart=/bin/sleep 60\n")
	writeUnit(t, units, "b.service", "[Service]\nExecStart=/bin/sleep 60\n")
	ss, dl, warned := cycleLoader(t, root)
	a, err := dl.LoadService("a")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if findStartCycle(a) != nil || len(*warned) != 1 || !strings.Contains((*warned)[0], "a's `before: b`") {
		t.Fatalf("cycle left %v, warnings %q", findStartCycle(a) != nil, *warned)
	}
	b := ss.FindService("b", false)
	ss.StartService(a)
	defer func() { ss.StopService(a); ss.StopService(b) }()
	waitStarted(t, a, b)
}

// A slinit file closing a loop with after: behind a dependency loaded
// first is refused, as dinit refuses it, and leaves nothing loaded that
// holds on to the refused services.
func TestNativeAfterCycleIsRefused(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	writeServiceFile(t, dir, "y", "type = internal\ndepends-on: x\n")
	writeServiceFile(t, dir, "x", "type = internal\nafter: y\n")
	ss, dl, _ := cycleLoader(t, root)
	if _, err := dl.LoadService("y"); err == nil || !strings.Contains(err.Error(), "cyclic dependency") {
		t.Fatalf("err = %v, want a cyclic dependency y -> x -> y", err)
	}
	if ss.FindService("y", false) != nil || ss.FindService("x", false) != nil {
		t.Error("a refused loop left a service loaded")
	}
}

// A reload of a slinit file whose after: closes a loop is refused, as
// dinit refuses it; a systemd unit's is broken, as systemd breaks it.
func TestReloadClosingACycle(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	writeServiceFile(t, dir, "y", "type = internal\ndepends-on: x\n")
	writeServiceFile(t, dir, "x", "type = internal\n")
	ss, dl, _ := cycleLoader(t, root)
	y, err := dl.LoadService("y")
	if err != nil {
		t.Fatal(err)
	}
	writeServiceFile(t, dir, "x", "type = internal\nafter: y\n")
	if _, err := dl.ReloadService(ss.FindService("x", false)); err == nil {
		t.Fatal("a slinit file's reload closing a loop was accepted")
	}
	if findStartCycle(y) != nil {
		t.Fatal("the refused reload left a loop")
	}

	root = t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "wickedd.service", "[Unit]\nWants=nanny.service\n[Service]\nExecStart=/bin/sleep 60\n")
	writeUnit(t, units, "nanny.service", "[Service]\nExecStart=/bin/sleep 60\n")
	ss, dl, warned := cycleLoader(t, root)
	w, err := dl.LoadService("wickedd")
	if err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "nanny.service", "[Unit]\nAfter=wickedd.service\n[Service]\nExecStart=/bin/sleep 60\n")
	if _, err := dl.ReloadService(ss.FindService("nanny", false)); err != nil {
		t.Fatalf("a unit's reload closing a loop was refused: %v", err)
	}
	if findStartCycle(w) != nil || len(*warned) != 1 {
		t.Fatalf("after a unit's reload: cycle left %v, warnings %q", findStartCycle(w) != nil, *warned)
	}
}

// dracut-cmdline: Before=dracut-pre-udev; dracut-pre-udev: Wants= and
// After=dracut-cmdline. The same order from both ends, no loop: loading
// dracut-cmdline loads dracut-pre-udev for its before:, whose waits-for
// back used to be refused as a cyclic dependency while dracut-cmdline
// was still loading.
func TestBeforeTargetWaitingBackIsNoCycle(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "cmdline.service", "[Unit]\nBefore=preudev.service\n[Service]\nExecStart=/bin/sleep 60\n")
	writeUnit(t, units, "preudev.service", "[Unit]\nWants=cmdline.service\nAfter=cmdline.service\n[Service]\nExecStart=/bin/sleep 60\n")
	ss, dl, warned := cycleLoader(t, root)
	c, err := dl.LoadService("cmdline")
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(*warned) != 0 || findStartCycle(c) != nil {
		t.Fatalf("warnings %q", *warned)
	}
	p := ss.FindService("preudev", false)
	ss.StartService(p)
	defer func() { ss.StopService(p); ss.StopService(c) }()
	waitStarted(t, c, p)

	// The same in slinit files.
	root = t.TempDir()
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	writeServiceFile(t, dir, "a", "type = internal\nbefore: b\n")
	writeServiceFile(t, dir, "b", "type = internal\nwaits-for: a\n")
	_, dl, _ = cycleLoader(t, root)
	if _, err := dl.LoadService("a"); err != nil {
		t.Fatalf("slinit files: refused: %v", err)
	}
}

// Loaded from the unit whose After= closes the loop, the loop is broken
// as when loaded from the other end, not refused.
func TestSystemdCycleLoadedFromEitherEnd(t *testing.T) {
	for _, first := range []string{"multipathd", "queueing"} {
		root := t.TempDir()
		units := filepath.Join(root, "units")
		writeUnit(t, units, "multipathd.service", "[Unit]\nWants=queueing.service\n[Service]\nExecStart=/bin/sleep 60\n")
		writeUnit(t, units, "queueing.service", "[Unit]\nAfter=multipathd.service\n[Service]\nExecStart=/bin/sleep 60\n")
		ss, dl, warned := cycleLoader(t, root)
		if _, err := dl.LoadService(first); err != nil {
			t.Fatalf("loaded from %s: refused: %v", first, err)
		}
		if findStartCycle(ss.FindService("multipathd", false)) != nil || len(*warned) != 1 {
			t.Fatalf("loaded from %s: warnings %q", first, *warned)
		}
	}
}

// Loading the before: target unmarks the service that declared it; a
// loop of dependencies closed in that window is still refused, and
// nothing that took an edge on a refused or failed service stays loaded
// with it.
func TestUnmarkedWindowCannotLeakACycleOrAnEdge(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(filepath.Join(dir, "x.d"), 0o755)
	os.WriteFile(filepath.Join(dir, "x.d", "a"), nil, 0o644)
	writeServiceFile(t, dir, "x", "type = internal\nbefore: b\nwaits-for.d: x.d\n")
	writeServiceFile(t, dir, "b", "type = internal\ndepends-on: a\n")
	writeServiceFile(t, dir, "a", "type = internal\ndepends-on: x\n")
	ss, dl, _ := cycleLoader(t, root)
	if _, err := dl.LoadService("x"); err == nil {
		t.Fatal("a loop of dependencies (x -> a -> x) loaded")
	}
	for _, n := range []string{"x", "a", "b"} {
		if ss.FindService(n, false) != nil {
			t.Errorf("%s left loaded behind the refused loop", n)
		}
	}

	// x fails after b, its before: target, took a waits-for on it.
	root = t.TempDir()
	dir = filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	writeServiceFile(t, dir, "x", "type = internal\nbefore: b\nafter: missing\n")
	writeServiceFile(t, dir, "b", "type = internal\nwaits-for: x\n")
	ss, dl, _ = cycleLoader(t, root)
	if _, err := dl.LoadService("x"); err == nil {
		t.Fatal("x loaded with a missing after:")
	}
	if b := ss.FindService("b", false); b != nil {
		t.Fatalf("b stayed loaded holding %v", b.Record().Dependencies())
	}
}

// A loop a nested load brought in is broken even when the load that
// brought it fails.
func TestLoopBrokenWhenRootFails(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	units := filepath.Join(root, "units")
	writeUnit(t, units, "wickedd.service", "[Unit]\nWants=nanny.service\n[Service]\nExecStart=/bin/sleep 60\n")
	writeUnit(t, units, "nanny.service", "[Unit]\nAfter=wickedd.service\n[Service]\nExecStart=/bin/sleep 60\n")
	writeServiceFile(t, dir, "r", "type = internal\nwaits-for: wickedd\nafter: missing\n")
	ss, dl, _ := cycleLoader(t, root)
	if _, err := dl.LoadService("r"); err == nil {
		t.Fatal("r loaded")
	}
	if w := ss.FindService("wickedd", false); w != nil && findStartCycle(w) != nil {
		t.Fatalf("loop left: %s", cycleString(findStartCycle(w)))
	}
}

// A unit's reload adding the Wants= that closes a loop with another
// unit's After= is broken, as loading the pair fresh breaks it.
func TestSystemdReloadAddingWantsBreaksTheLoop(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "wickedd.service", "[Service]\nExecStart=/bin/sleep 60\n")
	writeUnit(t, units, "nanny.service", "[Unit]\nAfter=wickedd.service\n[Service]\nExecStart=/bin/sleep 60\n")
	ss, dl, warned := cycleLoader(t, root)
	if _, err := dl.LoadService("nanny"); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "wickedd.service", "[Unit]\nWants=nanny.service\n[Service]\nExecStart=/bin/sleep 60\n")
	w, err := dl.ReloadService(ss.FindService("wickedd", false))
	if err != nil {
		t.Fatalf("reload refused: %v", err)
	}
	if findStartCycle(w) != nil || len(*warned) != 1 {
		t.Fatalf("cycle left %v, warnings %q", findStartCycle(w) != nil, *warned)
	}
}

// A failed reload leaves the service counted as the kind of description
// it still runs.
func TestFailedReloadKeepsTheUnitKind(t *testing.T) {
	root := t.TempDir()
	units := filepath.Join(root, "units")
	writeUnit(t, units, "n.service", "[Service]\nExecStart=/bin/sleep 60\n")
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	ss, dl, _ := cycleLoader(t, root)
	if _, err := dl.LoadService("n"); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(units, "n.service"))
	if _, err := dl.ReloadService(ss.FindService("n", false)); err == nil {
		t.Fatal("reload of a removed unit succeeded")
	}
	if !dl.systemdLoaded["n"] {
		t.Error("a failed reload forgot n is a systemd unit")
	}
	// A slinit file that parses and is then refused (its depends-on
	// does not resolve) does not make n count as one either.
	writeServiceFile(t, dir, "n", "type = internal\ndepends-on: missing\n")
	if _, err := dl.ReloadService(ss.FindService("n", false)); err == nil {
		t.Fatal("reload onto a missing dependency succeeded")
	}
	if !dl.systemdLoaded["n"] {
		t.Error("a refused reload to a slinit file made n stop counting as a systemd unit")
	}
}

// A unit's before: that would take a service past MaxDepDepth is
// dropped, not installed past the limit.
func TestSystemdBeforeRespectsMaxDepth(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "slinit.d")
	os.MkdirAll(dir, 0o755)
	units := filepath.Join(root, "units")
	n := func(p string, i int) string { return fmt.Sprintf("%s%02d", p, i) }
	for i := 0; i <= 30; i++ {
		body := "type = internal\n"
		if i > 0 {
			body += "depends-on: " + n("c", i-1) + "\n"
		}
		writeServiceFile(t, dir, n("c", i), body)
	}
	for i := 0; i <= 5; i++ {
		body := "[Service]\nExecStart=/bin/sleep 60\n"
		if i > 0 {
			body = "[Unit]\nRequires=" + n("d", i-1) + ".service\nAfter=" + n("d", i-1) + ".service\n" + body
		}
		writeUnit(t, units, n("d", i)+".service", body)
	}
	writeUnit(t, units, "s.service", "[Unit]\nBefore=c00.service\nRequires=d05.service\nAfter=d05.service\n[Service]\nExecStart=/bin/sleep 60\n")
	ss, dl, _ := cycleLoader(t, root)
	if _, err := dl.LoadService("c30"); err != nil {
		t.Fatal(err)
	}
	dl.LoadService("s")
	for _, d := range ss.FindService("c00", false).Record().Dependencies() {
		if d.To.Name() == "s" {
			t.Fatalf("before: installed past MaxDepDepth: c30 at depth %d", ss.FindService("c30", false).Record().DepDepth())
		}
	}
}
