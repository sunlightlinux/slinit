package bootmode

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParse — table-driven coverage of every recognized token plus
// negative cases (unknown tokens, KEY=VALUE forms that must NOT trip
// bare-token handlers, last-wins conflict resolution).
func TestParse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Options
	}{
		// Empty / no-op inputs.
		{"empty", "", Options{}},
		{"only unknown kernel args", "root=/dev/sda1 quiet ro noibrs", Options{}},

		// Sysvinit compat: bare runlevel-1 aliases.
		{"single alone", "single", Options{Mode: Rescue}},
		{"single amidst kernel args", "root=/dev/sda1 single ro", Options{Mode: Rescue}},
		{"s alone", "s", Options{Mode: Rescue}},
		{"runlevel 1 bare", "1", Options{Mode: Rescue}},

		// Numeric runlevels other than 1: not mapped (systemd doesn't
		// map them either).
		{"runlevel 2 ignored", "2", Options{}},
		{"runlevel 3 ignored", "3", Options{}},
		{"runlevel 5 ignored", "5", Options{}},

		// Bare emergency / rescue.
		{"emergency keyword", "emergency", Options{Mode: Emergency}},
		{"rescue keyword", "rescue", Options{Mode: Rescue}},

		// Vendor-prefixed selectors.
		{"slinit.emergency", "slinit.emergency", Options{Mode: Emergency}},
		{"slinit.rescue", "slinit.rescue", Options{Mode: Rescue}},
		{"slinit.debug-shell", "slinit.debug-shell", Options{DebugShell: true}},
		{"slinit.confirm-spawn", "slinit.confirm-spawn", Options{ConfirmSpawn: true}},
		{"slinit.crash-shell", "slinit.crash-shell", Options{CrashShell: true}},
		{"slinit.debug legacy", "slinit.debug", Options{Debug: true}},

		// log-level KEY=VALUE.
		{"log-level debug", "slinit.log-level=debug", Options{LogLevel: "debug"}},
		{"log-level info", "slinit.log-level=info", Options{LogLevel: "info"}},
		{"log-level bare (missing value) is ignored", "slinit.log-level", Options{}},

		// KEY=VALUE forms of bare-token selectors: must NOT trip
		// bare-token handlers. A `single=1` is a kernel arg
		// (whoever set it, not us) and mustn't accidentally trigger
		// Rescue mode.
		{"single with value ignored", "single=1", Options{}},
		{"emergency with value ignored", "emergency=yes", Options{}},
		{"rescue with value ignored", "rescue=maybe", Options{}},
		{"runlevel 1 with value ignored", "1=whatever", Options{}},

		// Combined settings — the operator packs several selectors
		// on one line.
		{"combined normal boot",
			"root=/dev/sda1 slinit.debug-shell slinit.log-level=info",
			Options{DebugShell: true, LogLevel: "info"}},
		{"combined emergency boot",
			"quiet slinit.emergency slinit.debug-shell slinit.crash-shell",
			Options{Mode: Emergency, DebugShell: true, CrashShell: true}},
		{"all flags together",
			"slinit.rescue slinit.debug-shell slinit.confirm-spawn " +
				"slinit.crash-shell slinit.debug slinit.log-level=notice",
			Options{Mode: Rescue, DebugShell: true, ConfirmSpawn: true,
				CrashShell: true, Debug: true, LogLevel: "notice"}},

		// Last-mode-wins semantics: matches systemd behavior + operator
		// muscle memory (last thing typed wins).
		{"emergency then rescue", "emergency rescue", Options{Mode: Rescue}},
		{"rescue then emergency", "rescue emergency", Options{Mode: Emergency}},
		{"slinit.rescue then slinit.emergency",
			"slinit.rescue slinit.emergency", Options{Mode: Emergency}},
		{"single then emergency (vendor tokens win)",
			"single slinit.emergency", Options{Mode: Emergency}},

		// Log level: last value wins.
		{"log-level overridden",
			"slinit.log-level=debug slinit.log-level=info",
			Options{LogLevel: "info"}},

		// Whitespace robustness — leading/trailing whitespace + tabs.
		{"leading + trailing whitespace",
			"  \tslinit.emergency  \n", Options{Mode: Emergency}},
		{"tabs between tokens",
			"quiet\tslinit.debug-shell\troot=/dev/sda1",
			Options{DebugShell: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Parse(c.in)
			if got != c.want {
				t.Errorf("Parse(%q)\n = %+v\n want %+v", c.in, got, c.want)
			}
		})
	}
}

// TestModeString verifies stringification is stable + covers the
// unknown-value fallback (defensive: an int Mode from elsewhere
// mustn't produce an empty log line).
func TestModeString(t *testing.T) {
	cases := []struct {
		m    Mode
		want string
	}{
		{Normal, "normal"},
		{Emergency, "emergency"},
		{Rescue, "rescue"},
		{Mode(99), "normal"}, // unknown → normal, not empty
	}
	for _, c := range cases {
		if got := c.m.String(); got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", int(c.m), got, c.want)
		}
	}
}

// TestParseFromProc uses a temp file as the /proc/cmdline stand-in via
// direct Parse — the actual ParseFromProc reads /proc/cmdline which we
// cannot fake in a unit test without OS_PROC path injection. Instead we
// exercise the "read a file, call Parse" contract by round-tripping
// through a real file so a future refactor that adds an injection path
// still exercises the same code shape.
func TestParseFromProc(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cmdline")
	content := "root=/dev/sda1 slinit.debug-shell slinit.rescue\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test cmdline: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test cmdline: %v", err)
	}
	got := Parse(string(data))
	want := Options{Mode: Rescue, DebugShell: true}
	if got != want {
		t.Errorf("Parse of tmp cmdline\n = %+v\n want %+v", got, want)
	}
}
