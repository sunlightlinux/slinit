package main

import (
	"bytes"
	"io"
	"log/syslog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"EINFO_QUIET", "EINFO_VERBOSE", "EINFO_COLOR",
		"EINFO_INDENT", "EINFO_LOG", "COLUMNS",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("TERM", "dumb")
}

// captureStreams redirects both stdout and stderr into byte
// buffers for the duration of fn. Returns (stdout, stderr, rc).
func captureStreams(t *testing.T, fn func() int) (string, string, int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	doneOut := make(chan string, 1)
	doneErr := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, rOut)
		doneOut <- b.String()
	}()
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, rErr)
		doneErr <- b.String()
	}()
	// Rewire the applet stream table so dispatch() writes into our
	// pipes. This mutation is safe because tests run serially.
	for name := range applets {
		a := applets[name]
		if a.stream == oldOut {
			a.stream = wOut
		} else if a.stream == oldErr {
			a.stream = wErr
		}
		applets[name] = a
	}
	rc := fn()
	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	// Restore.
	for name := range applets {
		a := applets[name]
		if a.stream == wOut {
			a.stream = oldOut
		} else if a.stream == wErr {
			a.stream = oldErr
		}
		applets[name] = a
	}
	return <-doneOut, <-doneErr, rc
}

func TestDispatchEinfoWritesStdout(t *testing.T) {
	clearEnv(t)
	stdout, stderr, rc := captureStreams(t, func() int {
		return dispatch("einfo", []string{"hello", "world"})
	})
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	if !strings.Contains(stdout, "hello world") {
		t.Errorf("stdout: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr leaked: %q", stderr)
	}
}

func TestDispatchEerrorReturnsOneAndUsesStderr(t *testing.T) {
	clearEnv(t)
	stdout, stderr, rc := captureStreams(t, func() int {
		return dispatch("eerror", []string{"boom"})
	})
	if rc != 1 {
		t.Errorf("rc=%d, want 1", rc)
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("stderr: %q", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout leaked: %q", stdout)
	}
}

func TestDispatchStripsSlinitPrefix(t *testing.T) {
	// A symlink installed as "slinit-einfo" should dispatch the same
	// as native "einfo".
	clearEnv(t)
	stdout, _, rc := captureStreams(t, func() int {
		return dispatch("einfo", []string{"prefix"})
	})
	if rc != 0 || !strings.Contains(stdout, "prefix") {
		t.Errorf("rc=%d stdout=%q", rc, stdout)
	}
}

func TestDispatchEbeginAndEend(t *testing.T) {
	clearEnv(t)
	t.Setenv("COLUMNS", "20")
	stdout, _, rc := captureStreams(t, func() int {
		if r := dispatch("ebegin", []string{"starting"}); r != 0 {
			t.Fatalf("ebegin rc=%d", r)
		}
		return dispatch("eend", []string{"0"})
	})
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	if !strings.Contains(stdout, "starting") || !strings.Contains(stdout, "[ ok ]") {
		t.Errorf("expected 'starting' and '[ ok ]' in stdout: %q", stdout)
	}
}

func TestDispatchEendFailPropagatesCode(t *testing.T) {
	clearEnv(t)
	t.Setenv("COLUMNS", "20")
	stdout, _, rc := captureStreams(t, func() int {
		return dispatch("eend", []string{"7", "died"})
	})
	if rc != 7 {
		t.Errorf("rc=%d, want 7", rc)
	}
	if !strings.Contains(stdout, "died") || !strings.Contains(stdout, "[ !! ]") {
		t.Errorf("expected 'died' and '[ !! ]': %q", stdout)
	}
}

func TestDispatchEwendUsesWarnPalette(t *testing.T) {
	clearEnv(t)
	stdout, _, rc := captureStreams(t, func() int {
		return dispatch("ewend", []string{"1", "advisory"})
	})
	if rc != 1 {
		t.Errorf("rc=%d, want 1", rc)
	}
	if !strings.Contains(stdout, "[ !! ]") || !strings.Contains(stdout, "advisory") {
		t.Errorf("stdout: %q", stdout)
	}
}

func TestDispatchVeinfoRequiresVerbose(t *testing.T) {
	clearEnv(t)
	stdout, _, _ := captureStreams(t, func() int {
		return dispatch("veinfo", []string{"quiet by default"})
	})
	if stdout != "" {
		t.Errorf("veinfo without EINFO_VERBOSE leaked: %q", stdout)
	}
	t.Setenv("EINFO_VERBOSE", "yes")
	stdout, _, _ = captureStreams(t, func() int {
		return dispatch("veinfo", []string{"loud now"})
	})
	if !strings.Contains(stdout, "loud now") {
		t.Errorf("veinfo with env didn't print: %q", stdout)
	}
}

func TestDispatchEindentIsNoop(t *testing.T) {
	clearEnv(t)
	stdout, stderr, rc := captureStreams(t, func() int {
		return dispatch("eindent", nil)
	})
	if rc != 0 || stdout != "" || stderr != "" {
		t.Errorf("rc=%d stdout=%q stderr=%q", rc, stdout, stderr)
	}
}

func TestDispatchEvalEcolors(t *testing.T) {
	clearEnv(t)
	stdout, _, rc := captureStreams(t, func() int {
		return dispatch("eval_ecolors", nil)
	})
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	for _, key := range []string{"GOOD=", "WARN=", "BAD=", "NORMAL="} {
		if !strings.Contains(stdout, key) {
			t.Errorf("missing %q: %q", key, stdout)
		}
	}
}

func TestDispatchUnknownApplet(t *testing.T) {
	clearEnv(t)
	_, stderr, rc := captureStreams(t, func() int {
		return dispatch("bogus-name", nil)
	})
	if rc == 0 || !strings.Contains(stderr, "unknown applet") {
		t.Errorf("rc=%d stderr=%q", rc, stderr)
	}
}

func TestParseSyslogPriorityFormats(t *testing.T) {
	cases := map[string]syslog.Priority{
		"info":         syslog.LOG_INFO | syslog.LOG_USER,
		"info.daemon":  syslog.LOG_INFO | syslog.LOG_DAEMON,
		"err.local0":   syslog.LOG_ERR | syslog.LOG_LOCAL0,
		"warning.mail": syslog.LOG_WARNING | syslog.LOG_MAIL,
	}
	for spec, want := range cases {
		got, err := parseSyslogPriority(spec)
		if err != nil {
			t.Errorf("%q: %v", spec, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %d, want %d", spec, got, want)
		}
	}
	if _, err := parseSyslogPriority("bogus"); err == nil {
		t.Errorf("expected error for bogus severity")
	}
	if _, err := parseSyslogPriority("info.bogus"); err == nil {
		t.Errorf("expected error for bogus facility")
	}
	// Numeric form.
	if p, err := parseSyslogPriority("13"); err != nil || p != 13 {
		t.Errorf("numeric: got %d %v", p, err)
	}
}

func TestRunWaitFileFiresOnceCreated(t *testing.T) {
	clearEnv(t)
	t.Setenv("EINFO_VERBOSE", "yes")
	dir := t.TempDir()
	path := filepath.Join(dir, "appear.txt")
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = os.WriteFile(path, []byte("hi"), 0644)
	}()
	stdout, _, rc := captureStreams(t, func() int {
		return runWaitFile([]string{"3", path})
	})
	if rc != 0 {
		t.Errorf("rc=%d", rc)
	}
	if !strings.Contains(stdout, "Waiting for "+path) {
		t.Errorf("no Waiting line: %q", stdout)
	}
	if !strings.Contains(stdout, "[ ok ]") {
		t.Errorf("no ok marker: %q", stdout)
	}
}

func TestRunWaitFileHardTimeout(t *testing.T) {
	clearEnv(t)
	t.Setenv("EINFO_VERBOSE", "yes")
	dir := t.TempDir()
	nope := filepath.Join(dir, "never")
	start := time.Now()
	stdout, _, rc := captureStreams(t, func() int {
		return runWaitFile([]string{"1", nope})
	})
	if rc == 0 {
		t.Errorf("expected non-zero rc, got %d", rc)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout escaped: took %v", time.Since(start))
	}
	if !strings.Contains(stdout, "timed out") {
		t.Errorf("no timeout marker: %q", stdout)
	}
}

func TestDispatchEwaitfileMinArgs(t *testing.T) {
	clearEnv(t)
	_, stderr, rc := captureStreams(t, func() int {
		return dispatch("ewaitfile", []string{"5"})
	})
	if rc == 0 {
		t.Errorf("rc=%d", rc)
	}
	if !strings.Contains(stderr, "usage") {
		t.Errorf("stderr: %q", stderr)
	}
}
