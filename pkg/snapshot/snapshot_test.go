package snapshot_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/snapshot"
)

// testLogger implements service.ServiceLogger silently.
type testLogger struct{}

func (testLogger) ServiceStarted(string)        {}
func (testLogger) ServiceStopped(string)        {}
func (testLogger) ServiceFailed(string, bool)   {}
func (testLogger) Error(string, ...interface{}) {}
func (testLogger) Info(string, ...interface{})  {}

// quietRestoreLogger captures emitted lines for assertion.
type quietRestoreLogger struct {
	notices []string
	infos   []string
	warns   []string
}

func (q *quietRestoreLogger) Notice(format string, args ...any) {
	q.notices = append(q.notices, format)
}
func (q *quietRestoreLogger) Info(format string, args ...any) {
	q.infos = append(q.infos, format)
}
func (q *quietRestoreLogger) Warn(format string, args ...any) {
	q.warns = append(q.warns, format)
}

func newSet() *service.ServiceSet {
	return service.NewServiceSet(testLogger{})
}

// TestCaptureSkipsUntouchedServices verifies the capture is dense:
// services with no operator-set state are not emitted.
func TestCaptureSkipsUntouchedServices(t *testing.T) {
	set := newSet()
	set.AddService(service.NewInternalService(set, "idle"))
	set.AddService(service.NewInternalService(set, "alive"))

	// Only "alive" gets explicit activation.
	set.StartService(set.FindService("alive", false))

	snap := snapshot.Capture(set)
	if len(snap.Services) != 1 || snap.Services[0].Name != "alive" {
		t.Fatalf("expected only 'alive', got %+v", snap.Services)
	}
	if !snap.Services[0].Activated {
		t.Errorf("expected Activated=true for 'alive'")
	}
}

func TestCapturePin(t *testing.T) {
	set := newSet()
	a := service.NewInternalService(set, "pinned-up")
	b := service.NewInternalService(set, "pinned-down")
	set.AddService(a)
	set.AddService(b)

	a.Record().PinStart()
	b.Record().PinStop()

	snap := snapshot.Capture(set)
	byName := make(map[string]snapshot.ServiceSnapshot)
	for _, e := range snap.Services {
		byName[e.Name] = e
	}
	if !byName["pinned-up"].PinnedStart {
		t.Errorf("pinned-up: PinnedStart=false")
	}
	if !byName["pinned-down"].PinnedStop {
		t.Errorf("pinned-down: PinnedStop=false")
	}
}

func TestCaptureTriggered(t *testing.T) {
	set := newSet()
	ts := service.NewTriggeredService(set, "trigger-svc")
	set.AddService(ts)

	ts.SetTrigger(true)

	snap := snapshot.Capture(set)
	if len(snap.Services) != 1 || !snap.Services[0].Triggered {
		t.Errorf("expected one Triggered=true entry, got %+v", snap.Services)
	}
}

func TestCaptureGlobalEnv(t *testing.T) {
	set := newSet()
	set.GlobalSetEnv("FOO", "bar")
	set.GlobalSetEnv("LANG", "C.UTF-8")

	snap := snapshot.Capture(set)
	sort.Strings(snap.GlobalEnv)
	want := []string{"FOO=bar", "LANG=C.UTF-8"}
	if !equalStrings(snap.GlobalEnv, want) {
		t.Errorf("GlobalEnv = %v, want %v", snap.GlobalEnv, want)
	}
}

func TestRestoreActivation(t *testing.T) {
	set := newSet()
	svc := service.NewInternalService(set, "nginx")
	set.AddService(svc)

	snap := &snapshot.Snapshot{
		Version:  snapshot.CurrentVersion,
		Services: []snapshot.ServiceSnapshot{{Name: "nginx", Activated: true}},
	}
	q := &quietRestoreLogger{}
	n, err := snapshot.Restore(set, snap, q)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if n != 1 {
		t.Errorf("applied = %d, want 1", n)
	}
	if svc.State() != service.StateStarted {
		t.Errorf("nginx state=%v, want STARTED", svc.State())
	}
}

func TestRestorePinStartIsApplied(t *testing.T) {
	set := newSet()
	svc := service.NewInternalService(set, "lock")
	set.AddService(svc)

	snap := &snapshot.Snapshot{
		Services: []snapshot.ServiceSnapshot{{Name: "lock", PinnedStart: true, Activated: true}},
	}
	if _, err := snapshot.Restore(set, snap, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !svc.Record().IsStartPinned() {
		t.Errorf("expected IsStartPinned()=true")
	}
	if svc.State() != service.StateStarted {
		t.Errorf("expected STARTED, got %v", svc.State())
	}
}

func TestRestorePinStopWinsOverActivation(t *testing.T) {
	set := newSet()
	svc := service.NewInternalService(set, "blocked")
	set.AddService(svc)

	// Hostile snapshot: activated AND pinned-stop. Restore must
	// honour the operator's pin-down intent and not start it.
	snap := &snapshot.Snapshot{
		Services: []snapshot.ServiceSnapshot{
			{Name: "blocked", Activated: true, PinnedStop: true},
		},
	}
	if _, err := snapshot.Restore(set, snap, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !svc.Record().IsStopPinned() {
		t.Errorf("expected IsStopPinned()=true")
	}
	if svc.State() == service.StateStarted {
		t.Errorf("pinned-stopped service was started anyway")
	}
}

func TestRestoreTriggeredAndActivated(t *testing.T) {
	set := newSet()
	ts := service.NewTriggeredService(set, "trig")
	set.AddService(ts)

	snap := &snapshot.Snapshot{
		Services: []snapshot.ServiceSnapshot{
			{Name: "trig", Activated: true, Triggered: true},
		},
	}
	if _, err := snapshot.Restore(set, snap, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !ts.IsTriggered() {
		t.Errorf("expected IsTriggered()=true")
	}
	// With the trigger latch armed and activation applied, BringUp
	// should have advanced through STARTING to STARTED.
	if ts.State() != service.StateStarted {
		t.Errorf("trig state=%v, want STARTED", ts.State())
	}
}

func TestRestoreGlobalEnv(t *testing.T) {
	set := newSet()
	snap := &snapshot.Snapshot{
		GlobalEnv: []string{"TZ=UTC", "LANG=C.UTF-8"},
	}
	if _, err := snapshot.Restore(set, snap, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got := set.GlobalEnv()
	sort.Strings(got)
	want := []string{"LANG=C.UTF-8", "TZ=UTC"}
	if !equalStrings(got, want) {
		t.Errorf("GlobalEnv = %v, want %v", got, want)
	}
}

func TestRestoreUnknownServiceLogged(t *testing.T) {
	set := newSet()
	snap := &snapshot.Snapshot{
		Services: []snapshot.ServiceSnapshot{{Name: "ghost", Activated: true}},
	}
	q := &quietRestoreLogger{}
	n, err := snapshot.Restore(set, snap, q)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if n != 0 {
		t.Errorf("applied = %d, want 0", n)
	}
	found := false
	for _, w := range q.warns {
		if strings.Contains(w, "not loaded") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a 'not loaded' warning, got warns=%v", q.warns)
	}
}

func TestRestoreEmptyNameSkipped(t *testing.T) {
	set := newSet()
	snap := &snapshot.Snapshot{
		Services: []snapshot.ServiceSnapshot{{Name: "", Activated: true}},
	}
	n, err := snapshot.Restore(set, snap, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if n != 0 {
		t.Errorf("applied = %d, want 0", n)
	}
}

// TestCaptureRestoreRoundTrip writes intent, captures, and applies
// to a fresh ServiceSet to verify the full pipeline.
func TestCaptureRestoreRoundTrip(t *testing.T) {
	src := newSet()
	src.AddService(service.NewInternalService(src, "alpha"))
	src.AddService(service.NewInternalService(src, "beta"))
	src.AddService(service.NewInternalService(src, "gamma"))
	src.GlobalSetEnv("X", "1")

	src.StartService(src.FindService("alpha", false))
	src.FindService("beta", false).Record().PinStop()
	// gamma left untouched, should not appear

	snap := snapshot.Capture(src)

	// Persist + reload to also exercise io.go.
	path := filepath.Join(t.TempDir(), "snap.json")
	if err := snapshot.Write(path, snap); err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := snapshot.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	dst := newSet()
	dst.AddService(service.NewInternalService(dst, "alpha"))
	dst.AddService(service.NewInternalService(dst, "beta"))
	dst.AddService(service.NewInternalService(dst, "gamma"))

	if _, err := snapshot.Restore(dst, loaded, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if dst.FindService("alpha", false).State() != service.StateStarted {
		t.Errorf("alpha not started after restore")
	}
	if !dst.FindService("beta", false).Record().IsStopPinned() {
		t.Errorf("beta not stop-pinned after restore")
	}
	if dst.FindService("gamma", false).State() == service.StateStarted {
		t.Errorf("gamma was started but had no recorded intent")
	}
	if got := dst.GlobalEnv(); len(got) != 1 || got[0] != "X=1" {
		t.Errorf("GlobalEnv after restore = %v, want [X=1]", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCaptureCarriesBootTiming pins the two fields that make
// `slinitctl boot-time` survive a soft reboot. Without them the new
// generation re-reads /proc/uptime and prints the machine's whole
// uptime under the "kernel" label, a figure that grows with every soft
// reboot.
func TestCaptureCarriesBootTiming(t *testing.T) {
	set := newSet()
	set.SetKernelUptime(550 * time.Millisecond)

	snap := snapshot.Capture(set)
	if snap.KernelBootNs != int64(550*time.Millisecond) {
		t.Errorf("KernelBootNs = %d, want %d", snap.KernelBootNs, int64(550*time.Millisecond))
	}
	// A set that was never told otherwise is the original boot, so the
	// snapshot it writes describes the first soft reboot.
	if snap.SoftReboots != 1 {
		t.Errorf("SoftReboots = %d, want 1 from the original boot", snap.SoftReboots)
	}
}

// TestCaptureAccumulatesSoftReboots walks three generations to make
// sure the count climbs instead of resetting, and that the kernel
// figure is handed on unchanged rather than re-measured.
func TestCaptureAccumulatesSoftReboots(t *testing.T) {
	const kernelBoot = 550 * time.Millisecond

	set := newSet()
	set.SetKernelUptime(kernelBoot)

	for gen := 1; gen <= 3; gen++ {
		snap := snapshot.Capture(set)
		if snap.SoftReboots != gen {
			t.Fatalf("generation %d wrote SoftReboots = %d, want %d", gen, snap.SoftReboots, gen)
		}
		if snap.KernelBootNs != int64(kernelBoot) {
			t.Fatalf("generation %d changed the kernel figure: got %d, want %d",
				gen, snap.KernelBootNs, int64(kernelBoot))
		}
		// What cmd/slinit does on the next start.
		next := newSet()
		next.SetSoftReboots(snap.SoftReboots)
		next.SetKernelUptime(time.Duration(snap.KernelBootNs))
		set = next
	}
}
