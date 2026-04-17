package config

import (
	"strings"
	"testing"
)

func TestWrapInitdWithConfD_StructureAndOrder(t *testing.T) {
	argv := wrapInitdWithConfD("/etc/init.d/nginx", "nginx", "start")
	if len(argv) != 3 {
		t.Fatalf("want 3-arg argv (shell, -c, snippet); got %v", argv)
	}
	if argv[0] != "/bin/sh" {
		t.Errorf("argv[0] = %q, want /bin/sh", argv[0])
	}
	if argv[1] != "-c" {
		t.Errorf("argv[1] = %q, want -c", argv[1])
	}

	snippet := argv[2]
	// rc.conf must be sourced before conf.d/<name>.
	rcIdx := strings.Index(snippet, "rc.conf")
	cdIdx := strings.Index(snippet, "conf.d")
	execIdx := strings.Index(snippet, "exec ")
	if rcIdx < 0 || cdIdx < 0 || execIdx < 0 {
		t.Fatalf("snippet missing expected pieces: %q", snippet)
	}
	if !(rcIdx < cdIdx && cdIdx < execIdx) {
		t.Errorf("wrong ordering rc<cd<exec: rc=%d cd=%d exec=%d in %q",
			rcIdx, cdIdx, execIdx, snippet)
	}
}

func TestWrapInitdWithConfD_GuardsPresence(t *testing.T) {
	snippet := wrapInitdWithConfD("/etc/init.d/foo", "foo", "start")[2]
	// Both sources must be guarded by [ -r ... ] so a missing file
	// doesn't abort the pipeline.
	if strings.Count(snippet, "[ -r ") != 2 {
		t.Errorf("expected two presence guards, got snippet: %q", snippet)
	}
}

func TestWrapInitdWithConfD_QuotesPaths(t *testing.T) {
	// Even a vanilla invocation must single-quote paths so shell
	// metacharacters in (hypothetical) conf.d filenames can't escape.
	snippet := wrapInitdWithConfD("/etc/init.d/foo", "foo", "start")[2]
	if !strings.Contains(snippet, "'/etc/init.d/foo'") {
		t.Errorf("script path not quoted: %q", snippet)
	}
	if !strings.Contains(snippet, "'/etc/conf.d'") {
		t.Errorf("conf.d dir not quoted: %q", snippet)
	}
}

func TestShellQuote_NoSpecials(t *testing.T) {
	if got := shellQuote("simple"); got != "'simple'" {
		t.Errorf("shellQuote(simple) = %q, want 'simple'", got)
	}
}

func TestShellQuote_EscapesSingleQuote(t *testing.T) {
	got := shellQuote("it's tricky")
	// Expected: 'it'\''s tricky'
	want := `'it'\''s tricky'`
	if got != want {
		t.Errorf("shellQuote(it's tricky) = %q, want %q", got, want)
	}
}

func TestShellQuote_MultipleQuotes(t *testing.T) {
	got := shellQuote("a'b'c")
	want := `'a'\''b'\''c'`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetOpenRCPaths_OverrideOneAtATime(t *testing.T) {
	origRC := openrcRCConf
	origCD := openrcConfDDir
	t.Cleanup(func() {
		openrcRCConf = origRC
		openrcConfDDir = origCD
	})

	SetOpenRCPaths("/custom/rc.conf", "")
	if openrcRCConf != "/custom/rc.conf" {
		t.Errorf("rc.conf override failed: got %q", openrcRCConf)
	}
	if openrcConfDDir != origCD {
		t.Errorf("conf.d should be unchanged, got %q", openrcConfDDir)
	}

	SetOpenRCPaths("", "/custom/conf.d")
	if openrcRCConf != "/custom/rc.conf" {
		t.Errorf("rc.conf clobbered: got %q", openrcRCConf)
	}
	if openrcConfDDir != "/custom/conf.d" {
		t.Errorf("conf.d override failed: got %q", openrcConfDDir)
	}
}

func TestSetOpenRCShell(t *testing.T) {
	orig := openrcShellPath
	t.Cleanup(func() { openrcShellPath = orig })

	SetOpenRCShell("/usr/bin/sh")
	if openrcShellPath != "/usr/bin/sh" {
		t.Errorf("got %q, want /usr/bin/sh", openrcShellPath)
	}
	SetOpenRCShell("") // empty preserves current value
	if openrcShellPath != "/usr/bin/sh" {
		t.Errorf("empty override should preserve; got %q", openrcShellPath)
	}
}

func TestInitDToServiceDescription_UsesWrapper(t *testing.T) {
	// Re-use the existing helper to create a minimal init.d script
	// on disk, then confirm the resulting Command goes through the
	// conf.d wrapper rather than execing the script directly.
	dir := t.TempDir()
	script := dir + "/myservice"
	if err := writeExec(script, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	desc, err := InitDToServiceDescription(script)
	if err != nil {
		t.Fatalf("InitDToServiceDescription: %v", err)
	}
	if len(desc.Command) < 3 || desc.Command[0] != "/bin/sh" || desc.Command[1] != "-c" {
		t.Errorf("Command not wrapped: %v", desc.Command)
	}
	if len(desc.StopCommand) < 3 || desc.StopCommand[0] != "/bin/sh" || desc.StopCommand[1] != "-c" {
		t.Errorf("StopCommand not wrapped: %v", desc.StopCommand)
	}
	// Script path and name must both appear in the wrapped snippet.
	if !strings.Contains(desc.Command[2], "myservice") {
		t.Errorf("wrapper snippet missing service name: %q", desc.Command[2])
	}
	// Start action present in start wrapper, stop in stop wrapper.
	if !strings.Contains(desc.Command[2], "'start'") {
		t.Errorf("start wrapper missing 'start': %q", desc.Command[2])
	}
	if !strings.Contains(desc.StopCommand[2], "'stop'") {
		t.Errorf("stop wrapper missing 'stop': %q", desc.StopCommand[2])
	}
}

// writeExec creates an executable file at path with the given content.
func writeExec(path, content string) error {
	f, err := openForWrite(path)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return chmodExec(path)
}
