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
// ExecReload=. The caller waits for it, bounded by start-timeout, as
// systemd bounds ExecReload= by TimeoutStartSec= (hook-timeout without one); a
// non-zero exit is the reload's failure, as it fails systemctl reload.
// A reload-signal set as well is sent after a command that succeeded,
// as Type=notify-reload sends ReloadSignal= after ExecReload=.

// ErrNoReloadCommand: the service has no reload-command.
var ErrNoReloadCommand = errors.New("no reload-command")

// reloadSpec is what RunReloadCommand needs of a service type: the
// command, its exec parameters with $MAINPID, and the bound. ok is false
// when there is no command; pid 0 when the service is not running.
// Caller holds queueMu.
type reloadSpec struct {
	params  process.ExecParams
	pid     int
	running bool // started: a scripted job is, with no process (pid 0)
	timeout time.Duration
}

func (s *ProcessService) reloadSpec() (reloadSpec, bool) {
	if len(s.reloadCommand) == 0 {
		return reloadSpec{}, false
	}
	if s.state.Load() != StateStarted || s.pid <= 0 {
		return reloadSpec{}, true
	}
	params := process.ExecParams{
		Command:           s.reloadCommand,
		WorkingDir:        s.workingDir,
		Env:               append(s.hookEnv(), "MAINPID="+strconv.Itoa(s.pid)),
		RunAsUID:          s.effectiveRunAsUID(),
		RunAsGID:          s.effectiveRunAsGID(),
		SupplementaryGIDs: s.supplementaryGIDs,
	}
	return reloadSpec{params: params, pid: s.pid, running: true, timeout: reloadBound(s.startTimeout, s.effectiveHookTimeout())}, true
}

func (s *BGProcessService) reloadSpec() (reloadSpec, bool) {
	if len(s.reloadCommand) == 0 {
		return reloadSpec{}, false
	}
	pid := s.daemonPID
	if s.state.Load() != StateStarted || pid <= 0 {
		return reloadSpec{}, true
	}
	params := process.ExecParams{
		Command:           s.reloadCommand,
		WorkingDir:        s.workingDir,
		Env:               append(s.buildEnv(), "MAINPID="+strconv.Itoa(pid)),
		RunAsUID:          s.effectiveRunAsUID(),
		RunAsGID:          s.effectiveRunAsGID(),
		SupplementaryGIDs: s.supplementaryGIDs,
	}
	return reloadSpec{params: params, pid: pid, running: true, timeout: reloadBound(s.startTimeout, defaultFinishTimeout)}, true
}

// A scripted job (a oneshot that stays started, nftables, apparmor)
// reloads by its command while started: there is no main process, so
// no $MAINPID, as systemd leaves it unset for a oneshot.
func (s *ScriptedService) reloadSpec() (reloadSpec, bool) {
	if len(s.reloadCommand) == 0 {
		return reloadSpec{}, false
	}
	if s.state.Load() != StateStarted {
		return reloadSpec{}, true
	}
	params := process.ExecParams{
		Command:           s.reloadCommand,
		WorkingDir:        s.workingDir,
		Env:               s.Record().BuildEnvWithFile(s.envFile),
		RunAsUID:          s.effectiveRunAsUID(),
		RunAsGID:          s.effectiveRunAsGID(),
		SupplementaryGIDs: s.supplementaryGIDs,
	}
	return reloadSpec{params: params, running: true, timeout: reloadBound(s.startTimeout, defaultFinishTimeout)}, true
}

// reloadBound is the reload's time limit: start-timeout, as systemd
// bounds ExecReload= by TimeoutStartSec=, else the hook bound.
func reloadBound(startTimeout, hook time.Duration) time.Duration {
	if startTimeout > 0 {
		return startTimeout
	}
	return hook
}

// RunReloadCommand runs svc's reload-command and waits for it. hasPID
// is false when the service is not running, which the reply reports
// as for a signal. What the command leaves behind in its process group
// is killed when it ends, as stop-command's is.
func (ss *ServiceSet) RunReloadCommand(svc Service) (hasPID bool, err error) {
	r, ok := svc.(interface{ reloadSpec() (reloadSpec, bool) })
	if !ok {
		return true, ErrNoReloadCommand
	}
	ss.queueMu.Lock()
	spec, has := r.reloadSpec()
	if !has {
		ss.queueMu.Unlock()
		return true, ErrNoReloadCommand
	}
	if !spec.running {
		ss.queueMu.Unlock()
		return false, nil
	}
	params := spec.params
	svc.Record().ApplyProcessAttrs(&params)
	if svc.Record().privHooks.Reload {
		params.FullyPrivileged()
	}
	params.ReuseCredentials = true // the running service's, left in place
	pid, exitCh, err := process.StartProcess(params)
	ss.queueMu.Unlock()
	if err != nil {
		return true, fmt.Errorf("reload-command: %w", err)
	}
	ss.logger.Info("Service '%s': reload-command started (pid %d)", svc.Name(), pid)
	defer process.KillProcessGroup(pid)
	t := time.NewTimer(spec.timeout)
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
		return true, fmt.Errorf("reload-command did not finish within %v; killed", spec.timeout)
	}
}
