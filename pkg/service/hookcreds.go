package service

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// hookCmdPreparer is a service whose auxiliary commands take its run-as
// credentials, working directory and environment. cron-command,
// health-check-command, unhealthy-command, exec-condition and
// log-processor ran as slinit itself, root under PID 1, whatever run-as
// said; systemd runs ExecCondition= and the rest as User=.
type hookCmdPreparer interface {
	// prepareHookCmdLocked sets cmd's credentials, Dir and Env from the
	// service. The caller holds queueMu, which is what a reload holds
	// while it changes them.
	prepareHookCmdLocked(cmd *exec.Cmd)
}

// prepareHookCmdLocked gives cmd what the finish-command gets: the
// run-as user and groups (applyRunAs), the working directory and the
// service environment. A service without run-as keeps running it as
// root, as before.
func (s *ProcessService) prepareHookCmdLocked(cmd *exec.Cmd) {
	s.applyRunAs(cmd)
	cmd.Dir = s.workingDir
	// The start's environment (hookEnv): BringUp computes it before the
	// predicates when an env-generator is configured, so the generator
	// runs once for the exec-condition and the start together.
	cmd.Env = s.hookCmdEnv()
}

// prepareHookCmd is prepareHookCmdLocked for a command started from a
// goroutine of its own (cron, health checks, the log processor), which
// does not hold queueMu: a reload changing run-as, working-dir or the
// environment while a check fires is a data race otherwise, and the
// check gets the values in force when it starts.
//
// The wait for the lock ends with ctx. BringDown stops the health
// checker with queueMu held and waits for its loop, so a check blocked
// here on the lock would never see that Stop. False means ctx ended
// first and cmd must not run.
func (s *ProcessService) prepareHookCmd(ctx context.Context, cmd *exec.Cmd) bool {
	mu := &s.services.queueMu
	locked := make(chan struct{})
	go func() {
		mu.RLock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		// The read lock is still taken once the holder lets go;
		// give it back then. An RWMutex is not tied to a goroutine.
		go func() {
			<-locked
			mu.RUnlock()
		}()
		return false
	}
	defer mu.RUnlock()
	// A running service's checks and cron jobs share the environment of
	// its current start (hookEnv), as its other hooks do: buildEnv would
	// run the env-generator again for every check.
	s.applyRunAs(cmd)
	cmd.Dir = s.workingDir
	cmd.Env = s.hookCmdEnv()
	return true
}

func (s *ScriptedService) prepareHookCmdLocked(cmd *exec.Cmd) {
	setRunAsCredential(cmd, s.effectiveRunAsUID(), s.effectiveRunAsGID(), s.supplementaryGIDs)
	cmd.Dir = s.workingDir
	cmd.Env = s.Record().withInheritedEnv(s.Record().BuildFullEnv())
}

func (s *BGProcessService) prepareHookCmdLocked(cmd *exec.Cmd) {
	setRunAsCredential(cmd, s.effectiveRunAsUID(), s.effectiveRunAsGID(), s.supplementaryGIDs)
	cmd.Dir = s.workingDir
	cmd.Env = s.Record().withInheritedEnv(s.buildEnv())
}

// withInheritedEnv returns env as the whole environment of a command run
// for the service: slinit's own variables, filtered by pass-environment
// and unset-environment, then env. It is how process.StartProcess builds
// the environment of the service's process; a hook given env alone lost
// PATH, HOME and everything else slinit has.
func (sr *ServiceRecord) withInheritedEnv(env []string) []string {
	return process.InheritedEnv(env, sr.inheritedEnvFilter())
}

// setRunAsCredential is ProcessService.applyRunAs for the types that
// have no such method: nothing for root, otherwise uid, gid and the
// supplementary groups (none when unset: setgroups to the empty list,
// not slinit's own groups).
func setRunAsCredential(cmd *exec.Cmd, uid, gid uint32, groups []uint32) {
	if uid == 0 && gid == 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    uid,
		Gid:    gid,
		Groups: groups,
	}
}

// prepareExecCondition is prepareHookCmdLocked for an exec-condition,
// which runs before the service's process does. The directories slinit
// manages (state-directory and the like) are created when that process
// is spawned, so a working-dir inside one may not exist yet on a first
// boot: the condition then runs from slinit's own directory instead of
// failing on the chdir and skipping the service.
func prepareExecCondition(p hookCmdPreparer) func(*exec.Cmd) {
	if p == nil {
		return nil
	}
	return func(cmd *exec.Cmd) {
		p.prepareHookCmdLocked(cmd)
		if cmd.Dir != "" {
			if st, err := os.Stat(cmd.Dir); err != nil || !st.IsDir() {
				cmd.Dir = ""
			}
		}
	}
}

// prepareHookCmdFor applies prepareHookCmd when svc has one; an
// internal service (cron-command without a process) has no run-as and
// keeps slinit's own. False: ctx ended, do not run cmd.
func prepareHookCmdFor(ctx context.Context, svc Service, cmd *exec.Cmd) bool {
	if p, ok := svc.(interface {
		prepareHookCmd(context.Context, *exec.Cmd) bool
	}); ok {
		return p.prepareHookCmd(ctx, cmd)
	}
	return true
}

// PrivilegedHooks says which of a service's commands carry systemd's
// "+" prefix ("run with full privileges"): they run as root, without
// the service's run-as user and groups, whatever run-as says. The
// parser strips the "+" from the command and sets the flag here.
//
// pre-start-command, post-start-command, finish-command, pre-stop-hook
// and control-command-* never had the service's sandbox, so for them
// "+" means exactly that: no run-as. A stop-command is started like the
// main process, sandbox included; with "+" it also goes without the
// sandbox, the capability and securebits limits and the seccomp filter,
// as systemd's ExecStop=+ does (see process.ExecParams.FullyPrivileged).
// An exec-condition carries its own flag (Predicate.Privileged).
type PrivilegedHooks struct {
	PreStart, PostStart, Stop, Finish, PreStop bool
	// Control is keyed by the upper-case signal name, as the
	// control-command-* map is.
	Control map[string]bool
}

// SetPrivilegedHooks records which commands carry the "+" prefix.
func (sr *ServiceRecord) SetPrivilegedHooks(h PrivilegedHooks) { sr.privHooks = h }

// PrivilegedHooks returns what SetPrivilegedHooks recorded.
func (sr *ServiceRecord) PrivilegedHooks() PrivilegedHooks { return sr.privHooks }

// hookCredential is the credential a hook runs with: none (slinit's
// own, root under PID 1) when privileged or when the service has no
// run-as, otherwise the run-as user and groups. Read it under queueMu.
func (s *ProcessService) hookCredential(privileged bool) *syscall.Credential {
	if privileged {
		return nil
	}
	uid, gid := s.effectiveRunAsUID(), s.effectiveRunAsGID()
	if uid == 0 && gid == 0 {
		return nil
	}
	return &syscall.Credential{Uid: uid, Gid: gid, Groups: s.supplementaryGIDs}
}
