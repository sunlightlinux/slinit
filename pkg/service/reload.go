package service

import (
	"errors"
	"fmt"
	"strconv"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// reload-command (systemd's ExecReload= with a command): what slinitctl
// reload-signal runs to have a running service re-read its own
// configuration, where the daemon offers a command for it rather than a
// signal (nft, avahi-daemon -r, apachectl graceful, multipathd
// reconfigure). It runs as stop-command does: the service's run-as,
// sandbox, environment and credentials ("+" for root and no sandbox),
// with $MAINPID set to the main process, as systemd sets it for
// ExecReload=. The caller waits for it, bounded by hook-timeout; a
// non-zero exit is the reload's failure, as it fails systemctl reload.
// A reload-signal set as well is sent after a command that succeeded,
// as Type=notify-reload sends ReloadSignal= after ExecReload=.

// ErrNoReloadCommand: the service has no reload-command.
var ErrNoReloadCommand = errors.New("no reload-command")

// RunReloadCommand runs svc's reload-command and waits for it. hasPID
// is false when the service is not running, which the reply reports
// as for a signal.
func (ss *ServiceSet) RunReloadCommand(svc Service) (hasPID bool, err error) {
	p, ok := svc.(*ProcessService)
	if !ok {
		return true, ErrNoReloadCommand
	}
	ss.queueMu.Lock()
	if len(p.reloadCommand) == 0 {
		ss.queueMu.Unlock()
		return true, ErrNoReloadCommand
	}
	if p.state.Load() != StateStarted || p.pid <= 0 {
		ss.queueMu.Unlock()
		return false, nil
	}
	params := process.ExecParams{
		Command:           p.reloadCommand,
		WorkingDir:        p.workingDir,
		Env:               append(p.hookEnv(), "MAINPID="+strconv.Itoa(p.pid)),
		RunAsUID:          p.effectiveRunAsUID(),
		RunAsGID:          p.effectiveRunAsGID(),
		SupplementaryGIDs: p.supplementaryGIDs,
	}
	p.Record().ApplyProcessAttrs(&params)
	if p.privHooks.Reload {
		params.FullyPrivileged()
	}
	params.ReuseCredentials = true // the running service's, left in place
	timeout := p.effectiveHookTimeout()
	pid, exitCh, err := process.StartProcess(params)
	ss.queueMu.Unlock()
	if err != nil {
		return true, fmt.Errorf("reload-command: %w", err)
	}
	ss.logger.Info("Service '%s': reload-command started (pid %d)", svc.Name(), pid)
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case exit := <-exitCh:
		if exit.ExecErr != nil {
			return true, fmt.Errorf("reload-command: %w", exit.ExecErr)
		}
		if !exit.Status.Exited() || exit.Status.ExitStatus() != 0 {
			if exit.Status.Signaled() {
				return true, fmt.Errorf("reload-command killed by %v", exit.Status.Signal())
			}
			return true, fmt.Errorf("reload-command exited %d", exit.Status.ExitStatus())
		}
		return true, nil
	case <-t.C:
		_ = syscall.Kill(pid, syscall.SIGKILL)
		<-exitCh
		return true, fmt.Errorf("reload-command did not finish within hook-timeout (%v); killed", timeout)
	}
}
