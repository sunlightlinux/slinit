package main

import (
	"strings"
	"testing"
)

// FuzzAnalyzeRunScript fuzzes the runit `run` script analyzer. The
// input is an arbitrary shell script (as read from /etc/sv/<svc>/run);
// analyzer looks for chpst prefixes, exec forms, subshell markers,
// and complex-logic hints. A parser panic here would abort the
// converter run for a whole runit tree.
func FuzzAnalyzeRunScript(f *testing.F) {
	f.Add(`#!/bin/sh
exec chpst -u nobody /usr/bin/mydaemon
`)
	f.Add(`#!/bin/sh
exec 2>&1
[ -e /etc/mydaemon.conf ] && exec /usr/bin/mydaemon --config /etc/mydaemon.conf
exec /usr/bin/mydaemon
`)
	f.Add(`#!/bin/sh
sv check dep1
exec /usr/bin/daemon
`)
	f.Add(`#!/bin/sh
if [ ! -e /tmp/marker ]; then
  touch /tmp/marker
fi
exec /usr/bin/daemon
`)
	f.Add(`#!/bin/execlineb -P
fdmove -c 2 1
foreground { echo starting }
/usr/bin/daemon
`)
	// Adversarial.
	f.Add("")
	f.Add("#!/bin/sh")
	f.Add("exec")
	f.Add("exec\x00daemon")
	f.Add(strings.Repeat("exec ", 1000))

	f.Fuzz(func(t *testing.T, data string) {
		cfg := &slinitConfig{}
		warns := analyzeRunScript(cfg, data)
		for _, w := range warns {
			_ = w
		}
	})
}

// FuzzParseChpst fuzzes the chpst argument parser. chpst is runit's
// pre-exec wrapper (equivalent to systemd's ExecStart pre-flags);
// slinit-runit-convert lifts its flags into slinit directives. The
// parser accepts ~15 short flags with optional args, some clustered
// (`-u user`) some standalone (`-P`), and combinations that legally
// nest (e.g. `-e /etc/env -u user:group -/ /path`).
func FuzzParseChpst(f *testing.F) {
	f.Add("-u nobody /usr/bin/daemon")
	f.Add("-u user:group -P /usr/bin/daemon")
	f.Add("-e /etc/env -u nobody /usr/bin/daemon")
	f.Add("-/ /var/lib/app /usr/bin/app")
	f.Add("-b /usr/bin/daemon -- --config /etc/x")
	f.Add("-U user:group /usr/bin/app")
	f.Add("-n +5 -N +1 /usr/bin/app")
	f.Add("-l /var/run/lock /usr/bin/app")
	f.Add("-A 60 /usr/bin/app")
	// Adversarial.
	f.Add("")
	f.Add("-")
	f.Add("-u")             // dangling arg-taking flag
	f.Add("-u ")            // dangling arg after space
	f.Add("--unknown-long") // no long flags in real chpst
	f.Add("-x -y -z")       // unknown flags
	f.Add(strings.Repeat("-P ", 500))

	f.Fuzz(func(t *testing.T, data string) {
		args := strings.Fields(data)
		cfg := &slinitConfig{}
		warns := parseChpst(cfg, args)
		for _, w := range warns {
			_ = w
		}
	})
}
