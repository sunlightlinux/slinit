package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupFakeProc drops a scratch /proc/sys tree the tests can write
// into, then repoints procSysRoot at it. Restored on cleanup.
func setupFakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc", "sys"), 0755); err != nil {
		t.Fatal(err)
	}
	saved := procSysRoot
	procSysRoot = filepath.Join(root, "proc", "sys")
	t.Cleanup(func() { procSysRoot = saved })
	return root
}

// prepKernelKey pre-creates the target file (kernel exposes each
// tunable as a pre-existing file) so applySpec's WriteFile doesn't
// have to create it.
func prepKernelKey(t *testing.T, key string) string {
	t.Helper()
	full := filepath.Join(procSysRoot, key)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestApplySpecWritesValuePlusNewline(t *testing.T) {
	setupFakeProc(t)
	target := prepKernelKey(t, "vm/swappiness")
	s := spec{key: "vm/swappiness", rawKey: "vm.swappiness", value: "60"}
	if err := applySpec(s, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "60\n" {
		t.Errorf("got %q, want %q", got, "60\n")
	}
}

func TestApplySpecIgnoreFlagSwallowsErrorInLooseMode(t *testing.T) {
	setupFakeProc(t)
	// Point at a key we haven't pre-created — WriteFile fails because
	// the parent dir doesn't exist. With ignoreErrors, the error must
	// come back as errIgnored, not a hard failure.
	s := spec{
		key: "nonexistent/tunable", rawKey: "nonexistent.tunable",
		value: "1", ignoreErrors: true,
	}
	err := applySpec(s, false)
	if err == nil {
		t.Fatal("expected errIgnored, got nil")
	}
	if _, ok := err.(errIgnored); !ok {
		t.Errorf("want errIgnored, got %T: %v", err, err)
	}
}

func TestApplySpecStrictOverridesIgnoreFlag(t *testing.T) {
	setupFakeProc(t)
	s := spec{
		key: "nonexistent/tunable", rawKey: "nonexistent.tunable",
		value: "1", ignoreErrors: true,
	}
	err := applySpec(s, true) // strict=true
	if err == nil {
		t.Fatal("expected error under strict")
	}
	if _, ok := err.(errIgnored); ok {
		t.Errorf("strict must not return errIgnored")
	}
}

func TestApplyFilesEndToEnd(t *testing.T) {
	setupFakeProc(t)
	swap := prepKernelKey(t, "vm/swappiness")
	fwd := prepKernelKey(t, "net/ipv4/ip_forward")

	dir := t.TempDir()
	confPath := filepath.Join(dir, "s.conf")
	body := "# comment\nvm.swappiness = 25\nnet.ipv4.ip_forward = 1\n-does.not.exist = 1\n"
	if err := os.WriteFile(confPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	res := applyFiles([]string{confPath}, false, false)
	if res.applied != 2 {
		t.Errorf("applied=%d, want 2", res.applied)
	}
	if res.ignored != 1 {
		t.Errorf("ignored=%d, want 1 (missing target)", res.ignored)
	}
	if len(res.errors) != 0 {
		t.Errorf("unexpected errors: %v", res.errors)
	}
	if got, _ := os.ReadFile(swap); string(got) != "25\n" {
		t.Errorf("swap = %q", got)
	}
	if got, _ := os.ReadFile(fwd); string(got) != "1\n" {
		t.Errorf("fwd = %q", got)
	}
}

func TestApplyFilesReportsParseErrors(t *testing.T) {
	setupFakeProc(t)
	dir := t.TempDir()
	confPath := filepath.Join(dir, "bad.conf")
	swap := prepKernelKey(t, "vm/swappiness")
	fwd := prepKernelKey(t, "net/ipv4/ip_forward")
	// The middle line is malformed (no '='): it is an error (exit 1,
	// as systemd-sysctl returns the first parse error) but only that
	// line is skipped — the lines around it still apply.
	body := "vm.swappiness = 10\nno equals\nnet.ipv4.ip_forward = 1\n"
	if err := os.WriteFile(confPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	res := applyFiles([]string{confPath}, false, false)
	if len(res.errors) != 1 || !strings.Contains(res.errors[0].Error(), "bad.conf:2") {
		t.Errorf("errors = %v, want one naming bad.conf:2", res.errors)
	}
	if res.applied != 2 {
		t.Errorf("applied=%d, want 2 (bad line skipped, rest applied)", res.applied)
	}
	if got, _ := os.ReadFile(swap); string(got) != "10\n" {
		t.Errorf("swappiness = %q", got)
	}
	if got, _ := os.ReadFile(fwd); string(got) != "1\n" {
		t.Errorf("ip_forward = %q", got)
	}
}

func TestApplyResultString(t *testing.T) {
	r := &applyResult{applied: 5, ignored: 2, errors: []error{nil, nil}}
	got := r.String()
	if !strings.Contains(got, "applied=5") ||
		!strings.Contains(got, "ignored=2") ||
		!strings.Contains(got, "errors=2") {
		t.Errorf("String()=%q", got)
	}
}

func readKey(t *testing.T, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(procSysRoot, key))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// systemd's own 50-default.conf (openSUSE, copied verbatim into
// testdata) mixes globs, `-key` exclusions and plain keys. The old
// parser rejected `net.ipv4.conf.*.rp_filter` and the bare
// `-net.ipv4.conf.all.rp_filter`, and dropped the WHOLE file — so
// fs.protected_*, kernel.kptr_restrict etc. were never applied.
func TestApplyFilesSystemd50Default(t *testing.T) {
	setupFakeProc(t)
	keys := []string{
		"net/ipv4/icmp_echo_ignore_broadcasts",
		"net/ipv6/conf/default/use_tempaddr",
		"net/ipv4/ping_group_range",
		"fs/inotify/max_user_instances",
		"kernel/sysrq",
		"fs/protected_hardlinks", "fs/protected_symlinks",
		"fs/protected_regular", "fs/protected_fifos",
		"kernel/kptr_restrict", "kernel/dmesg_restrict",
	}
	for _, ifc := range []string{"all", "default", "lo", "eth0", "eth0.100"} {
		keys = append(keys,
			"net/ipv4/conf/"+ifc+"/rp_filter",
			"net/ipv4/conf/"+ifc+"/promote_secondaries")
	}
	for _, k := range keys {
		prepKernelKey(t, k)
	}
	res := applyFiles([]string{filepath.Join("testdata", "50-default.conf")}, false, false)
	if len(res.errors) != 0 {
		t.Fatalf("errors: %v", res.errors)
	}
	want := map[string]string{
		"net/ipv4/icmp_echo_ignore_broadcasts": "1\n",
		"net/ipv6/conf/default/use_tempaddr":   "1\n",
		"net/ipv4/ping_group_range":            "0 2147483647\n",
		"fs/inotify/max_user_instances":        "8192\n",
		"kernel/sysrq":                         "184\n",
		"fs/protected_hardlinks":               "1\n",
		"fs/protected_symlinks":                "1\n",
		"fs/protected_regular":                 "2\n",
		"fs/protected_fifos":                   "2\n",
		"kernel/kptr_restrict":                 "1\n",
		"kernel/dmesg_restrict":                "1\n",
		// glob hits every interface, the VLAN with a dot included...
		"net/ipv4/conf/default/rp_filter":           "2\n",
		"net/ipv4/conf/lo/rp_filter":                "2\n",
		"net/ipv4/conf/eth0/rp_filter":              "2\n",
		"net/ipv4/conf/eth0.100/rp_filter":          "2\n",
		"net/ipv4/conf/default/promote_secondaries": "1\n",
		"net/ipv4/conf/eth0/promote_secondaries":    "1\n",
		// ...except the excluded "all".
		"net/ipv4/conf/all/rp_filter":           "0\n",
		"net/ipv4/conf/all/promote_secondaries": "0\n",
	}
	for k, v := range want {
		if got := readKey(t, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// Glob expansion against procSysRoot, in both separator spellings,
// with `-key` exclusions and explicit keys taking precedence over
// the glob regardless of order (systemd's apply_glob_option).
func TestApplyFilesGlobExclusionAndPrecedence(t *testing.T) {
	setupFakeProc(t)
	for _, ifc := range []string{"all", "lo", "eth0", "eth0.100", "wlan0"} {
		prepKernelKey(t, "net/ipv4/conf/"+ifc+"/forwarding")
		prepKernelKey(t, "net/ipv6/conf/"+ifc+"/forwarding")
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "10-a.conf")
	b := filepath.Join(dir, "20-b.conf")
	if err := os.WriteFile(a, []byte(
		"net.ipv4.conf.eth0.forwarding = 7\n"+ // explicit, before the glob
			"net.ipv4.conf.*.forwarding = 1\n"+
			"-net.ipv4.conf.eth0/100.forwarding\n"+ // dotted exclusion of the VLAN
			"net/ipv6/conf/*/forwarding = 3\n"+ // slashed glob
			"net.ipv6.conf.wlan0.forwarding = 9\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(
		"-net/ipv6/conf/lo/forwarding\n"+ // exclusion from a later file
			"net.ipv4.conf.lo.forwarding = 5\n"+ // explicit, after the glob
			"net.ipv4.conf.*.forwarding = 1\n"), 0644); err != nil { // same value again: no reorder
		t.Fatal(err)
	}
	res := applyFiles([]string{a, b}, false, false)
	if len(res.errors) != 0 {
		t.Fatalf("errors: %v", res.errors)
	}
	want := map[string]string{
		"net/ipv4/conf/all/forwarding":      "1\n",
		"net/ipv4/conf/wlan0/forwarding":    "1\n",
		"net/ipv4/conf/eth0/forwarding":     "7\n",
		"net/ipv4/conf/lo/forwarding":       "5\n",
		"net/ipv4/conf/eth0.100/forwarding": "0\n",
		"net/ipv6/conf/all/forwarding":      "3\n",
		"net/ipv6/conf/eth0/forwarding":     "3\n",
		"net/ipv6/conf/eth0.100/forwarding": "3\n",
		"net/ipv6/conf/wlan0/forwarding":    "9\n",
		"net/ipv6/conf/lo/forwarding":       "0\n",
	}
	for k, v := range want {
		if got := readKey(t, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// A later `-key` cancels an earlier assignment of the same key, as
// in systemd (the exclusion replaces the option in the set).
func TestApplyFilesExclusionCancelsEarlierAssignment(t *testing.T) {
	setupFakeProc(t)
	prepKernelKey(t, "vm/swappiness")
	conf := filepath.Join(t.TempDir(), "x.conf")
	if err := os.WriteFile(conf, []byte("vm.swappiness = 60\n-vm.swappiness\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res := applyFiles([]string{conf}, false, false)
	if len(res.errors) != 0 || res.applied != 0 {
		t.Fatalf("res = %s %v", res, res.errors)
	}
	if got := readKey(t, "vm/swappiness"); got != "0\n" {
		t.Errorf("swappiness = %q, want untouched", got)
	}
}

// A tunable this kernel lacks is not an error (systemd-sysctl warns
// and goes on unless --strict); neither is a glob that matches
// nothing. The old code failed the pass, so the boot service failed
// on any config written for another kernel.
func TestApplyFilesMissingKeyNotAnError(t *testing.T) {
	setupFakeProc(t)
	prepKernelKey(t, "vm/swappiness")
	conf := filepath.Join(t.TempDir(), "m.conf")
	body := "vm.no_such_tunable = 1\nnosuch.subsys.tunable = 1\nnet.no.such.* = 1\nvm.swappiness = 5\n"
	if err := os.WriteFile(conf, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	res := applyFiles([]string{conf}, false, false)
	if len(res.errors) != 0 {
		t.Errorf("errors: %v", res.errors)
	}
	if res.applied != 1 || res.ignored != 3 || len(res.warnings) != 3 {
		t.Errorf("res = %s warnings=%v, want applied=1 ignored=3 and 3 warnings", res, res.warnings)
	}
	if _, err := os.Stat(filepath.Join(procSysRoot, "vm", "no_such_tunable")); err == nil {
		t.Errorf("missing tunable was created")
	}
	if got := readKey(t, "vm/swappiness"); got != "5\n" {
		t.Errorf("swappiness = %q", got)
	}

	// --strict makes every miss an error again.
	res = applyFiles([]string{conf}, true, false)
	if len(res.errors) != 3 {
		t.Errorf("strict errors = %v, want 3", res.errors)
	}
}

// A refused write (EACCES here) is ignored outside --strict, as
// systemd treats EROFS/EACCES/EPERM.
func TestApplyFilesWriteRefusedIgnored(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file modes")
	}
	setupFakeProc(t)
	ro := prepKernelKey(t, "kernel/ro_tunable")
	if err := os.Chmod(ro, 0444); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(t.TempDir(), "r.conf")
	if err := os.WriteFile(conf, []byte("kernel.ro_tunable = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res := applyFiles([]string{conf}, false, false); len(res.errors) != 0 || res.ignored != 1 {
		t.Errorf("loose: %s %v", res, res.errors)
	}
	if res := applyFiles([]string{conf}, true, false); len(res.errors) != 1 {
		t.Errorf("strict: %s %v", res, res.errors)
	}
}
