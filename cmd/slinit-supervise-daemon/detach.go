package main

import (
	"os/exec"
	"syscall"
)

// setDetached puts the supervisor in its own session so it isn't tied
// to the calling shell's controlling terminal. Kept in its own file
// so a future BSD build can replace it with a stub.
func setDetached(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
