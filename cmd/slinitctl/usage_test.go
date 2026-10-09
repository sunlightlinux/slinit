package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// commands is checked before connecting, so it must name exactly the
// commands the dispatch switches handle: a missing entry would turn a
// real command into "Unknown command", an extra one would let a typo
// through to a confusing connection error. Every entry that can reject
// a count must also say what to print.
func TestCommandNamesMatchDispatch(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	dispatched := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SwitchStmt:
			if id, ok := n.Tag.(*ast.Ident); ok && id.Name == "command" {
				for _, st := range n.Body.List {
					for _, e := range st.(*ast.CaseClause).List {
						if bl, ok := e.(*ast.BasicLit); ok {
							s, _ := strconv.Unquote(bl.Value)
							dispatched[s] = true
						}
					}
				}
			}
		case *ast.BinaryExpr:
			// `if command == "platform"` and friends, handled pre-connect.
			if id, ok := n.X.(*ast.Ident); ok && id.Name == "command" && n.Op == token.EQL {
				if bl, ok := n.Y.(*ast.BasicLit); ok {
					s, _ := strconv.Unquote(bl.Value)
					dispatched[s] = true
				}
			}
		}
		return true
	})
	for name := range dispatched {
		if _, ok := commands[name]; !ok {
			t.Errorf("%q is dispatched but missing from commands", name)
		}
	}
	for name, a := range commands {
		if !dispatched[name] {
			t.Errorf("%q is in commands but nothing dispatches it", name)
		}
		if (a.min > 0 || a.max >= 0) && a.usage == "" {
			t.Errorf("%q can fail its arity check but has no usage text", name)
		}
		if a.max >= 0 && a.max < a.min {
			t.Errorf("%q: max %d < min %d", name, a.max, a.min)
		}
	}
}

func TestArityError(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		args []string
		ok   bool
	}{
		{"list", nil, true},
		{"list", []string{"extra"}, true}, // extras tolerated, as before
		{"start", nil, false},
		{"start", []string{"x"}, true},
		{"start", []string{"x", "y"}, true},
		{"status", []string{"-l"}, false},
		{"status", []string{"--full", "x"}, true},
		{"catlog", []string{"--clear"}, false},
		{"catlog", []string{"--clear", "x"}, true},
		{"signal", []string{"-l"}, true},
		{"signal", []string{"--list"}, true},
		{"signal", []string{"HUP"}, false},
		{"signal", []string{"HUP", "x"}, true},
		{"setenv", []string{"x"}, false},
		{"setenv", []string{"x", "K=V"}, true},
		{"setenv-global", nil, false},
		{"add-dep", []string{"a", "b"}, false},
		{"add-dep", []string{"a", "waits-for", "b"}, true},
		{"action", []string{"svc"}, false},
		{"action", []string{"svc", "rotate"}, true},
		{"is-newer-than", []string{"/"}, false},
		{"is-newer-than", []string{"/", "/", "/"}, false},
		{"is-newer-than", []string{"/", "/"}, true},
		{"switch-root", nil, false},
		{"switch-root", []string{"/new"}, true},
		{"switch-root", []string{"/new", "/sbin/init"}, true},
		{"switch-root", []string{"/new", "/sbin/init", "x"}, false},
		{"edit", nil, false},
		{"edit", []string{"a", "b"}, false},
		{"suspend", nil, true},
		{"suspend", []string{"--no-coordination", "mem"}, true},
		{"suspend", []string{"mem", "disk"}, false},
		{"completion", nil, true},
		{"reset-failed", nil, false},
		{"reset-failed", []string{"--all"}, true},
		{"activate-profile", nil, false},
		{"run", nil, false},
		{"run", []string{"--", "true"}, true},
		{"shutdown", nil, true},
		{"reboot", []string{"now"}, true},
		{"analyze", []string{"critical-chain", "x"}, true},
	} {
		msg := arityError(c.cmd, c.args)
		if (msg == "") != c.ok {
			t.Errorf("arityError(%q, %q) = %q, want ok=%v", c.cmd, c.args, msg, c.ok)
		}
	}
}

func TestUsagefIsUsageError(t *testing.T) {
	var ue usageError
	if !errors.As(usagef("bad %s", "x"), &ue) {
		t.Error("usagef result is not a usageError")
	}
}

// Exit statuses, as STABILITY.md states them: 2 for a usage error, 1
// for a command that failed. No daemon is running at the socket used,
// so every usage error here must be caught without one.
func TestExitStatuses(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	bin := filepath.Join(t.TempDir(), "slinitctl")
	if out, err := exec.Command(gobin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	sock := filepath.Join(t.TempDir(), "no-daemon.sock")

	for _, c := range []struct {
		args []string
		want int
	}{
		{nil, 2},                           // no command
		{[]string{"-p", sock, "bogus"}, 2}, // unknown command, no daemon
		{[]string{"-p", sock, "-w", "abc", "ls"}, 2}, // bad global flag value
		{[]string{"-p"}, 2},                          // flag missing its argument
		{[]string{"is-newer-than", "/"}, 2},          // wrong argument count
		{[]string{"completion", "tcsh"}, 2},          // bad argument value
		{[]string{"--offline", "start", "x"}, 2},     // not an offline command
		{[]string{"-p", sock, "start", "x"}, 1},      // valid call, no daemon: failure

		// Argument counts are checked before connecting.
		{[]string{"-p", sock, "start"}, 2},
		{[]string{"-p", sock, "setenv", "x"}, 2},
		{[]string{"-p", sock, "add-dep", "a", "b"}, 2},
		{[]string{"-p", sock, "action", "svc"}, 2},
		{[]string{"--offline", "enable"}, 2}, // before offline mode too
	} {
		err := exec.Command(bin, c.args...).Run()
		got := 0
		if ee, ok := err.(*exec.ExitError); ok {
			got = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got != c.want {
			t.Errorf("slinitctl %v: exit %d, want %d", c.args, got, c.want)
		}
	}
}
