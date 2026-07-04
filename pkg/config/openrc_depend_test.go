package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mkScript drops a minimal init.d-shaped script into a temp dir so
// the parser has a real path to source.
func mkScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "svc")
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
}

func TestParseOpenRCDependSimpleDirectives(t *testing.T) {
	requireSh(t)
	body := `#!/sbin/openrc-run
depend() {
    need localmount
    use lvm modules
    after clock
    before bootmisc logger
    provide net
    keyword -docker -lxc
}
start() { :; }
`
	dep, err := ParseOpenRCDepend(mkScript(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrs(dep.Need, []string{"localmount"}) {
		t.Errorf("need=%v", dep.Need)
	}
	if !equalStrs(dep.Use, []string{"lvm", "modules"}) {
		t.Errorf("use=%v", dep.Use)
	}
	if !equalStrs(dep.After, []string{"clock"}) {
		t.Errorf("after=%v", dep.After)
	}
	if !equalStrs(dep.Before, []string{"bootmisc", "logger"}) {
		t.Errorf("before=%v", dep.Before)
	}
	if !equalStrs(dep.Provide, []string{"net"}) {
		t.Errorf("provide=%v", dep.Provide)
	}
	if !equalStrs(dep.Keyword, []string{"-docker", "-lxc"}) {
		t.Errorf("keyword=%v", dep.Keyword)
	}
}

func TestParseOpenRCDependDedupPerKind(t *testing.T) {
	requireSh(t)
	body := `#!/sbin/openrc-run
depend() {
    after clock
    after clock
    after clock modules
}
`
	dep, err := ParseOpenRCDepend(mkScript(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrs(dep.After, []string{"clock", "modules"}) {
		t.Errorf("after=%v, want [clock modules]", dep.After)
	}
}

func TestParseOpenRCDependConditional(t *testing.T) {
	// Real-world shape: netmount runs shell code inside depend().
	// Our stubs make fstabinfo a no-op, so any conditional path
	// that keys off its output takes the else branch — that's fine
	// for a compat-shim, and it's a strictly better outcome than a
	// regex scan would produce.
	requireSh(t)
	body := `#!/sbin/openrc-run
depend() {
    local mywant=""
    if [ "$always_want" = yes ]; then
        mywant="nfsclient"
    fi
    need root
    [ -n "$mywant" ] && use $mywant
    after clock
}
`
	// Neither branch of the [ -n $mywant ] triggers under our stubs
	// (mywant is unset), so use= remains empty.
	dep, err := ParseOpenRCDepend(mkScript(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrs(dep.Need, []string{"root"}) {
		t.Errorf("need=%v", dep.Need)
	}
	if !equalStrs(dep.After, []string{"clock"}) {
		t.Errorf("after=%v", dep.After)
	}
	if len(dep.Use) != 0 {
		t.Errorf("use should be empty, got %v", dep.Use)
	}
}

func TestParseOpenRCDependMissingDependIsNotAnError(t *testing.T) {
	requireSh(t)
	body := `#!/sbin/openrc-run
description="no depend"
start() { :; }
`
	dep, err := ParseOpenRCDepend(mkScript(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if dep.HasAny() {
		t.Errorf("expected empty, got %+v", dep)
	}
}

func TestParseOpenRCDependIgnoresHelpersAtSourceTime(t *testing.T) {
	// If a top-level einfo runs during source (which happens when
	// scripts have unguarded log calls) the stub swallows it and the
	// parse still succeeds.
	requireSh(t)
	body := `#!/sbin/openrc-run
einfo "source-time noise"
description="foo"
depend() {
    need bar
}
`
	dep, err := ParseOpenRCDepend(mkScript(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrs(dep.Need, []string{"bar"}) {
		t.Errorf("need=%v", dep.Need)
	}
}

func TestParseOpenRCDependWantAlias(t *testing.T) {
	requireSh(t)
	body := `#!/sbin/openrc-run
depend() {
    want modules
    need root
}
`
	dep, err := ParseOpenRCDepend(mkScript(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrs(dep.Want, []string{"modules"}) {
		t.Errorf("want=%v", dep.Want)
	}
	if !equalStrs(dep.Need, []string{"root"}) {
		t.Errorf("need=%v", dep.Need)
	}
}

func TestParseDependOutputTolerantOfNoise(t *testing.T) {
	// White-box: parseDependOutput is happy with blank lines and
	// stray text that lacks a space delimiter (it just ignores them).
	dep := parseDependOutput(`
need foo
random noise line
after bar

keyword -docker
`)
	if !equalStrs(dep.Need, []string{"foo"}) {
		t.Errorf("need=%v", dep.Need)
	}
	if !equalStrs(dep.After, []string{"bar"}) {
		t.Errorf("after=%v", dep.After)
	}
	if !equalStrs(dep.Keyword, []string{"-docker"}) {
		t.Errorf("keyword=%v", dep.Keyword)
	}
}

func TestLooksLikeOpenRCScript(t *testing.T) {
	yes := []string{
		"#!/sbin/openrc-run\n\ndepend() {}\n",
		"#! /sbin/openrc-run\ndepend() {}\n",
		"#!/usr/sbin/openrc-run\n",
	}
	no := []string{
		"#!/bin/sh\n",
		"#!/bin/bash -e\n",
		"### BEGIN INIT INFO\n",
		"",
		"just text",
	}
	for _, s := range yes {
		if !LooksLikeOpenRCScript(s) {
			t.Errorf("LooksLikeOpenRCScript(%q) = false", firstLine(s))
		}
	}
	for _, s := range no {
		if LooksLikeOpenRCScript(s) {
			t.Errorf("LooksLikeOpenRCScript(%q) = true", firstLine(s))
		}
	}
}

func TestHasAnyEmpty(t *testing.T) {
	var d *OpenRCDepend
	if d.HasAny() {
		t.Error("nil.HasAny() should be false")
	}
	d = &OpenRCDepend{}
	if d.HasAny() {
		t.Error("empty.HasAny() should be false")
	}
	d.Need = []string{"x"}
	if !d.HasAny() {
		t.Error("populated.HasAny() should be true")
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
