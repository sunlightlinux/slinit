package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestScheduleShutdownPayloadRoundTrip(t *testing.T) {
	for _, msg := range []string{"", "kernel update"} {
		st, d, m, ok := DecodeScheduleShutdown(EncodeScheduleShutdown(service.ShutdownReboot, 7*time.Minute, msg))
		if !ok || st != service.ShutdownReboot || d != 7*time.Minute || m != msg {
			t.Errorf("round trip of %q: %v %v %q %v", msg, st, d, m, ok)
		}
	}
	if _, _, _, ok := DecodeScheduleShutdown([]byte{1, 0}); ok {
		t.Error("short payload accepted")
	}
}

func TestParseShutdownTimeHHMM(t *testing.T) {
	now := time.Date(2026, 11, 19, 22, 15, 0, 0, time.Local)
	if d, _ := ParseShutdownTime("23:00", now); d != 45*time.Minute {
		t.Errorf("later today: %v", d)
	}
	if d, _ := ParseShutdownTime("06:00", now); d != 7*time.Hour+45*time.Minute {
		t.Errorf("tomorrow: %v", d)
	}
}

func nologinServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nologin")
	s := &Server{logger: logging.New(logging.LevelError), NologinPath: path}
	return s, path
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// A shutdown less than five minutes away refuses logins at once; a
// cancel lets them in again.
func TestNologinCreatedAndRemovedOnCancel(t *testing.T) {
	s, path := nologinServer(t)
	s.ScheduleShutdown(service.ShutdownReboot, 3*time.Minute, "disk swap")
	defer s.CancelShutdown()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no %s within five minutes of a shutdown: %v", path, err)
	}
	if !strings.Contains(string(b), "reboot") || !strings.Contains(string(b), "disk swap") {
		t.Errorf("nologin says %q", b)
	}
	if !s.CancelShutdown() {
		t.Fatal("cancel found nothing")
	}
	if exists(path) {
		t.Error("nologin left behind after cancel")
	}
}

// Further away, the file waits for the last five minutes.
func TestNologinWaitsForTheLastFiveMinutes(t *testing.T) {
	s, path := nologinServer(t)
	s.ScheduleShutdown(service.ShutdownPoweroff, 20*time.Minute, "")
	defer s.CancelShutdown()
	if exists(path) {
		t.Error("logins refused 20 minutes ahead")
	}
	s.scheduledMu.Lock()
	armed := s.nologinTimer != nil
	s.scheduledMu.Unlock()
	if !armed {
		t.Error("no timer armed for the last five minutes")
	}
	// Moving the shutdown closer creates it; moving it away removes it.
	s.ScheduleShutdown(service.ShutdownPoweroff, time.Minute, "")
	if !exists(path) {
		t.Error("rescheduled to 1 minute: no nologin")
	}
	s.ScheduleShutdown(service.ShutdownPoweroff, time.Hour, "")
	if exists(path) {
		t.Error("rescheduled to an hour away: nologin kept")
	}
}

// A nologin the administrator made is theirs: neither overwritten nor
// removed.
func TestNologinLeavesAnExistingFileAlone(t *testing.T) {
	s, path := nologinServer(t)
	os.WriteFile(path, []byte("maintenance\n"), 0o644)
	s.ScheduleShutdown(service.ShutdownReboot, time.Minute, "")
	s.CancelShutdown()
	if b, _ := os.ReadFile(path); string(b) != "maintenance\n" {
		t.Errorf("administrator's nologin changed or removed: %q", b)
	}
}

// /run survives a soft-reboot, so a scheduled one removes its nologin
// before it runs; the system that comes up must accept logins.
func TestNologinRemovedBeforeScheduledSoftReboot(t *testing.T) {
	s, path := nologinServer(t)
	ran := make(chan struct{})
	s.ShutdownFunc = func(service.ShutdownType, uint8) { close(ran) }
	s.ScheduleShutdown(service.ShutdownSoftReboot, time.Second, "")
	if !exists(path) {
		t.Fatal("no nologin before the soft-reboot")
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled soft-reboot never ran")
	}
	if exists(path) {
		t.Error("nologin survives into the soft-rebooted system")
	}
}
