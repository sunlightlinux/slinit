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

// commandNames is checked before connecting, so it must name exactly the
// commands the dispatch switches handle: a missing entry would turn a
// real command into "Unknown command", an extra one would let a typo
// through to a confusing connection error.
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
		if !commandNames[name] {
			t.Errorf("%q is dispatched but missing from commandNames", name)
		}
	}
	for name := range commandNames {
		if !dispatched[name] {
			t.Errorf("%q is in commandNames but nothing dispatches it", name)
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
