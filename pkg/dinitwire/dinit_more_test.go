package dinitwire

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A process whose exec fails: the status buffer carries the exec stage and
// the errno where the exit status would be, in every version.
func TestDinitExecFailedStatus(t *testing.T) {
	c, svc := startDinit(t)
	if err := os.WriteFile(filepath.Join(svc, "noexec"),
		[]byte("type = process\ncommand = /nonexistent/dw-binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := mustLoad(t, c, "noexec")
	if _, err := c.StartStop(CmdStartService, 0, rec.Handle); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, c, rec.Handle, stStopped)
	if st.StopReason != stopReasonExecFailed {
		t.Fatalf("stop reason %d, want exec-failed (%d)", st.StopReason, stopReasonExecFailed)
	}
	if st.HasPID {
		t.Errorf("exec-failed status has a pid")
	}
	const doExec = 18 // exec_stage::DO_EXEC
	if st.ExecStage != doExec {
		t.Errorf("exec stage %d, want DO_EXEC (%d)", st.ExecStage, doExec)
	}
	if st.SiCode != 2 /* ENOENT */ {
		t.Errorf("status5 errno %d, want ENOENT", st.SiCode)
	}

	code, st1, err := c.Status(CmdServiceStatus, rec.Handle)
	if err != nil || code != RplyServiceStatus {
		t.Fatalf("status v1: code %d err %v", code, err)
	}
	if st1.ExecStage != doExec || st1.ExitStatus != 2 {
		t.Errorf("status v1: stage %d errno %d, want %d and ENOENT", st1.ExecStage, st1.ExitStatus, doExec)
	}
}

// Version 1 of the status buffer: a running process reports its pid.
func TestDinitStatusV1Pid(t *testing.T) {
	c, _ := startDinit(t)
	rec := mustLoad(t, c, "runner")
	waitState(t, c, rec.Handle, stStarted)
	code, st, err := c.Status(CmdServiceStatus, rec.Handle)
	if err != nil || code != RplyServiceStatus {
		t.Fatalf("status v1: code %d err %v", code, err)
	}
	if !st.HasPID || st.PID <= 0 {
		t.Fatalf("status v1: has-pid %v pid %d", st.HasPID, st.PID)
	}
	cmdline, err := os.ReadFile(filepath.Join("/proc", itoa(st.PID), "cmdline"))
	if err != nil || !strings.Contains(string(cmdline), "sleep") {
		t.Errorf("pid %d is not the runner's sleep: %q %v", st.PID, cmdline, err)
	}
	if _, _, err := c.Status(CmdStartService, rec.Handle); err == nil {
		t.Errorf("Status accepted a non-status command")
	}
}

// A gentle stop of a service with a started dependent is refused with the
// dependents' handles, and nothing stops. With the pre-ack bit the daemon
// sends PREACK first.
func TestDinitGentleStopListsDependents(t *testing.T) {
	c, _ := startDinit(t)
	runner := mustLoad(t, c, "runner")
	leaf := mustLoad(t, c, "leaf")
	if _, err := c.StartStop(CmdStartService, 0, leaf.Handle); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, leaf.Handle, stStarted)

	rep, err := c.StartStop(CmdStopService, StartStopGentle|StartStopPreACK, runner.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.PreACK {
		t.Errorf("no PREACK although asked for")
	}
	if rep.Code != RplyDependents {
		t.Fatalf("gentle stop reply %d, want dependents (%d)", rep.Code, RplyDependents)
	}
	// dinit allocates a fresh handle for each dependent it lists, even one
	// the client already holds, so the handle is resolved by name.
	if len(rep.Dependents) != 1 {
		t.Fatalf("dependents %v, want one", rep.Dependents)
	}
	if code, name, err := c.QueryServiceName(rep.Dependents[0]); err != nil ||
		code != RplyServiceName || name != "leaf" {
		t.Errorf("dependent handle %d names %q (code %d err %v), want leaf",
			rep.Dependents[0], name, code, err)
	}
	if _, st, _ := c.Status(CmdServiceStatus5, leaf.Handle); st.State != stStarted {
		t.Errorf("leaf state %d after a refused gentle stop", st.State)
	}
}

// Shutdown stops every service and is acknowledged. POWEROFF rather than
// REMAIN: a user-mode dinit exits once everything is down instead of
// powering off, while after REMAIN it ignores the SIGTERM the cleanup
// sends and the test waits out the kill timeout.
func TestDinitShutdown(t *testing.T) {
	c, _ := startDinit(t)
	rec := mustLoad(t, c, "runner")
	waitState(t, c, rec.Handle, stStarted)
	const poweroff = 3 // shutdown_type_t::POWEROFF
	code, err := c.Shutdown(poweroff)
	if err != nil || code != rplyACK {
		t.Fatalf("shutdown: code %d err %v", code, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := c.NextEvent()
		if err != nil {
			t.Fatalf("waiting for the stop event: %v", err)
		}
		if ev.Handle == rec.Handle && ev.Event == 1 /* STOPPED */ {
			return
		}
	}
	t.Fatal("no stop event after shutdown")
}

func itoa(n int32) string {
	var b [12]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

// fakeConn feeds canned daemon bytes and records what the client sent.
type fakeConn struct {
	io.Reader
	sent bytes.Buffer
}

func (f *fakeConn) Write(p []byte) (int, error) { return f.sent.Write(p) }

func fake(b ...byte) (*Conn, *fakeConn) {
	f := &fakeConn{Reader: bytes.NewReader(b)}
	return New(f), f
}

// Information packets that arrive before a reply are set aside, in order,
// and handed out by NextEvent.
func TestEventsSetAsideBeforeReply(t *testing.T) {
	ev := func(handle uint32, event uint8) []byte {
		b := []byte{InfoServiceEvent, 2 + 5 + statusSize}
		b = binary.LittleEndian.AppendUint32(b, handle)
		b = append(b, event)
		return append(b, make([]byte, statusSize)...)
	}
	var stream []byte
	stream = append(stream, ev(7, 0)...)
	stream = append(stream, ev(8, 1)...)
	stream = append(stream, rplyACK)
	c, f := fake(stream...)

	code, err := c.HandleCmd(CmdUnpinService, 5)
	if err != nil || code != rplyACK {
		t.Fatalf("code %d err %v", code, err)
	}
	if !bytes.Equal(f.sent.Bytes(), []byte{CmdUnpinService, 5, 0, 0, 0}) {
		t.Errorf("sent % x", f.sent.Bytes())
	}
	if c.PendingEvents() != 2 {
		t.Fatalf("pending %d, want 2", c.PendingEvents())
	}
	for _, want := range []uint32{7, 8} {
		e, err := c.NextEvent()
		if err != nil || e.Handle != want {
			t.Fatalf("event handle %d err %v, want %d", e.Handle, err, want)
		}
	}
	if c.PendingEvents() != 0 {
		t.Errorf("pending %d after draining", c.PendingEvents())
	}
}

// Malformed or hostile input is an error, never a panic or a huge
// allocation.
func TestMalformedInputIsAnError(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		call func(*Conn) error
	}{
		{"info length below its own header", []byte{InfoServiceEvent, 1},
			func(c *Conn) error { _, err := c.NextEvent(); return err }},
		{"service event too short", []byte{InfoServiceEvent, 4, 0, 0},
			func(c *Conn) error { _, err := c.NextEvent(); return err }},
		{"service event5 too short", append([]byte{InfoServiceEvent5, 2 + 5 + statusSize}, make([]byte, 5+statusSize)...),
			func(c *Conn) error { _, err := c.NextEvent(); return err }},
		{"env event too short", []byte{InfoEnvEvent, 3, 0},
			func(c *Conn) error { _, err := c.NextEvent(); return err }},
		{"reply while waiting for an event", []byte{rplyACK},
			func(c *Conn) error { _, err := c.NextEvent(); return err }},
		{"implausible dependents count", append([]byte{RplyDependents}, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0),
			func(c *Conn) error { _, err := c.StartStop(CmdStopService, StartStopGentle, 1); return err }},
		{"implausible dir count", []byte{RplyLoaderMech, 1, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff},
			func(c *Conn) error { _, _, err := c.QueryLoadMech(); return err }},
		{"implausible env size", append([]byte{RplyAllEnv}, 0, 0, 0, 0, 0, 0, 0, 0x7f),
			func(c *Conn) error { _, _, err := c.GetAllEnv(); return err }},
		{"implausible log size", []byte{RplyServiceLog, 0, 0xff, 0xff, 0xff, 0xff},
			func(c *Conn) error { _, _, err := c.CatLog(0, 1); return err }},
		{"implausible dsc dir length", []byte{RplySvcDscDir, 0xff, 0xff, 0xff, 0xff},
			func(c *Conn) error { _, _, err := c.QueryServiceDscDir(1); return err }},
		{"truncated status", []byte{RplyServiceStatus, 0, 1, 2},
			func(c *Conn) error { _, _, err := c.Status(CmdServiceStatus5, 1); return err }},
		{"wrong version reply", []byte{rplyNAK},
			func(c *Conn) error { _, _, err := c.QueryVersion(); return err }},
		{"unexpected reply in a list", []byte{rplyNAK},
			func(c *Conn) error { _, err := c.List(); return err }},
		{"empty name", nil,
			func(c *Conn) error { _, _, err := c.FindOrLoad(CmdFindService, ""); return err }},
		{"name too long", nil,
			func(c *Conn) error { _, _, err := c.FindOrLoad(CmdFindService, strings.Repeat("a", 1022)); return err }},
		{"env too long", nil,
			func(c *Conn) error { _, err := c.SetEnv(strings.Repeat("a", 1022)); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fake(tc.in...)
			if err := tc.call(c); err == nil {
				t.Errorf("no error")
			}
		})
	}
}

// An environment listing whose last string has no terminator still yields
// it, and an env event's NUL terminator is not part of the variable.
func TestEnvDecoding(t *testing.T) {
	body := []byte("A=1\x00B=2")
	in := append([]byte{RplyAllEnv}, binary.LittleEndian.AppendUint64(nil, uint64(len(body)))...)
	c, _ := fake(append(in, body...)...)
	_, env, err := c.GetAllEnv()
	if err != nil || len(env) != 2 || env[0] != "A=1" || env[1] != "B=2" {
		t.Fatalf("env %q err %v", env, err)
	}

	v := []byte("X=y\x00")
	ev := []byte{InfoEnvEvent, 5, 1}
	ev = binary.LittleEndian.AppendUint16(ev, uint16(len(v)))
	c, _ = fake(append(ev, v...)...)
	e, err := c.NextEvent()
	if err != nil || e.Var != "X=y" || !e.Overridden {
		t.Fatalf("env event %+v err %v", e, err)
	}
}
