package process

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// StartProcess starts a child process with the given parameters.
// It returns the PID and a channel that will receive exactly one ChildExit
// when the process terminates. The caller must read from the channel.
//
// If the command cannot be started at all (e.g., binary not found),
// an error is returned and no channel/PID is produced.
func StartProcess(params ExecParams) (int, <-chan ChildExit, error) {
	if len(params.Command) == 0 {
		return 0, nil, &ExecError{Stage: StageDoExec, Err: os.ErrInvalid}
	}

	// Load any service-shipped AppArmor profile into the kernel before
	// the child starts. This is a kernel-side operation (not per-task),
	// so the parent does it; a failure aborts the start because a
	// security control must never silently degrade to unconfined.
	if params.AppArmorLoadProfile != "" {
		if err := loadAppArmorProfile(params.AppArmorLoadProfile); err != nil {
			return 0, nil, &ExecError{Stage: StageDoExec, Err: err}
		}
	}

	// Create the service's runtime/state/cache/logs/configuration
	// directories before the child starts, owned by the run-as user.
	// A failure aborts the start: a service that cannot get its
	// StateDirectory must not run as if it had one.
	if len(params.ServiceDirs) > 0 {
		if err := ensureServiceDirs(params.ServiceDirs, params.RunAsUID, params.RunAsGID); err != nil {
			return 0, nil, &ExecError{Stage: StageDoExec, Err: err}
		}
	}

	// Populate $CREDENTIALS_DIRECTORY from the configured sources.
	// A failure aborts the start — running without an expected
	// credential is worse than not running, and secrets are by design
	// the kind of input you must not silently degrade away from.
	credDir, err := SetupCredentials(params.ServiceName, params.Credentials, params.RunAsUID, params.RunAsGID)
	if err != nil {
		return 0, nil, &ExecError{Stage: StageDoExec, Err: err}
	}
	if credDir != "" {
		// Add CREDENTIALS_DIRECTORY to env, building Env if needed.
		if cap(params.Env) == 0 {
			params.Env = []string{"CREDENTIALS_DIRECTORY=" + credDir}
		} else {
			params.Env = append(params.Env, "CREDENTIALS_DIRECTORY="+credDir)
		}
	}

	// mlockall and set_mempolicy operate on the calling process, so
	// they cannot be applied to a fork()ed child from outside. When
	// either is configured we prepend slinit-runner to the command —
	// the runner applies the syscalls then exec()s the real program
	// in place, so the running process is the one slinit ultimately
	// supervises (PID and signals match).
	command := params.Command
	wrapped := needsRunnerWrap(params) && params.RunnerPath != ""
	if wrapped {
		command = wrapWithRunner(params)
	}

	cmd := exec.Command(command[0], command[1:]...)

	// argv[0] override (runit chpst -b). Only apply in the unwrapped
	// path — wrapWithRunner emits --argv0 so the runner does the
	// substitution across its own exec.
	if !wrapped && params.Argv0 != "" {
		cmd.Args[0] = params.Argv0
	}

	// Working directory
	if params.WorkingDir != "" {
		cmd.Dir = params.WorkingDir
	}

	// Environment: cache os.Environ() once, reuse for all env additions
	baseEnv := os.Environ()
	if len(params.Env) > 0 {
		cmd.Env = make([]string, 0, len(baseEnv)+len(params.Env)+3)
		cmd.Env = append(cmd.Env, baseEnv...)
		cmd.Env = append(cmd.Env, params.Env...)
	}

	// Set process group so we can signal the group later
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// Credential setup (run as different user/group).
	//
	// When the service combines a non-root credential with any runner
	// feature (mlockall, mempolicy, AppArmor, sandbox, seccomp,
	// hardening) the credential drop has to happen *inside*
	// slinit-runner instead of at fork time: every one of those ops
	// either needs CAP_SYS_ADMIN / CAP_IPC_LOCK / CAP_MAC_ADMIN, all of
	// which a UID drop to (say) nobody would strip before the runner
	// ever started. wrapWithRunner emits matching
	// --run-as-uid/--run-as-gid/--ambient-cap flags so the runner does
	// the drop after setup, keeping AmbientCaps preserved via
	// PR_SET_KEEPCAPS + a fresh PR_CAP_AMBIENT_RAISE.
	deferRunAsToRunner := needsRunnerWrap(params) && params.RunnerPath != "" &&
		(params.RunAsUID != 0 || params.RunAsGID != 0)
	if !deferRunAsToRunner && (params.RunAsUID != 0 || params.RunAsGID != 0) {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: params.RunAsUID,
			Gid: params.RunAsGID,
		}
	}

	// Ambient capabilities (applied in child between fork and exec).
	// Same caveat as above: when run-as is deferred to the runner, the
	// kernel would clear the ambient set on the UID drop, so we let the
	// runner re-raise them after setresuid.
	if len(params.AmbientCaps) > 0 && !deferRunAsToRunner {
		cmd.SysProcAttr.AmbientCaps = params.AmbientCaps
	}

	// Chroot support
	if params.Chroot != "" {
		cmd.SysProcAttr.Chroot = params.Chroot
	}

	// New session (setsid) — overrides default Setpgid
	if params.NewSession && !params.OnConsole {
		cmd.SysProcAttr.Setpgid = false
		cmd.SysProcAttr.Setsid = true
	}

	// Namespace isolation via clone flags
	if params.Cloneflags != 0 {
		cmd.SysProcAttr.Cloneflags = params.Cloneflags

		// User namespace requires UID/GID mappings
		if params.Cloneflags&syscall.CLONE_NEWUSER != 0 {
			if len(params.UidMappings) > 0 {
				cmd.SysProcAttr.UidMappings = params.UidMappings
			} else {
				cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
					{ContainerID: 0, HostID: os.Getuid(), Size: 1},
				}
			}
			if len(params.GidMappings) > 0 {
				cmd.SysProcAttr.GidMappings = params.GidMappings
			} else {
				cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
					{ContainerID: 0, HostID: os.Getgid(), Size: 1},
				}
			}
		}
	}

	// Lock file: acquire exclusive non-blocking flock before exec.
	// O_NOFOLLOW prevents an attacker from pre-creating the path as a
	// symlink to a system file — slinit runs as root so following the
	// link would let any local user influence which file gets locked
	// (DoS by holding a lock on a real lockfile elsewhere).
	var lockFD *os.File
	if params.LockFile != "" {
		var err error
		lockFD, err = os.OpenFile(params.LockFile, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return 0, nil, &ExecError{Stage: StageDoExec, Err: fmt.Errorf("lock-file open: %w", err)}
		}
		if err := syscall.Flock(int(lockFD.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lockFD.Close()
			return 0, nil, &ExecError{Stage: StageDoExec, Err: fmt.Errorf("lock-file already locked: %s", params.LockFile)}
		}
		// lockFD stays open for the lifetime of the process (flock released on close)
	}

	// Virtual TTY: open slave PTY as stdin/stdout/stderr, create new session
	var ptySlaveFd *os.File
	if params.PTYSlave != "" {
		var err error
		ptySlaveFd, err = os.OpenFile(params.PTYSlave, os.O_RDWR|syscall.O_NOCTTY, 0)
		if err == nil {
			cmd.Stdin = ptySlaveFd
			cmd.Stdout = ptySlaveFd
			cmd.Stderr = ptySlaveFd
			cmd.SysProcAttr.Setpgid = false
			cmd.SysProcAttr.Setsid = true
			cmd.SysProcAttr.Setctty = true
			cmd.SysProcAttr.Ctty = 0 // fd 0 (stdin) = pty slave
		}
	}

	// Console handling: open /dev/console, create new session, set controlling terminal
	var consoleFd *os.File
	if params.PTYSlave == "" && params.OnConsole {
		var err error
		consoleFd, err = os.OpenFile("/dev/console", os.O_RDWR, 0)
		if err != nil {
			// Fallback to inherited stdin/stdout/stderr
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		} else {
			cmd.Stdin = consoleFd
			cmd.Stdout = consoleFd
			cmd.Stderr = consoleFd
			// Create new session so the child is session leader.
			cmd.SysProcAttr.Setpgid = false // Setsid implies new pgid
			cmd.SysProcAttr.Setsid = true
			// Only set /dev/console as controlling terminal when unmask-intr
			// is enabled. With a controlling terminal, the child receives
			// terminal-generated signals (SIGINT from Ctrl+C, SIGQUIT, SIGTSTP).
			// Without it, the child can still read/write the console via fds
			// but is shielded from keyboard signals — matching dinit's default
			// behavior of masking SIGINT for console services.
			if params.UnmaskSigint {
				cmd.SysProcAttr.Setctty = true
				cmd.SysProcAttr.Ctty = 0 // fd 0 (stdin) = /dev/console
			}
		}
	} else if params.OutputPipe != nil {
		// Capture stdout/stderr to a pipe for log buffering or piping.
		// When ErrorPipe is set, stderr goes to a separate pipe (used by
		// the error-logger feature for piping stderr to a different command).
		cmd.Stdout = params.OutputPipe
		if params.ErrorPipe != nil {
			cmd.Stderr = params.ErrorPipe
		} else {
			cmd.Stderr = params.OutputPipe
		}
	}

	// Wire stdin from input pipe (consumer-of)
	if params.InputPipe != nil && !params.OnConsole {
		cmd.Stdin = params.InputPipe
	}

	// Close stdin/stdout/stderr: redirect to /dev/null (runit -0/-1/-2 style)
	if params.CloseStdin && cmd.Stdin == nil {
		devNull, err := os.Open("/dev/null")
		if err == nil {
			cmd.Stdin = devNull
			defer devNull.Close()
		}
	}
	if params.CloseStdout && cmd.Stdout == nil {
		devNull, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
		if err == nil {
			cmd.Stdout = devNull
			defer devNull.Close()
		}
	}
	if params.CloseStderr && cmd.Stderr == nil {
		devNull, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
		if err == nil {
			cmd.Stderr = devNull
			defer devNull.Close()
		}
	}

	// Set up extra file descriptors for the child process.
	// ExtraFiles[i] becomes fd 3+i in the child.
	//
	// Ordering: socket activation fd MUST be fd 3 (systemd convention),
	// so socket goes first. Readiness notification pipe follows.
	var extraFdNullFiles []*os.File // /dev/null files to close after start

	// Socket activation: pre-opened listening sockets starting at fd 3.
	// Order: fd-store handoffs first (so a restart re-attaches its old
	// sockets at the same fd numbers), then SocketFD + extras.
	listenNames := make([]string, 0, len(params.StoredFDs))
	nFDs := 0
	for _, e := range params.StoredFDs {
		if e.File == nil {
			continue
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, e.File)
		listenNames = append(listenNames, e.Name)
		nFDs++
	}
	if params.SocketFD != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.SocketFD)
		nFDs++
		for _, extraFD := range params.ExtraSocketFDs {
			cmd.ExtraFiles = append(cmd.ExtraFiles, extraFD)
			nFDs++
		}
	}
	if nFDs > 0 {
		listenEnv := fmt.Sprintf("LISTEN_FDS=%d", nFDs)
		if cmd.Env == nil {
			cmd.Env = append(baseEnv[:len(baseEnv):len(baseEnv)], listenEnv)
		} else {
			cmd.Env = append(cmd.Env, listenEnv)
		}
		// LISTEN_FDNAMES is only set when we have names to advertise
		// (fd-store entries always carry one; socket-listen sockets
		// don't, so we fill blanks for them).
		if len(listenNames) > 0 {
			for len(listenNames) < nFDs {
				listenNames = append(listenNames, "")
			}
			cmd.Env = append(cmd.Env, "LISTEN_FDNAMES="+strings.Join(listenNames, ":"))
		}
		// LISTEN_PID will be set after cmd.Start() (see below)
	}

	// $NOTIFY_SOCKET for sd_notify FDSTORE=1 messages.
	if params.NotifySocketPath != "" {
		notifyEnv := "NOTIFY_SOCKET=" + params.NotifySocketPath
		if cmd.Env == nil {
			cmd.Env = append(baseEnv[:len(baseEnv):len(baseEnv)], notifyEnv)
		} else {
			cmd.Env = append(cmd.Env, notifyEnv)
		}
	}

	// Readiness notification pipe
	if params.NotifyPipe != nil {
		targetFD := 3 // default: first ExtraFile slot = fd 3
		if params.ForceNotifyFD >= 3 {
			targetFD = params.ForceNotifyFD
		}

		// If socket already occupies fd 3, shift notify target up
		baseOffset := len(cmd.ExtraFiles)
		if targetFD < 3+baseOffset {
			targetFD = 3 + baseOffset
		}

		// Fill ExtraFiles up to the target slot
		slotIndex := targetFD - 3
		for len(cmd.ExtraFiles) < slotIndex {
			devNull, err := os.Open("/dev/null")
			if err != nil {
				return 0, nil, &ExecError{Stage: StageArrangeFDs, Err: err}
			}
			extraFdNullFiles = append(extraFdNullFiles, devNull)
			cmd.ExtraFiles = append(cmd.ExtraFiles, devNull)
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.NotifyPipe)

		// Set environment variable with the actual fd number
		actualFD := 3 + len(cmd.ExtraFiles) - 1
		if params.NotifyVar != "" {
			if cmd.Env == nil {
				cmd.Env = make([]string, len(baseEnv), len(baseEnv)+2)
				copy(cmd.Env, baseEnv)
			}
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", params.NotifyVar, actualFD))
		}
	}

	// Control socket fd (pass-cs-fd): append after other extra fds
	if params.ControlSocketFD != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, params.ControlSocketFD)
		csFD := 3 + len(cmd.ExtraFiles) - 1
		if cmd.Env == nil {
			cmd.Env = make([]string, len(baseEnv), len(baseEnv)+2)
			copy(cmd.Env, baseEnv)
		}
		cmd.Env = append(cmd.Env, fmt.Sprintf("SLINIT_CS_FD=%d", csFD))
	}

	// Cgroup pre-attach: open the target cgroup as a directory fd and
	// route fork through clone3+CLONE_INTO_CGROUP. Without this, the
	// child shell could fork (e.g. setsid'd) grandchildren in the root
	// cgroup before the parent's post-fork cgroup.procs write lands,
	// defeating `kill-all-on-stop`. Best-effort: on older kernels we
	// fall back to the post-fork attach in applyPostForkAttrs.
	var cgroupFD *os.File
	if params.CgroupPath != "" {
		if fd, err := PrepareCgroupForFD(params.CgroupPath, params.CgroupSettings); err == nil {
			cgroupFD = fd
			cmd.SysProcAttr.UseCgroupFD = true
			cmd.SysProcAttr.CgroupFD = int(fd.Fd())
		}
	}
	defer func() {
		if cgroupFD != nil {
			cgroupFD.Close()
		}
	}()

	// Per-service umask: apply just before fork so the child inherits it,
	// then restore immediately. Safe because every StartProcess call runs
	// serialized under ServiceSet.queueMu — no other goroutine forks or
	// changes the process umask concurrently. Done this late so slinit's
	// own file/dir creation above keeps the daemon's normal umask.
	prevUmask := -1
	if params.Umask != nil {
		prevUmask = syscall.Umask(int(*params.Umask))
	}

	// Start the process
	err = cmd.Start()
	if prevUmask >= 0 {
		syscall.Umask(prevUmask)
	}
	if err != nil {
		if ptySlaveFd != nil {
			ptySlaveFd.Close()
		}
		if consoleFd != nil {
			consoleFd.Close()
		}
		for _, f := range extraFdNullFiles {
			f.Close()
		}
		if lockFD != nil {
			lockFD.Close()
		}
		// Roll back the credentials tmpfs so a failed start does not
		// leave a populated /run/credentials/<svc>/ behind.
		if credDir != "" {
			_ = CleanupCredentials(params.ServiceName)
		}
		return 0, nil, &ExecError{Stage: StageDoExec, Err: err}
	}

	// Close our copy of PTY slave and console fd after fork
	if ptySlaveFd != nil {
		ptySlaveFd.Close()
	}
	if consoleFd != nil {
		consoleFd.Close()
	}

	// Close /dev/null filler fds after fork
	for _, f := range extraFdNullFiles {
		f.Close()
	}

	pid := cmd.Process.Pid

	// Apply post-fork process attributes.
	// These are best-effort: failures are logged but don't prevent startup.
	if errs := applyPostForkAttrs(pid, params); len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "slinit: pid %d: post-fork attr warning: %v\n", pid, err)
		}
	}

	exitCh := make(chan ChildExit, 1)

	// Register with the exit router BEFORE the wait goroutine starts. If
	// PID 1's SIGCHLD handler reaps this child before cmd.Wait() does,
	// the router delivers the real WaitStatus here; otherwise cmd.Wait()
	// is the source of truth. Without this, an orphan-reaper win silently
	// loses the exit code and finish-command sees ExitStatus()==0.
	routedCh := DefaultExitRouter.Register(pid)

	// Goroutine that waits for the process to finish
	go func() {
		defer close(exitCh)
		defer DefaultExitRouter.Unregister(pid)
		// Release lock file when process exits
		if lockFD != nil {
			defer lockFD.Close()
		}

		// Run cmd.Wait() in a sub-goroutine so we can race it against
		// the router. Buffered cap 1 so the sub-goroutine's send never
		// blocks if the router wins.
		waitDone := make(chan syscall.WaitStatus, 1)
		go func() {
			err := cmd.Wait()
			var status syscall.WaitStatus
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					status = exitErr.Sys().(syscall.WaitStatus)
				}
			}
			waitDone <- status
		}()

		var status syscall.WaitStatus
		select {
		case status = <-routedCh:
			// Orphan reaper got there first. Drain the cmd.Wait() goroutine
			// in the background so it doesn't leak — Wait4 will eventually
			// return ECHILD now that the child is reaped.
			go func() { <-waitDone }()
		case status = <-waitDone:
			// cmd.Wait() won the race; routedCh will be unregistered by
			// the deferred Unregister above.
		}

		exitCh <- ChildExit{
			PID:    pid,
			Status: status,
		}
	}()

	return pid, exitCh, nil
}

// SignalProcess sends a signal to a process.
// If signalGroupOnly is false, signals the process group (negative PID).
func SignalProcess(pid int, sig syscall.Signal, processOnly bool) error {
	if pid <= 0 {
		return nil
	}
	if processOnly {
		return syscall.Kill(pid, sig)
	}
	// Signal the process group
	return syscall.Kill(-pid, sig)
}

// KillProcessGroup sends SIGKILL to all remaining processes in a process
// group and reaps their zombie entries. The group leader should already have
// been reaped by cmd.Wait(). Because each service uses Setpgid, the pgid
// equals the leader's PID. Using wait4(-pgid) is safe: it only reaps
// children in this specific group, never other managed service processes.
func KillProcessGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	// Kill remaining group members (ESRCH if group is already empty)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	// Reap zombies from this specific group
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-pgid, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			break
		}
	}
}

// ensureServiceDirs creates each directory (parents included), sets its
// mode explicitly (MkdirAll is umask-masked), and chowns it to the
// run-as user when one is configured. Missing parents are created; an
// existing directory is left in place but its mode/owner are corrected.
func ensureServiceDirs(dirs []ServiceDir, uid, gid uint32) error {
	for _, d := range dirs {
		if err := os.MkdirAll(d.Path, d.Mode); err != nil {
			return fmt.Errorf("service directory %s: %w", d.Path, err)
		}
		if err := os.Chmod(d.Path, d.Mode); err != nil {
			return fmt.Errorf("service directory %s: chmod: %w", d.Path, err)
		}
		if uid != 0 || gid != 0 {
			if err := os.Chown(d.Path, int(uid), int(gid)); err != nil {
				return fmt.Errorf("service directory %s: chown: %w", d.Path, err)
			}
		}
	}
	return nil
}

// loadAppArmorProfile parses and replaces an AppArmor profile by
// running `apparmor_parser -r <path>`. The binary normally lives in
// /sbin; fall back there if it is not on PATH (apparmor_parser is
// frequently outside a minimal service PATH).
func loadAppArmorProfile(path string) error {
	bin, err := exec.LookPath("apparmor_parser")
	if err != nil {
		bin = "/sbin/apparmor_parser"
		if _, statErr := os.Stat(bin); statErr != nil {
			return fmt.Errorf("apparmor_parser not found: %w", err)
		}
	}
	out, runErr := exec.Command(bin, "-r", path).CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("apparmor_parser -r %s: %w: %s",
			path, runErr, strings.TrimSpace(string(out)))
	}
	return nil
}

// needsRunnerWrap reports whether the command needs to be prefixed with
// slinit-runner because mlockall(2) and/or set_mempolicy(2) — both
// per-calling-process syscalls — were requested.
func needsRunnerWrap(p ExecParams) bool {
	return p.MlockallFlags != 0 || p.NumaMempolicySet ||
		p.AppArmorProfile != "" || p.DebugStop ||
		sandboxActive(p) || seccompActive(p) || hardeningActive(p) ||
		len(p.BoundingCaps) > 0 || p.NoNewPrivs
}

// hardeningActive reports whether any Restrict*/Protect* knob is set.
// Each active knob expands at runner-side to a seccomp deny filter or
// a mount op; an unwrapped command would silently lose the protection.
func hardeningActive(p ExecParams) bool {
	return p.ProtectKernelTunables || p.ProtectKernelModules ||
		p.ProtectKernelLogs || p.ProtectClock ||
		p.ProtectControlGroups || p.ProtectHostname ||
		p.LockPersonality
}

// seccompActive reports whether any seccomp field is set. seccomp must
// be installed in the calling task (the one that becomes the service),
// which is the runner, so any presence flips this on.
func seccompActive(p ExecParams) bool {
	return len(p.SeccompFilter) > 0 || len(p.SeccompLogFilter) > 0 ||
		p.SeccompErrorAction != "" || len(p.SeccompArchitectures) > 0
}

// sandboxActive reports whether any filesystem-sandbox field is set.
// The runner needs the wrap whenever ANY of them is requested because
// mount(2) and tmpfs setup happen inside the child's mount namespace
// before exec.
func sandboxActive(p ExecParams) bool {
	return p.PrivateTmp ||
		p.ProtectSystem != "" || len(p.ReadOnlyPaths) > 0 || len(p.ReadWritePaths) > 0 ||
		p.ProtectHome != "" || len(p.InaccessiblePaths) > 0 ||
		p.ProtectProc != "" || p.ProcSubset != "" ||
		len(p.BindPaths) > 0 || len(p.BindReadOnlyPaths) > 0 ||
		len(p.TemporaryFileSystem) > 0
}

// wrapWithRunner returns a new argv that invokes slinit-runner with
// the appropriate flags before the real command.
func wrapWithRunner(p ExecParams) []string {
	args := []string{p.RunnerPath}
	if p.MlockallFlags != 0 {
		args = append(args, "--mlockall="+strconv.Itoa(p.MlockallFlags))
	}
	if p.NumaMempolicySet {
		args = append(args, "--mempolicy="+mempolicyName(p.NumaMempolicy))
		if len(p.NumaNodes) > 0 {
			args = append(args, "--numa-nodes="+formatNodeList(p.NumaNodes))
		}
	}
	if p.AppArmorProfile != "" {
		args = append(args, "--apparmor="+p.AppArmorProfile)
	}
	if p.DebugStop {
		args = append(args, "--debug")
	}
	// Filesystem sandbox flags. These are applied inside the child's
	// fresh mount namespace (CLONE_NEWNS, auto-implied by the loader) by
	// slinit-runner before exec'ing the real service.
	if p.PrivateTmp {
		args = append(args, "--private-tmp")
	}
	if p.ProtectSystem != "" {
		args = append(args, "--protect-system="+p.ProtectSystem)
	}
	for _, ro := range p.ReadOnlyPaths {
		args = append(args, "--read-only-path="+ro)
	}
	for _, rw := range p.ReadWritePaths {
		args = append(args, "--read-write-path="+rw)
	}
	if p.ProtectHome != "" {
		args = append(args, "--protect-home="+p.ProtectHome)
	}
	for _, ip := range p.InaccessiblePaths {
		args = append(args, "--inaccessible-path="+ip)
	}
	if p.ProtectProc != "" {
		args = append(args, "--protect-proc="+p.ProtectProc)
	}
	if p.ProcSubset != "" {
		args = append(args, "--proc-subset="+p.ProcSubset)
	}
	for _, b := range p.BindPaths {
		args = append(args, "--bind-path="+b)
	}
	for _, b := range p.BindReadOnlyPaths {
		args = append(args, "--bind-ro-path="+b)
	}
	for _, t := range p.TemporaryFileSystem {
		args = append(args, "--tmpfs-path="+t)
	}
	for _, s := range p.SeccompFilter {
		args = append(args, "--syscall-filter="+s)
	}
	for _, a := range p.SeccompArchitectures {
		args = append(args, "--syscall-arch="+a)
	}
	if p.SeccompErrorAction != "" {
		args = append(args, "--syscall-action="+p.SeccompErrorAction)
	}
	for _, s := range p.SeccompLogFilter {
		args = append(args, "--syscall-log="+s)
	}
	if p.ProtectKernelTunables {
		args = append(args, "--protect-kernel-tunables")
	}
	if p.ProtectKernelModules {
		args = append(args, "--protect-kernel-modules")
	}
	if p.ProtectKernelLogs {
		args = append(args, "--protect-kernel-logs")
	}
	if p.ProtectClock {
		args = append(args, "--protect-clock")
	}
	if p.ProtectControlGroups {
		args = append(args, "--protect-control-groups")
	}
	if p.ProtectHostname {
		args = append(args, "--protect-hostname")
	}
	if p.LockPersonality {
		args = append(args, "--lock-personality")
	}
	// Deferred run-as + ambient caps. Always emit when a non-root
	// credential is configured: by the time wrapWithRunner is being
	// called the parent has already decided to use the runner, so the
	// runner is responsible for the credential drop in every code path
	// where it runs. (See exec.go: deferRunAsToRunner.)
	if p.RunAsUID != 0 || p.RunAsGID != 0 {
		args = append(args, "--run-as-uid="+strconv.Itoa(int(p.RunAsUID)))
		args = append(args, "--run-as-gid="+strconv.Itoa(int(p.RunAsGID)))
		for _, c := range p.AmbientCaps {
			args = append(args, "--ambient-cap="+strconv.FormatUint(uint64(c), 10))
		}
	}
	// Bounding-set narrowing: pass the positive keep-list; the runner
	// PR_CAPBSET_DROPs every cap not on it. Must run before the
	// setresuid drop above (the kernel strips CAP_SETPCAP at UID
	// change, which is the gate for PR_CAPBSET_DROP).
	for _, c := range p.BoundingCaps {
		args = append(args, "--bounding-cap="+strconv.FormatUint(uint64(c), 10))
	}
	// no-new-privs: parent-side applyNoNewPrivs is a stub (prctl can't
	// target a peer task — attrs.go:345). Defer to the runner, which sets
	// PR_SET_NO_NEW_PRIVS on its own task before exec.
	if p.NoNewPrivs {
		args = append(args, "--no-new-privs")
	}
	// argv[0] override survives the runner's own exec via --argv0.
	if p.Argv0 != "" {
		args = append(args, "--argv0="+p.Argv0)
	}
	args = append(args, "--")
	args = append(args, p.Command...)
	return args
}

func mempolicyName(mode uint32) string {
	switch mode {
	case unix.MPOL_DEFAULT:
		return "default"
	case unix.MPOL_BIND:
		return "bind"
	case unix.MPOL_PREFERRED:
		return "preferred"
	case unix.MPOL_INTERLEAVE:
		return "interleave"
	case unix.MPOL_LOCAL:
		return "local"
	default:
		return "default"
	}
}

func formatNodeList(nodes []uint) string {
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = strconv.FormatUint(uint64(n), 10)
	}
	return strings.Join(parts, ",")
}
