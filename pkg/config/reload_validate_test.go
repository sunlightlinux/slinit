package config

import (
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func newReloadTestLoader(t *testing.T) (string, *service.ServiceSet, *DirLoader) {
	t.Helper()
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	return dir, ss, loader
}

func depNames(svc service.Service) []string {
	var out []string
	for _, d := range svc.Record().Dependencies() {
		out = append(out, d.To.Name())
	}
	return out
}

// bundle-of sets the type and the deps of a bundle, and only the load
// path applied it: a reload of a stopped bundle turned it into an empty
// type = process, and a started bundle could not be reloaded at all.
func TestReloadStoppedBundleStaysBundle(t *testing.T) {
	dir, _, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "m1", "type = internal\n")
	writeServiceFile(t, dir, "m2", "type = internal\n")
	writeServiceFile(t, dir, "bundle", "bundle-of = m1\n")

	svc, err := loader.LoadService("bundle")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	writeServiceFile(t, dir, "bundle", "bundle-of = m1, m2\n")
	newSvc, err := loader.ReloadService(svc)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if newSvc.Type() != service.TypeInternal {
		t.Fatalf("type after reload = %v, want internal", newSvc.Type())
	}
	if got := strings.Join(depNames(newSvc), ","); got != "m1,m2" {
		t.Fatalf("deps after reload = %q, want m1,m2", got)
	}
}

func TestReloadStartedBundle(t *testing.T) {
	dir, ss, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "m1", "type = internal\n")
	writeServiceFile(t, dir, "bundle", "bundle-of = m1\n")

	svc, err := loader.LoadService("bundle")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc.Start()
	ss.ProcessQueues()
	if svc.State() != service.StateStarted {
		t.Fatalf("bundle state = %v, want STARTED", svc.State())
	}
	if _, err := loader.ReloadService(svc); err != nil {
		t.Fatalf("reload of an unchanged started bundle: %v", err)
	}
	if got := strings.Join(depNames(svc), ","); got != "m1" {
		t.Fatalf("deps after reload = %q, want m1", got)
	}
}

// A fresh load refuses chroot on anything but type = process, since the
// other types would run outside the jail. A reload used to accept it.
func TestReloadRefusesChrootOnScripted(t *testing.T) {
	dir, ss, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "jailed", "type = process\ncommand = /bin/true\nchroot = /srv/jail\n")
	svc, err := loader.LoadService("jailed")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	writeServiceFile(t, dir, "jailed", "type = scripted\ncommand = /bin/true\nchroot = /srv/jail\n")
	_, err = loader.ReloadService(svc)
	if err == nil || !strings.Contains(err.Error(), "chroot is only supported for type = process") {
		t.Fatalf("reload to scripted with chroot: err = %v, want the load-time chroot refusal", err)
	}
	if got := ss.FindService("jailed", false); got != svc || got.Type() != service.TypeProcess {
		t.Fatalf("set holds %v after a refused reload, want the original process record", got)
	}
}

func TestReloadRefusesWatchdogWithoutReadyNotification(t *testing.T) {
	dir, _, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "wd", "type = process\ncommand = /bin/true\n")
	svc, err := loader.LoadService("wd")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	writeServiceFile(t, dir, "wd", "type = process\ncommand = /bin/true\nwatchdog-timeout = 1\n")
	_, err = loader.ReloadService(svc)
	if err == nil || !strings.Contains(err.Error(), "watchdog-timeout requires ready-notification") {
		t.Fatalf("reload adding watchdog-timeout: err = %v, want the load-time refusal", err)
	}
}

// load-options' variables were set only by a fresh load, so a reload
// that changed run-as kept the old USER/HOME, and one that dropped an
// option kept its variables. A value changed with setenv since is the
// operator's and stays.
func TestReloadRefreshesLoadOptionsEnv(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("needs a non-root uid so run-as = root changes USER")
	}
	me, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Skipf("current user does not resolve: %v", err)
	}
	if _, err := user.Lookup("root"); err != nil {
		t.Skipf("root does not resolve: %v", err)
	}

	dir, _, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "lo", "type = process\ncommand = /bin/true\nrun-as = root\n"+
		"load-options = export-passwd-vars export-service-name\n")
	svc, err := loader.LoadService("lo")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if env := svc.Record().GetAllEnv(); env["USER"] != "root" || env["DINIT_SERVICE"] != "lo" {
		t.Fatalf("after load: USER=%q DINIT_SERVICE=%q", env["USER"], env["DINIT_SERVICE"])
	}
	svc.Record().SetEnvVar("LOGNAME", "operator-set")

	writeServiceFile(t, dir, "lo", "type = process\ncommand = /bin/true\n"+
		"load-options = export-passwd-vars\n")
	if _, err := loader.ReloadService(svc); err != nil {
		t.Fatalf("reload: %v", err)
	}
	env := svc.Record().GetAllEnv()
	if env["USER"] != me.Username || env["HOME"] != me.HomeDir {
		t.Errorf("after dropping run-as: USER=%q HOME=%q, want %q %q",
			env["USER"], env["HOME"], me.Username, me.HomeDir)
	}
	if v, ok := env["DINIT_SERVICE"]; ok {
		t.Errorf("DINIT_SERVICE=%q survived dropping export-service-name", v)
	}
	if env["LOGNAME"] != "operator-set" {
		t.Errorf("LOGNAME = %q, want the value set at runtime kept", env["LOGNAME"])
	}
}

// checkGraph asserts parent -> s -> dep, with every edge in both lists
// naming the record the set holds for s.
func checkGraph(t *testing.T, ss *service.ServiceSet, s service.Service) {
	t.Helper()
	if got := ss.FindService("s", false); got != s {
		t.Fatalf("set holds %p for s, want the original %p", got, s)
	}
	if got := strings.Join(depNames(s), ","); got != "dep" {
		t.Errorf("s deps = %q, want dep", got)
	}
	depts := s.Record().Dependents()
	if len(depts) != 1 || depts[0].From.Name() != "parent" || depts[0].To != s {
		t.Errorf("s dependents = %v, want one edge from parent to s", depts)
	}
	parent := ss.FindService("parent", false)
	if pd := parent.Record().Dependencies(); len(pd) != 1 || pd[0].To != s {
		t.Errorf("parent deps = %v, want one edge to s", pd)
	}
	dep := ss.FindService("dep", false)
	if dd := dep.Record().Dependents(); len(dd) != 1 || dd[0].From != s {
		t.Errorf("dep dependents = %v, want one edge from s", dd)
	}
}

// A type-change reload moved the dependents and dropped the old deps
// before consumer-of was checked, so its failure left the graph half
// rewired: parent pointing at a record no longer in the set, s with no
// edges, dep naming an orphan.
func TestReloadFailedTypeChangeLeavesGraph(t *testing.T) {
	dir, ss, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "dep", "type = internal\n")
	writeServiceFile(t, dir, "prod", "type = process\ncommand = /bin/true\n")
	writeServiceFile(t, dir, "s", "type = internal\ndepends-on: dep\n")
	writeServiceFile(t, dir, "parent", "type = internal\ndepends-on: s\n")
	if _, err := loader.LoadService("parent"); err != nil {
		t.Fatalf("load: %v", err)
	}
	s := ss.FindService("s", false)
	checkGraph(t, ss, s)

	writeServiceFile(t, dir, "s", "type = process\ncommand = /bin/true\ndepends-on: dep\nconsumer-of = prod\n")
	if _, err := loader.ReloadService(s); err == nil {
		t.Fatal("reload with consumer-of a non-pipe producer succeeded")
	}
	checkGraph(t, ss, s)
	if c := ss.FindService("prod", false).Record().LogConsumer(); c != nil {
		t.Errorf("prod has consumer %q after a failed reload", c.Name())
	}
}

// The same type-change path never checked for a cycle: a dep on a
// service that already depends on this one was installed.
func TestReloadTypeChangeRefusesCycle(t *testing.T) {
	dir, ss, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "dep", "type = internal\n")
	writeServiceFile(t, dir, "s", "type = internal\ndepends-on: dep\n")
	writeServiceFile(t, dir, "parent", "type = internal\ndepends-on: s\n")
	if _, err := loader.LoadService("parent"); err != nil {
		t.Fatalf("load: %v", err)
	}
	s := ss.FindService("s", false)

	writeServiceFile(t, dir, "s", "type = process\ncommand = /bin/true\ndepends-on: dep\ndepends-on: parent\n")
	if _, err := loader.ReloadService(s); err == nil {
		t.Fatal("type-change reload closing a cycle through parent succeeded")
	}
	checkGraph(t, ss, s)
}

// An in-place reload changed the deps, then failed on consumer-of with
// the new deps left installed.
func TestReloadFailedConsumerOfLeavesDeps(t *testing.T) {
	dir, ss, loader := newReloadTestLoader(t)
	writeServiceFile(t, dir, "dep", "type = internal\n")
	writeServiceFile(t, dir, "other", "type = internal\n")
	writeServiceFile(t, dir, "prod", "type = process\ncommand = /bin/true\n")
	writeServiceFile(t, dir, "s", "type = process\ncommand = /bin/true\ndepends-on: dep\n")
	writeServiceFile(t, dir, "parent", "type = internal\ndepends-on: s\n")
	if _, err := loader.LoadService("parent"); err != nil {
		t.Fatalf("load: %v", err)
	}
	s := ss.FindService("s", false)

	writeServiceFile(t, dir, "s", "type = process\ncommand = /bin/true\ndepends-on: other\nconsumer-of = prod\n")
	if _, err := loader.ReloadService(s); err == nil {
		t.Fatal("reload with consumer-of a non-pipe producer succeeded")
	}
	checkGraph(t, ss, s)
	if o := ss.FindService("other", false); o != nil && len(o.Record().Dependents()) != 0 {
		t.Errorf("other has %d dependents after a failed reload", len(o.Record().Dependents()))
	}
}
