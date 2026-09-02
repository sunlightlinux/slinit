package main

import (
	"testing"
)

// FuzzParseSystemdUnit fuzzes the systemd .service/.socket/.mount
// parser. slinit-systemd-convert reads adversarial input (an
// operator's imported /etc/systemd/system tree of unknown quality),
// so a parser panic is a direct operator-facing crash. The converter
// touches ~200 systemd directives with domain-specific value parsers
// each (durations, sizes, cap sets, RestartForceExitStatus lists,
// nested key=value substitutions inside quoted strings for
// SystemCallFilter, etc.) — pretty much every non-trivial format
// systemd supports has a code path here.
//
// Invariant: must not panic on any input; the returned slinitConfig
// (partially initialised on error) must expose accessor slice/map
// fields without nil-deref.
func FuzzParseSystemdUnit(f *testing.F) {
	// Canonical shapes.
	f.Add(`[Unit]
Description=Test
Requires=network.target

[Service]
Type=simple
ExecStart=/usr/bin/true
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`)
	f.Add(`[Unit]
Description=Notify service

[Service]
Type=notify
NotifyAccess=main
ExecStart=/usr/sbin/daemon
`)
	f.Add(`[Service]
Type=oneshot
ExecStart=/bin/mount /var
RemainAfterExit=yes
`)
	// systemd-specific niche formats.
	f.Add(`[Service]
LimitNOFILE=65535
LimitAS=infinity
LimitCORE=0
`)
	f.Add(`[Service]
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_SYS_PTRACE
AmbientCapabilities=CAP_NET_BIND_SERVICE
`)
	f.Add(`[Service]
Environment="KEY1=val1" "KEY2=with space"
EnvironmentFile=/etc/foo.env
EnvironmentFile=-/etc/optional.env
`)
	f.Add(`[Service]
SystemCallFilter=@system-service ~@privileged @debug
SystemCallErrorNumber=EPERM
`)
	f.Add(`[Timer]
OnCalendar=daily
OnBootSec=15min
`)
	// Adversarial shapes.
	f.Add("")
	f.Add("[")
	f.Add("[Service\nExecStart=/bin/true")
	f.Add("[Service]\nExecStart=")
	f.Add("[Service]\n" + "Description=" + string(make([]byte, 10000)))
	f.Add("[Service]\nExecStart=\x00binary\x00")
	f.Add("=orphan\n[Service]\nExecStart=/bin/true\n")
	f.Add("[Service]\n\nExecStart=/bin/true\n\n\n[Service]\nExecStart=/bin/false")

	f.Fuzz(func(t *testing.T, data string) {
		cfg := &slinitConfig{}
		warns := parseSystemdUnit(cfg, data)
		// Warnings slice must be accessible.
		for _, w := range warns {
			_ = w.level
			_ = w.msg
		}
		// The cfg's slice/map fields must be safe to traverse
		// regardless of parse outcome (partial init on early error
		// path must not leave dangling nil-only-safe pointers).
		_ = cfg.command
		_ = cfg.stopCommand
		_ = cfg.depends
		_ = cfg.waitsFor
		_ = cfg.conditions
		_ = cfg.comments
	})
}
