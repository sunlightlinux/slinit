package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// statusOf is what a client decodes for svc.
func statusOf(t *testing.T, svc service.Service) ServiceStatusInfo5 {
	t.Helper()
	st, err := DecodeServiceStatus5(EncodeServiceStatus5(svc))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func waitStopped(t *testing.T, svc service.Service) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if svc.State() == service.StateStopped {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not stop: %v", svc.Name(), svc.State())
}

// is-failed's verdict, end to end through the wire encoding. The flip bit
// must be set exactly where failure-conditions overrule the status, and
// never under the default — a client's own reading then stands, which is
// also all that a daemon predating the bit gives it.
func TestIsFailedVerdictOnTheWire(t *testing.T) {
	for _, c := range []struct {
		name     string
		script   string
		fc       string
		wantFail bool
		wantFlip bool
	}{
		{"default-failing", "exit 1", "process-fails", true, false},
		{"default-clean", "exit 0", "process-fails", false, false},
		{"default-killed", "kill -KILL $$", "process-fails", true, false},
		{"none-failing", "exit 1", "none", false, true},
		{"stops-clean", "exit 0", "process-stops", true, true},
		{"stops-failing", "exit 1", "process-stops", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			set := service.NewServiceSet(&flagTestLogger{})
			svc := service.NewProcessService(set, "p")
			svc.SetCommand([]string{"/bin/sh", "-c", "sleep 0.1; " + c.script})
			fc, err := service.ParseFailureConditions(c.fc)
			if err != nil {
				t.Fatal(err)
			}
			svc.Record().SetFailureConditions(fc)
			set.AddService(svc)
			set.StartService(svc)
			waitStopped(t, svc)

			st := statusOf(t, svc)
			if got := st.Flags&StatusFlagFailedFlip != 0; got != c.wantFlip {
				t.Errorf("flip bit = %v, want %v (flags 0x%02x)", got, c.wantFlip, st.Flags)
			}
			if got := StatusShowsFailure(st); got != c.wantFail {
				t.Errorf("is-failed verdict = %v, want %v", got, c.wantFail)
			}
		})
	}
}

// A service stopped on request has not failed. The old rule (stopped
// with a v1 exit field other than 0) said it had, because that field is
// -1 for a process that did not exit by itself and for one that never ran.
func TestIsFailedFalseAfterRequestedStop(t *testing.T) {
	set := service.NewServiceSet(&flagTestLogger{})
	in := service.NewInternalService(set, "in")
	set.AddService(in)
	set.StartService(in)
	set.StopService(in)

	p := service.NewProcessService(set, "p")
	p.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(p)
	set.StartService(p)
	time.Sleep(100 * time.Millisecond)
	set.StopService(p)
	waitStopped(t, p)

	for _, s := range []service.Service{in, p} {
		st := statusOf(t, s)
		if StatusShowsFailure(st) {
			t.Errorf("%s: is-failed says FAILED after a requested stop (reason %v)", s.Name(), s.StopReason())
		}
		if st.Flags&StatusFlagFailedFlip != 0 {
			t.Errorf("%s: flip bit set under the default conditions", s.Name())
		}
	}
}
