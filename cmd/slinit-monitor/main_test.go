package main

import (
	"io"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestSubstituteArg(t *testing.T) {
	cases := []struct {
		arg, want string
	}{
		{"plain", "plain"},
		{"%n", "svc"},
		{"<%n|%s|%v>", "<svc|started|val>"},
		{"100%%", "100%"},
		{"%%n", "%n"},
		{"%x", "%x"},
		{"trail%", "trail%"},
		{"%", "%"},
	}
	for _, c := range cases {
		if got := substituteArg(c.arg, "svc", "started", "val"); got != c.want {
			t.Errorf("substituteArg(%q) = %q, want %q", c.arg, got, c.want)
		}
	}
}

// TestSubstituteArgNoChaining: text that came from a substitution must not
// be scanned for specifiers again.
func TestSubstituteArgNoChaining(t *testing.T) {
	if got := substituteArg("%n", "svc%v", "set", "VAL"); got != "svc%v" {
		t.Errorf("name re-expanded: got %q, want %q", got, "svc%v")
	}
	if got := substituteArg("%v", "n", "set", "%%n%s"); got != "%%n%s" {
		t.Errorf("value re-expanded: got %q, want %q", got, "%%n%s")
	}
}

func TestCommandArgvValueStaysOneArgument(t *testing.T) {
	got := commandArgv(`/bin/echo "a %v" %n`, "FOO", "set", `a b --flag "x`)
	want := []string{"/bin/echo", `a a b --flag "x`, "FOO"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandArgv = %q, want %q", got, want)
	}
}

// runCaptured runs executeCommand with os.Stdout redirected to a pipe and
// returns what the spawned command printed.
func runCaptured(t *testing.T, tmpl, name, status, value string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	executeCommand(tmpl, name, status, value)
	os.Stdout = old
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestExecuteCommandValueWithSpaces(t *testing.T) {
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("no printf binary")
	}
	got := runCaptured(t, printf+` [%%s]\n %n %v`, "FOO", "set", `a b --flag "x`)
	want := "[FOO]\n[a b --flag \"x]\n"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestExecuteCommandNoChaining(t *testing.T) {
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("no printf binary")
	}
	got := runCaptured(t, printf+` [%%s]\n %n`, "svc%v", "set", "VAL")
	want := "[svc%v]\n"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
