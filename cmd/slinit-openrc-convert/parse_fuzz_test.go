package main

import (
	"strings"
	"testing"
)

// FuzzParseOpenrcScript fuzzes the /etc/init.d/<svc> OpenRC-style
// script parser. slinit-openrc-convert reads any script the operator
// points it at; the parser walks for `depend()` bodies, `command`
// variables, `start_pre`/`start_post` hooks, `pidfile` declarations,
// and various openrc-run helpers. Arbitrary shell content flows
// through here.
//
// Invariant: no panic on any input. Warnings + slinitConfig field
// traversal must be safe.
func FuzzParseOpenrcScript(f *testing.F) {
	f.Add(`#!/sbin/openrc-run
name="mydaemon"
command="/usr/bin/mydaemon"
command_args="--config /etc/mydaemon.conf"
pidfile="/run/mydaemon.pid"

depend() {
    need net
    after network
    before display-manager
}
`)
	f.Add(`#!/sbin/openrc-run
description="Test service"
command=/usr/bin/foo
command_background=true

start_pre() {
    checkpath -d /run/foo
}
`)
	f.Add(`#!/bin/sh
# Legacy sysvinit-shaped script.
### BEGIN INIT INFO
# Provides:          foo
# Required-Start:    $network
### END INIT INFO
start() {
    /usr/bin/foo &
}
`)
	// depend() shapes.
	f.Add(`depend() {
    need net
    use dns
    provide mta
    after logger
    before mail
    keyword -jail -shutdown
}
`)
	// Adversarial.
	f.Add("")
	f.Add("#!/sbin/openrc-run")
	f.Add(`depend() {`)
	f.Add(`command=`)
	f.Add(`command="with \"escaped\" quotes"`)
	f.Add(`command="` + strings.Repeat("a", 10000) + `"`)
	f.Add("command=/bin/\x00binary")
	f.Add(strings.Repeat("depend() { need net; }\n", 500))

	f.Fuzz(func(t *testing.T, data string) {
		cfg := &slinitConfig{}
		warns := parseOpenrcScript(cfg, data, "openrc-run")
		for _, w := range warns {
			_ = w
		}
	})
}

// FuzzParseDepend fuzzes the depend() body parser in isolation. The
// body is what the OpenRC script's `depend()` function contains
// between braces — a mini-DSL of `need`, `use`, `after`, `before`,
// `provide`, `keyword`. Each token combination is a code path here.
func FuzzParseDepend(f *testing.F) {
	f.Add("need net")
	f.Add("after network")
	f.Add("before display-manager")
	f.Add("use dns")
	f.Add("provide mta")
	f.Add("keyword -jail -shutdown")
	f.Add("need net\nafter network\nbefore x")
	// Adversarial.
	f.Add("")
	f.Add("need")
	f.Add("unknown_keyword foo")
	f.Add("need\x00net")
	f.Add(strings.Repeat("need net\n", 500))

	f.Fuzz(func(t *testing.T, data string) {
		cfg := &slinitConfig{}
		lines := strings.Split(data, "\n")
		warns := parseDepend(cfg, lines)
		for _, w := range warns {
			_ = w
		}
	})
}
