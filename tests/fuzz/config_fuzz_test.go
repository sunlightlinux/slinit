package fuzz

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
)

// FuzzConfigParse fuzzes the main service config file parser.
// This is the highest-priority target — it parses untrusted text input
// with complex grammar (operators, includes, env substitution).
func FuzzConfigParse(f *testing.F) {
	// Seed corpus with valid configs
	f.Add("type = process\ncommand = /bin/true\n")
	f.Add("type = internal\ndepends-on: boot\n")
	f.Add("type = scripted\ncommand = /bin/start\nstop-command = /bin/stop\n")
	f.Add("type = bgprocess\ncommand = /usr/sbin/daemon\npid-file = /run/d.pid\n")
	f.Add("type = triggered\n")
	f.Add("type = process\ncommand = /bin/sh -c \"echo hello\"\nrestart = true\nstop-timeout = 10\n")
	f.Add("type = process\ncommand = /bin/true\noptions = runs-on-console signal-process-only\n")
	f.Add("type = process\ncommand = /bin/true\nnamespace-pid = true\nnamespace-mount = true\n")
	f.Add("type = process\ncommand = /bin/true\nnamespace-user = true\nnamespace-uid-map = 0:1000:65536\n")
	f.Add("type = process\ncommand = /bin/true\nrlimit-nofile = 1024:4096\nrlimit-core = unlimited\n")
	f.Add("type = process\ncommand = /bin/true\ncpu-affinity = 0-3 8-11\n")
	f.Add("type = process\ncommand = /bin/true\ncapabilities = cap_net_bind_service cap_sys_ptrace\n")
	f.Add("type = process\ncommand = /bin/true\nenv-file = /etc/env\nworking-dir = /tmp\n")
	f.Add("type = process\ncommand = /bin/true\nlogfile = /tmp/test.log\nlogfile-max-size = 1024\n")
	f.Add("type = process\ncommand = /bin/true\nsocket-listen = /tmp/test.sock\n")
	f.Add("type = process\ncommand += arg1\ncommand += arg2\n")
	f.Add("# comment\n\ntype = process\ncommand = /bin/true\n")
	f.Add("type = process\ncommand = /bin/true\nnice = 10\noom-score-adj = -500\nioprio = be:4\n")
	f.Add("type = process\ncommand = /bin/true\nlog-include = ^ERROR\nlog-exclude = ^DEBUG\n")
	f.Add("type = process\ncommand = /bin/true\ncron-command = /bin/check\ncron-interval = 60\n")

	f.Fuzz(func(t *testing.T, data string) {
		// Must not panic on any input
		config.Parse(strings.NewReader(data), "fuzz-svc", "fuzz-file")
	})
}

// FuzzParseIDMapping fuzzes the namespace UID/GID mapping parser.
func FuzzParseIDMapping(f *testing.F) {
	f.Add("0:1000:65536")
	f.Add("1000:2000:1000")
	f.Add("0:0:1")
	f.Add("")
	f.Add("abc:def:ghi")
	f.Add("0:0:0")
	f.Add("-1:0:100")
	f.Add("0:0:999999999999")
	f.Add(":::")
	f.Add("0:1000")

	f.Fuzz(func(t *testing.T, data string) {
		config.ParseIDMapping(data)
	})
}

// FuzzParseCPUAffinity fuzzes the CPU affinity spec parser.
func FuzzParseCPUAffinity(f *testing.F) {
	f.Add("0")
	f.Add("0-3")
	f.Add("0,1,2,3")
	f.Add("0-3 8-11")
	f.Add("0,2,4-7")
	f.Add("")
	f.Add("999999")
	f.Add("3-0")
	f.Add("a-b")
	f.Add("-1")
	f.Add("0-0")

	f.Fuzz(func(t *testing.T, data string) {
		config.ParseCPUAffinity(data)
	})
}

// FuzzParseLSBHeaders fuzzes the /etc/init.d LSB header parser. The
// parser reads from disk, so each iteration stages the fuzz input in
// a temp file before calling ParseLSBHeaders. Prefixing every line
// with `#` in part of the corpus exercises the real-world shape of
// init scripts; bare inputs exercise the tolerant fall-through path.
func FuzzParseLSBHeaders(f *testing.F) {
	f.Add("### BEGIN INIT INFO\n# Provides: foo\n# Required-Start: $network\n### END INIT INFO\n")
	f.Add("### BEGIN INIT INFO\n# Provides: a b c\n# Description: multi\n#  line\n#  continuation\n### END INIT INFO\n")
	f.Add("#!/bin/sh\necho no lsb headers here\n")
	f.Add("### BEGIN INIT INFO\n### END INIT INFO\n")
	f.Add("### BEGIN INIT INFO\n# Short-Description: X\n# Default-Start: 2 3 4 5\n# Default-Stop: 0 1 6\n### END INIT INFO\n")
	f.Add("### BEGIN INIT INFO\n# :missing-key\n# Provides :\n# :\n### END INIT INFO\n")
	f.Add("### BEGIN INIT INFO\n# Unknown-Key: ignored\n# Provides: x\n### END INIT INFO\n")
	f.Add("")

	f.Fuzz(func(t *testing.T, data string) {
		path := filepath.Join(t.TempDir(), "init-script")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Skip()
		}
		_, _ = config.ParseLSBHeaders(path)
	})
}
