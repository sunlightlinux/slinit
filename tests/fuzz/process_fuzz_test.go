package fuzz

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/internal/util"
	"github.com/sunlightlinux/slinit/pkg/process"
)

// FuzzParseCapabilities fuzzes the Linux capability name parser.
func FuzzParseCapabilities(f *testing.F) {
	f.Add("cap_net_bind_service")
	f.Add("cap_sys_ptrace cap_net_raw")
	f.Add("net_bind_service,sys_ptrace")
	f.Add("CAP_NET_BIND_SERVICE")
	f.Add("0 1 2 3")
	f.Add("")
	f.Add("invalid_cap")
	f.Add("cap_")
	f.Add("cap_net_bind_service , , cap_sys_admin")
	f.Add("999")

	f.Fuzz(func(t *testing.T, data string) {
		process.ParseCapabilities(data)
	})
}

// FuzzParseSecurebits fuzzes the securebits flag parser.
func FuzzParseSecurebits(f *testing.F) {
	f.Add("noroot")
	f.Add("noroot noroot-locked")
	f.Add("no-setuid-fixup no-setuid-fixup-locked")
	f.Add("keep-caps keep-caps-locked")
	f.Add("")
	f.Add("invalid-bit")
	f.Add("noroot noroot noroot") // duplicates

	f.Fuzz(func(t *testing.T, data string) {
		process.ParseSecurebits(data)
	})
}

// FuzzParseDuration fuzzes the decimal seconds duration parser.
func FuzzParseDuration(f *testing.F) {
	f.Add("10")
	f.Add("0.5")
	f.Add("0")
	f.Add("3600")
	f.Add("")
	f.Add("abc")
	f.Add("-1")
	f.Add("1e10")
	f.Add("99999999999999999999999")
	f.Add("0.001")
	f.Add("inf")
	f.Add("NaN")

	f.Fuzz(func(t *testing.T, data string) {
		util.ParseDuration(data)
	})
}

// FuzzParseSignal fuzzes the signal name/number parser.
func FuzzParseSignal(f *testing.F) {
	f.Add("SIGTERM")
	f.Add("TERM")
	f.Add("9")
	f.Add("HUP")
	f.Add("SIGUSR1")
	f.Add("")
	f.Add("SIGNOTREAL")
	f.Add("999")
	f.Add("-1")
	f.Add("sigkill")

	f.Fuzz(func(t *testing.T, data string) {
		util.ParseSignal(data)
	})
}

// FuzzReadEnvFile fuzzes the KEY=VALUE env-file parser, including the
// !clear / !unset / !import meta-commands. The parser reads from disk,
// so each iteration stages the fuzz input in a temp file before calling
// ReadEnvFile — that way the fuzzer explores both the line-parsing and
// the meta-command dispatch.
func FuzzReadEnvFile(f *testing.F) {
	f.Add("FOO=bar\n")
	f.Add("# comment\nFOO=bar\nBAZ=qux\n")
	f.Add("!clear\nFOO=new\n")
	f.Add("!unset FOO BAR\n")
	f.Add("!import PATH HOME\n")
	f.Add("!unknown meta\nA=1\n")
	f.Add("no_equals_line\nVALID=ok\n")
	f.Add("=empty-key\n")
	f.Add("KEY=\n")
	f.Add("  spaced_out  =  value  \n")
	f.Add("")
	f.Add("!\n")
	f.Add("!!!clear\n")

	f.Fuzz(func(t *testing.T, data string) {
		path := filepath.Join(t.TempDir(), "env")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Skip()
		}
		// Pin origEnv so the fuzzer doesn't depend on the test harness
		// environment — !import results must be reproducible.
		_, _ = process.ReadEnvFileWithOrigEnv(path, []string{"PATH=/bin", "HOME=/root"})
	})
}

// FuzzReadEnvDir fuzzes the runit-style env-dir reader. The fuzzer
// encodes each input as a single file whose name is derived from the
// first line and whose content is the remainder — this keeps the
// fuzz corpus simple while still exercising name filtering, NUL-to-LF
// translation, first-line extraction, and trailing-whitespace trimming.
func FuzzReadEnvDir(f *testing.F) {
	f.Add("GREETING\nhello world\n")
	f.Add("PATH\n/usr/bin\n")
	f.Add("KEY_WITH_NUL\nline1\x00line2\n")
	f.Add(".hidden\nshould be skipped\n")
	f.Add("KEY=WITH_EQUALS\nvalue\n")
	f.Add("EMPTY\n")
	f.Add("\nno name\n")
	f.Add("TRAILING_WS\nvalue   \t\n")

	f.Fuzz(func(t *testing.T, data string) {
		dir := t.TempDir()
		// First line = filename, rest = content. Guard against empty
		// or slash-bearing names — the harness isn't testing path
		// traversal, just the parser — so skip those corpora rather
		// than write outside the temp dir.
		nl := -1
		for i := 0; i < len(data); i++ {
			if data[i] == '\n' {
				nl = i
				break
			}
		}
		var name, content string
		if nl < 0 {
			name, content = data, ""
		} else {
			name, content = data[:nl], data[nl+1:]
		}
		if name == "" || containsPathSep(name) {
			t.Skip()
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Skip()
		}
		_, _ = process.ReadEnvDir(dir)
	})
}

func containsPathSep(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == 0 {
			return true
		}
	}
	return false
}
