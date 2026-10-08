package config

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// The vtty socket directory comes from the daemon's mode (SetVTTYDir),
// not a hard-coded /run/slinit.
func TestLoaderVTTYDir(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)
	loader.SetVTTYDir("/run/user/4242/slinit")
	writeServiceFile(t, dir, "tty", "type = process\ncommand = /bin/true\nvtty = yes\n")

	svc, err := loader.LoadService("tty")
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.(*service.ProcessService).VTTYSockDir(); got != "/run/user/4242/slinit" {
		t.Errorf("vtty socket dir = %q", got)
	}
}
