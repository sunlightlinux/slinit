package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestScriptingContract runs slinitctl and rc-service against a live
// slinit -u and checks what a script sees: the exit status, and which
// stream carries what. slinitctl(8) EXIT STATUS, rc-service(8) and
// STABILITY.md are the contract; a change here is a change a script
// notices.
//
// slinit runs in a fresh user, PID and mount namespace so every process
// it starts dies with the test. Skipped under -short and where user
// namespaces are not permitted.
func TestScriptingContract(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test skipped under -short")
	}
	if err := exec.Command("unshare", "-Urpfm", "--mount-proc", "true").Run(); err != nil {
		t.Skipf("unshare -Urpfm not permitted here: %v", err)
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	bin := t.TempDir()
	for _, pkg := range []string{"../slinit", ".", "../rc-service"} {
		name := filepath.Base(pkg)
		if pkg == "." {
			name = "slinitctl"
		}
		if out, err := exec.Command(gobin, "build", "-o", filepath.Join(bin, name), pkg).CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", pkg, err, out)
		}
	}

	dir := t.TempDir()
	sd := filepath.Join(dir, "sd")
	sock := filepath.Join(dir, "sock")
	svcs := map[string]string{
		"boot":      "type = internal\nwaits-for = up\n",
		"up":        "type = process\ncommand = /bin/sleep 1000\n",
		"down":      "type = process\ncommand = /bin/sleep 1000\n",
		"pinned":    "type = process\ncommand = /bin/sleep 1000\n",
		"failer":    "type = scripted\ncommand = /bin/false\n",
		"slowstart": "type = scripted\ncommand = /bin/sleep 1000\nstart-timeout = 0\n",
		"slowstop":  "type = process\ncommand = /bin/sh -c \"trap '' TERM; while :; do sleep 1; done\"\nstop-timeout = 0\n",
		"trig":      "type = triggered\n",
		"bad":       "type = process\ncommand = /bin/true\nno-such-directive = 1\n",
	}
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range svcs {
		if err := os.WriteFile(filepath.Join(sd, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	daemon := exec.CommandContext(ctx, "unshare", "-Urpfm", "--mount-proc", "--kill-child", "sh", "-c",
		`"$B/slinit" -u -d "$D/sd" -p "$D/sock" -B --log-file "$D/log" -F 3 & wait`)
	daemon.Env = append(os.Environ(), "B="+bin, "D="+dir)
	daemon.ExtraFiles = []*os.File{wr}
	if err := daemon.Start(); err != nil {
		cancel()
		t.Fatalf("start slinit: %v", err)
	}
	wr.Close()
	t.Cleanup(func() { cancel(); daemon.Wait() })
	rd.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := rd.Read(make([]byte, 256)); err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "log"))
		t.Fatalf("slinit not ready: %v\n%s", err, log)
	}

	env := []string{"DINIT_SOCKET_PATH=", "SLINIT_SOCKET_PATH=" + sock, "SLINITCTL=" + filepath.Join(bin, "slinitctl")}
	run := func(tool string, args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(filepath.Join(bin, tool), args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		done := make(chan error, 1)
		if err := cmd.Start(); err != nil {
			t.Fatalf("%s %v: %v", tool, args, err)
		}
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			<-done
			return -1, stdout.String(), stderr.String()
		}
		return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
	}
	ctl := func(args ...string) {
		t.Helper()
		if rc, out, errOut := run("slinitctl", args...); rc != 0 && args[len(args)-1] != "failer" {
			t.Fatalf("setup slinitctl %v: exit %d\n%s%s", args, rc, out, errOut)
		}
	}
	// Bring each service into its state.
	ctl("--pin", "start", "pinned")
	ctl("start", "slowstop")
	ctl("start", "failer")
	ctl("--no-wait", "start", "slowstart")
	ctl("--no-wait", "stop", "slowstop")

	const any = "\x00" // stdout or stderr: anything, not checked
	for _, c := range []struct {
		tool   string
		args   []string
		rc     int
		stdout string // substring that must be there; "" = must be empty
		stderr string
	}{
		// Predicates: the answer is the exit status, the state on stdout.
		{"slinitctl", []string{"is-started", "up"}, 0, "STARTED", ""},
		{"slinitctl", []string{"is-started", "down"}, 1, "STOPPED", ""},
		{"slinitctl", []string{"is-started", "slowstart"}, 1, "STARTING", ""},
		{"slinitctl", []string{"is-started", "slowstop"}, 1, "STOPPING", ""},
		{"slinitctl", []string{"--quiet", "is-started", "up"}, 0, "STARTED", ""},
		{"slinitctl", []string{"is-failed", "failer"}, 0, "FAILED", ""},
		{"slinitctl", []string{"is-failed", "up"}, 1, "STARTED", ""},
		{"slinitctl", []string{"is-failed", "down"}, 1, "STOPPED", ""},
		// A service that cannot be had is a failure reported on stderr,
		// never a state on stdout.
		{"slinitctl", []string{"is-started", "nope"}, 1, "", "not found"},
		{"slinitctl", []string{"is-failed", "nope"}, 1, "", "not found"},
		{"slinitctl", []string{"is-started", "bad"}, 1, "", "description error"},
		{"slinitctl", []string{"status", "nope"}, 1, "", "not found"},
		{"slinitctl", []string{"status", "bad"}, 1, "", "description error"},
		{"slinitctl", []string{"status", "failer"}, 0, "failer", ""},

		// Lifecycle.
		{"slinitctl", []string{"start", "up"}, 0, "already started", ""},
		{"slinitctl", []string{"--quiet", "start", "up"}, 0, "", ""},
		{"slinitctl", []string{"start", "failer"}, 1, "", "failed to start"},
		{"slinitctl", []string{"start", "nope"}, 1, "", "not found"},
		{"slinitctl", []string{"--no-wait", "start", "nope"}, 1, "", "not found"},
		{"slinitctl", []string{"start", "bad"}, 1, "", "description error"},
		{"slinitctl", []string{"stop", "down"}, 0, "already stopped", ""},
		{"slinitctl", []string{"stop", "pinned"}, 1, "", "pinned"},
		{"slinitctl", []string{"trigger", "up"}, 1, "", "not a triggered service"},
		{"slinitctl", []string{"signal", "HUP", "down"}, 1, "", "no running process"},
		// -w bounds the wait for a stop that does not end.
		{"slinitctl", []string{"-w", "1", "stop", "slowstop"}, 1, "", "timeout"},

		// Usage errors: exit 2, on stderr, nothing sent.
		{"slinitctl", nil, 2, "", "Usage:"},
		{"slinitctl", []string{"bogus"}, 2, "", "Unknown command"},
		{"slinitctl", []string{"start"}, 2, "", "Service name required"},
		{"slinitctl", []string{"signal", "BOGUS", "up"}, 2, "", "unknown signal"},
		{"slinitctl", []string{"setenv", "up", "NOEQUALS"}, 2, "", "KEY=VALUE"},
		{"slinitctl", []string{"add-dep", "up", "bogus", "down"}, 2, "", "dependency type"},
		{"slinitctl", []string{"shutdown", "bogus"}, 2, "", "unknown shutdown type"},
		{"slinitctl", []string{"reboot", "bogus"}, 2, "", "invalid time"},
		{"slinitctl", []string{"analyze", "bogus"}, 2, "", "unknown subcommand"},
		{"slinitctl", []string{"--help"}, 0, "Usage:", ""},
		{"slinitctl", []string{"--version"}, 0, "slinitctl version", ""},

		// rc-service status exits with the state, as OpenRC's does.
		{"rc-service", []string{"up", "status"}, 0, "up", ""},
		{"rc-service", []string{"down", "status"}, 3, "down", ""},
		{"rc-service", []string{"slowstop", "status"}, 4, "slowstop", ""},
		{"rc-service", []string{"slowstart", "status"}, 8, "slowstart", ""},
		{"rc-service", []string{"failer", "status"}, 32, "failer", ""},
		{"rc-service", []string{"nope", "status"}, 1, "", "not found"},
		{"rc-service", []string{"-e", "up"}, 0, "", ""},
		{"rc-service", []string{"-e", "nope"}, 1, "", "not found"},
		{"rc-service", []string{"up"}, 2, "", "usage"},

		// Still up after all of the above: nothing shut it down.
		{"slinitctl", []string{"is-started", "up"}, 0, "STARTED", ""},
	} {
		rc, out, errOut := run(c.tool, c.args...)
		bad := rc != c.rc
		for _, s := range []struct{ got, want string }{{out, c.stdout}, {errOut, c.stderr}} {
			switch {
			case s.want == any:
			case s.want == "":
				bad = bad || s.got != ""
			default:
				bad = bad || !strings.Contains(s.got, s.want)
			}
		}
		if bad {
			t.Errorf("%s %q: exit %d, want %d\nstdout: %q (want %q)\nstderr: %q (want %q)",
				c.tool, c.args, rc, c.rc, out, c.stdout, errOut, c.stderr)
		}
	}

	// Daemon gone: a failure (1), on stderr.
	env[1] = "SLINIT_SOCKET_PATH=" + filepath.Join(dir, "no-such-socket")
	if rc, out, errOut := run("slinitctl", "is-started", "up"); rc != 1 || out != "" || !strings.Contains(errOut, "Failed to connect") {
		t.Errorf("no daemon: exit %d stdout %q stderr %q, want 1, empty, Failed to connect", rc, out, errOut)
	}
	if rc, out, _ := run("rc-service", "up", "status"); rc != 1 || out != "" {
		t.Errorf("rc-service status, no daemon: exit %d stdout %q, want 1 and nothing", rc, out)
	}

	// Socket not accessible: also a failure (1), not a hang or usage.
	if os.Getuid() != 0 {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sock, filepath.Join(locked, "sock")); err != nil {
			t.Fatal(err)
		}
		os.Chmod(locked, 0)
		defer os.Chmod(locked, 0o755)
		env[1] = "SLINIT_SOCKET_PATH=" + filepath.Join(locked, "sock")
		if rc, out, errOut := run("slinitctl", "is-started", "up"); rc != 1 || out != "" || !strings.Contains(errOut, "permission denied") {
			t.Errorf("socket not accessible: exit %d stdout %q stderr %q, want 1, empty, permission denied", rc, out, errOut)
		}
	}
}
