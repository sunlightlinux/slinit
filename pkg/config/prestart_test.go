package config

import (
	"strings"
	"testing"
)

func TestParsePreStartCommand(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
pre-start-command = /usr/local/bin/setup --first
pre-start-command += --second
post-start-command = /usr/local/bin/notify started
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wantPre := []string{"/usr/local/bin/setup", "--first", "--second"}
	if len(desc.PreStartCommand) != len(wantPre) {
		t.Fatalf("pre-start-command: got %v want %v", desc.PreStartCommand, wantPre)
	}
	for i, w := range wantPre {
		if desc.PreStartCommand[i] != w {
			t.Errorf("pre[%d]: got %q want %q", i, desc.PreStartCommand[i], w)
		}
	}
	wantPost := []string{"/usr/local/bin/notify", "started"}
	if len(desc.PostStartCommand) != len(wantPost) {
		t.Fatalf("post-start-command: got %v want %v", desc.PostStartCommand, wantPost)
	}
	for i, w := range wantPost {
		if desc.PostStartCommand[i] != w {
			t.Errorf("post[%d]: got %q want %q", i, desc.PostStartCommand[i], w)
		}
	}
}

func TestParsePrePostStartEmptyByDefault(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.PreStartCommand != nil {
		t.Errorf("PreStartCommand should be nil by default, got %v", desc.PreStartCommand)
	}
	if desc.PostStartCommand != nil {
		t.Errorf("PostStartCommand should be nil by default, got %v", desc.PostStartCommand)
	}
}

// systemd's "+" prefix: the command runs as root, not as run-as. The
// parser strips it and sets the flag; "+=" keeps the flag of the
// command it extends and takes a "+" there as an argument; "=" resets it.
func TestParsePrivilegedPrefix(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
run-as = nobody
pre-start-command = +/bin/mkdir -p /run/foo
pre-start-command += +x
post-start-command = /bin/notify
post-start-command = +/bin/notify again
post-start-command = /bin/notify last
stop-command += +/bin/stopper
finish-command = +/bin/fin
pre-stop-hook = +/bin/pre
control-command-hup = +/bin/reload
control-command-hup += -v
control-command-usr1 = /bin/usr1
exec-condition = +test -d /run/foo
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := strings.Join(desc.PreStartCommand, "|"); got != "/bin/mkdir|-p|/run/foo|+x" {
		t.Errorf("pre-start-command = %q", got)
	}
	if got := strings.Join(desc.PostStartCommand, "|"); got != "/bin/notify|last" {
		t.Errorf("post-start-command = %q", got)
	}
	if got := strings.Join(desc.StopCommand, "|"); got != "/bin/stopper" {
		t.Errorf("stop-command = %q", got)
	}
	if got := strings.Join(desc.ControlCommands["HUP"], "|"); got != "/bin/reload|-v" {
		t.Errorf("control-command-hup = %q", got)
	}
	p := desc.Privileged
	if !p.PreStart || p.PostStart || !p.Stop || !p.Finish || !p.PreStop ||
		!p.Control["HUP"] || p.Control["USR1"] {
		t.Errorf("privileged flags = %+v", p)
	}
	if desc.FinishCommand[0] != "/bin/fin" || desc.PreStopHook[0] != "/bin/pre" {
		t.Errorf("finish %v, pre-stop %v: + not stripped", desc.FinishCommand, desc.PreStopHook)
	}
	if len(desc.Predicates) != 1 || !desc.Predicates[0].Privileged ||
		desc.Predicates[0].Param != "test -d /run/foo" {
		t.Errorf("exec-condition = %+v", desc.Predicates)
	}
}

func TestParsePrivilegedPrefixRefusesSpace(t *testing.T) {
	for _, line := range []string{"pre-start-command = + /bin/x", "post-start-command = +", "exec-condition = + true"} {
		if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+line+"\n"), "svc", "test"); err == nil {
			t.Errorf("%q: accepted", line)
		}
	}
}
