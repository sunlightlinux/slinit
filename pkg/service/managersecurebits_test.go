package service

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// slinit --securebits: every service gets the manager's bits on top of
// its own, and one that sets none still gets them (so it is routed
// through slinit-runner, which is what applies them).
func TestManagerSecurebitsAreAFloor(t *testing.T) {
	set, _ := newTestSet()
	set.SetManagerSecurebits(process.SecbitExecRestrictFile | process.SecbitExecRestrictFileLocked)

	plain := NewProcessService(set, "plain")
	set.AddService(plain)
	var p process.ExecParams
	plain.Record().ApplyProcessAttrs(&p)
	if want := process.SecbitExecRestrictFile | process.SecbitExecRestrictFileLocked; p.Securebits != want {
		t.Errorf("service with no securebits of its own: %#x, want the manager's %#x", p.Securebits, want)
	}

	own := NewProcessService(set, "own")
	set.AddService(own)
	own.Record().SetSecurebits(process.SecbitNoroot)
	p = process.ExecParams{}
	own.Record().ApplyProcessAttrs(&p)
	if want := process.SecbitNoroot | process.SecbitExecRestrictFile | process.SecbitExecRestrictFileLocked; p.Securebits != want {
		t.Errorf("service with its own securebits: %#x, want its own plus the manager's %#x", p.Securebits, want)
	}

	// Without --securebits nothing changes for a service.
	set2, _ := newTestSet()
	none := NewProcessService(set2, "none")
	set2.AddService(none)
	p = process.ExecParams{}
	none.Record().ApplyProcessAttrs(&p)
	if p.Securebits != 0 {
		t.Errorf("no manager bits, no service bits: got %#x", p.Securebits)
	}
}
