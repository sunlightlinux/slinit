package service

import (
	"fmt"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// exitDetail renders a child's exit in the form an operator can act on.
//
// It exists because `%v` on a syscall.WaitStatus prints the raw packed
// integer: a stop-command that exited 1 logs as "status 256", and one
// that exited 5 as "status 1280" (5<<8). scripted.go had already
// learned this and carried its own copy of the switch, with a comment
// saying the raw form is "confusing every time"; process.go's
// stop-command path had not, and it is what an operator actually reads
// during a shutdown. Shared rather than copied a third time.
func exitDetail(exit process.ChildExit) string {
	switch {
	case exit.Signaled():
		return fmt.Sprintf("killed by signal %d (%s)",
			int(exit.Status.Signal()), exit.Status.Signal())
	case exit.Status.Exited():
		return fmt.Sprintf("exit code %d", exit.Status.ExitStatus())
	default:
		return fmt.Sprintf("raw wait status %d", int(exit.Status))
	}
}
