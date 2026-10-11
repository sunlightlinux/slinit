package process

import (
	"strings"
	"testing"
)

// The command the child actually execs is the only thing that decides
// whether inetd semantics happen at all, and getting the composition
// wrong is silent: the service starts either way. These tests pin the
// argv.

func TestSocketAcceptFailsClosedWithoutTheHelper(t *testing.T) {
	// Starting with the listener on stdio instead is a different
	// protocol, not a degraded one, so it must be refused.
	_, _, err := StartProcess(ExecParams{
		ServiceName:  "svc",
		Command:      []string{"/bin/true"},
		SocketAccept: true,
		InetdPath:    "",
	})
	if err == nil {
		t.Fatal("socket-accept started without slinit-inetd")
	}
	if !strings.Contains(err.Error(), "slinit-inetd") {
		t.Errorf("error does not name the missing helper: %v", err)
	}
	if !strings.Contains(err.Error(), "socket-accept") {
		t.Errorf("error does not name the directive: %v", err)
	}
}

func TestInetdWrapOrderPutsTheRunnerOutside(t *testing.T) {
	// runner -> slinit-inetd -> program. The order matters: the
	// hardening has to land on the helper so the per-connection
	// children inherit it.
	p := ExecParams{
		Command:       []string{"/usr/bin/myserver", "-x"},
		SocketAccept:  true,
		InetdPath:     "/sbin/slinit-inetd",
		RunnerPath:    "/sbin/slinit-runner",
		MlockallFlags: 1,
	}
	// Mirror what StartProcess does before wrapping.
	p.Command = append([]string{p.InetdPath, "--"}, p.Command...)
	got := wrapWithRunner(p)

	if got[0] != "/sbin/slinit-runner" {
		t.Errorf("argv[0] = %q, want the runner outermost", got[0])
	}
	joined := strings.Join(got, " ")
	ri := strings.Index(joined, "slinit-runner")
	ii := strings.Index(joined, "slinit-inetd")
	pi := strings.Index(joined, "myserver")
	if !(ri < ii && ii < pi) {
		t.Errorf("order is wrong: %v", got)
	}
	if !strings.HasSuffix(joined, "/usr/bin/myserver -x") {
		t.Errorf("the real command is not last: %v", got)
	}
}

func TestNoInetdWrapWhenNotRequested(t *testing.T) {
	p := ExecParams{
		Command:    []string{"/usr/bin/myserver"},
		InetdPath:  "/sbin/slinit-inetd",
		RunnerPath: "/sbin/slinit-runner",
		// SocketAccept deliberately false: a discovered helper must not
		// wrap a service that never asked for it.
		MlockallFlags: 1,
	}
	got := strings.Join(wrapWithRunner(p), " ")
	if strings.Contains(got, "slinit-inetd") {
		t.Errorf("the helper was used without socket-accept: %s", got)
	}
}
