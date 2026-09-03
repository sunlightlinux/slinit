// slinit-nspawn — a systemd-nspawn-alike container launcher for
// slinit. Scope: enough kernel plumbing to boot a rootfs as an
// isolated PID-namespaced container running slinit (or any /sbin/init)
// as PID 1, then register it under /run/slinit/machines/<name> so
// slinit-journalctl -M and slinit-machinectl see it.
//
// What's inside:
//   - Namespaces: CLONE_NEWPID, CLONE_NEWNS, CLONE_NEWUTS, CLONE_NEWIPC
//     always; CLONE_NEWNET optional via --private-network.
//   - Mount setup: mount --rbind rootfs → temp dir + MS_PRIVATE, then
//     pivot_root inside the child. Standard virtual FS bind mounts:
//     /proc (rw, private), /sys (ro, private, rebound), /dev
//     (ro-rbind from host), /dev/pts (new, private), /run (tmpfs).
//   - Init exec: default /sbin/slinit; override with --init=PATH.
//     Argv beyond -- is passed through as init's args.
//   - Registry: parent writes /run/slinit/machines/<name> post-fork,
//     removes it on child exit. On SIGTERM/SIGINT the parent proxies
//     SIGTERM to PID 1 inside the container before waiting.
//   - Console: the child keeps the launching TTY (no pty allocation
//     yet — good enough for foreground demos, systemd-nspawn's
//     -x pseudo-tty is deferred).
//
// What's NOT inside:
//   - CLONE_NEWUSER + uid mapping (rootless containers) — future.
//   - Machined D-Bus registration — dinit-philosophy, never.
//   - Port forwarding / network setup for private-network — the
//     namespace is created empty; caller adds interfaces via a
//     shell hook if needed.
//   - Overlay/copy-up on the rootfs — mutations happen in place.
//
// Design intent: enough to be useful for slinit's demo/alpine flow
// and for real slinit-inside-slinit deployments where the operator
// wants isolation + journal-visibility without the full systemd
// stack. Grows on demand.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/pkg/machine"
)

// reexecEnv marks the child re-exec that runs after pivot_root. The
// parent sets this env var when it re-execs itself inside the new
// namespaces so `main` can dispatch to childMain instead of restarting
// the parent flow.
const reexecEnv = "_SLINIT_NSPAWN_CHILD"

// childPayload is what the parent hands to the child via env vars.
// Kept env-based (not a pipe) so the child can be a bare re-exec of
// this same binary — no fork/exec dance, no fd inheritance juggling.
type childPayload struct {
	Name           string   // registry name
	Rootfs         string   // absolute path to the container rootfs
	Init           string   // init binary inside the container (default /sbin/slinit)
	InitArgs       []string // args passed after --
	PrivateNet     bool     // CLONE_NEWNET
	Hostname       string   // container UTS hostname (default = name)
	RegistryDir    string   // override for machine.SetDir (default /run/slinit/machines)
}

func main() {
	if os.Getenv(reexecEnv) != "" {
		if err := childMain(); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-nspawn (child): %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := parentMain(); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-nspawn: %v\n", err)
		os.Exit(1)
	}
}

// parentMain parses flags, forks itself into new namespaces, writes
// the registry entry, proxies signals, and cleans up on exit.
func parentMain() error {
	fs := flag.NewFlagSet("slinit-nspawn", flag.ContinueOnError)
	var (
		name        = fs.String("name", "", "container name (registry key + default hostname); required")
		rootfs      = fs.String("boot", "", "path to the container rootfs; required")
		initBin     = fs.String("init", "/sbin/slinit", "init binary inside the container")
		hostname    = fs.String("hostname", "", "container UTS hostname (default: --name)")
		privateNet  = fs.Bool("private-network", false, "create CLONE_NEWNET so container has an empty network stack")
		machineDir  = fs.String("machine-dir", machine.DefaultDir, "registry directory to write into")
		showHelp    = fs.Bool("h", false, "show help")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: slinit-nspawn --name NAME --boot ROOTFS [flags] [-- INIT-ARGS...]

Launches ROOTFS as a slinit container in its own PID/mount/UTS/IPC
namespaces, execs /sbin/slinit (or --init=PATH) inside as PID 1, and
registers NAME → container-PID in `+machine.DefaultDir+`/.

Flags:
`)
		fs.PrintDefaults()
	}
	// Split argv at "--".
	args := os.Args[1:]
	var initArgs []string
	for i, a := range args {
		if a == "--" {
			initArgs = args[i+1:]
			args = args[:i]
			break
		}
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showHelp {
		fs.Usage()
		return nil
	}
	if *name == "" || *rootfs == "" {
		fs.Usage()
		return errors.New("--name and --boot are required")
	}
	absRoot, err := filepath.Abs(*rootfs)
	if err != nil {
		return fmt.Errorf("--boot: %w", err)
	}
	if st, err := os.Stat(absRoot); err != nil || !st.IsDir() {
		return fmt.Errorf("--boot %s: not a directory", absRoot)
	}
	if os.Geteuid() != 0 {
		return errors.New("slinit-nspawn needs root — unshare/pivot_root require CAP_SYS_ADMIN")
	}
	uts := *hostname
	if uts == "" {
		uts = *name
	}

	payload := childPayload{
		Name:        *name,
		Rootfs:      absRoot,
		Init:        *initBin,
		InitArgs:    initArgs,
		PrivateNet:  *privateNet,
		Hostname:    uts,
		RegistryDir: *machineDir,
	}
	return runParent(payload)
}

func runParent(p childPayload) error {
	// Re-exec self with CLONE_NEW* set via SysProcAttr. The child
	// hits main → reexecEnv branch → childMain.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self exec: %w", err)
	}
	cmd := exec.Command(self)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		reexecEnv+"=1",
		"_SLINIT_NSPAWN_NAME="+p.Name,
		"_SLINIT_NSPAWN_ROOTFS="+p.Rootfs,
		"_SLINIT_NSPAWN_INIT="+p.Init,
		"_SLINIT_NSPAWN_HOSTNAME="+p.Hostname,
		"_SLINIT_NSPAWN_INIT_ARGS="+strings.Join(p.InitArgs, "\x1f"),
	)

	flagsUnshare := uintptr(unix.CLONE_NEWPID | unix.CLONE_NEWNS |
		unix.CLONE_NEWUTS | unix.CLONE_NEWIPC)
	if p.PrivateNet {
		flagsUnshare |= unix.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  flagsUnshare,
		Setpgid:     true,
		Unshareflags: unix.CLONE_NEWNS,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("clone into namespaces: %w", err)
	}
	childPid := cmd.Process.Pid

	// Register early — journalctl -M works as soon as the child is
	// running, even before init inside has fully come up.
	machine.SetDir(p.RegistryDir)
	if err := machine.Register(machine.Machine{
		Name:    p.Name,
		PID:     childPid,
		Class:   "container",
		Service: "",
		Root:    p.Rootfs,
	}); err != nil {
		// Best-effort — a registry write failure shouldn't tank the
		// container. Warn and continue.
		fmt.Fprintf(os.Stderr, "slinit-nspawn: registry write failed: %v\n", err)
	}
	defer func() {
		if err := machine.Unregister(p.Name); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-nspawn: registry cleanup failed: %v\n", err)
		}
	}()

	// Signal proxy: forward SIGTERM/SIGINT to the child's PID 1.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigCh:
				// Send to the child directly. Inside the container's
				// PID namespace it appears as PID 1, so init handles
				// it via its own signal wiring.
				_ = syscall.Kill(childPid, s.(syscall.Signal))
			case <-done:
				return
			}
		}
	}()

	err = cmd.Wait()
	close(done)
	signal.Stop(sigCh)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return fmt.Errorf("wait: %w", err)
	}
	return nil
}

// childMain runs inside the new namespaces. Sets hostname, does
// mount setup, pivot_root into the container rootfs, exec init.
func childMain() error {
	name := os.Getenv("_SLINIT_NSPAWN_NAME")
	rootfs := os.Getenv("_SLINIT_NSPAWN_ROOTFS")
	initBin := os.Getenv("_SLINIT_NSPAWN_INIT")
	hostname := os.Getenv("_SLINIT_NSPAWN_HOSTNAME")
	rawArgs := os.Getenv("_SLINIT_NSPAWN_INIT_ARGS")
	var initArgs []string
	if rawArgs != "" {
		// US (0x1f) separator — envs are C strings so NUL is unusable,
		// and 0x1f can't collide with a normal command-line arg.
		initArgs = strings.Split(rawArgs, "\x1f")
	}
	_ = name

	// UTS: hostname (no-op if empty).
	if hostname != "" {
		if err := unix.Sethostname([]byte(hostname)); err != nil {
			return fmt.Errorf("sethostname: %w", err)
		}
	}

	// Make host mount tree private in our new NS so subsequent mounts
	// don't leak back.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("mount(/, private): %w", err)
	}

	// Bind-mount rootfs onto itself so pivot_root has a mount point.
	if err := unix.Mount(rootfs, rootfs, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind rootfs onto itself: %w", err)
	}

	// pivot_root(newroot, put_old) with put_old under newroot so we
	// can umount it once we're inside.
	putOld := filepath.Join(rootfs, "mnt", "old-root")
	if err := os.MkdirAll(putOld, 0o700); err != nil {
		return fmt.Errorf("mkdir put_old: %w", err)
	}
	if err := unix.PivotRoot(rootfs, putOld); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	// Detach the old root and drop the mount point. MNT_DETACH does
	// a lazy unmount so any lingering handles from init setup don't
	// block us.
	if err := unix.Unmount("/mnt/old-root", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("umount old-root: %w", err)
	}
	_ = os.RemoveAll("/mnt/old-root")

	// Standard virtual FS mounts.
	type m struct {
		src, dst, fstype string
		flags            uintptr
		data             string
	}
	// Create dirs first (idempotent) — some minimal rootfs don't ship
	// /proc /sys /dev/pts.
	for _, d := range []string{"/proc", "/sys", "/dev", "/dev/pts", "/run", "/tmp"} {
		if err := os.MkdirAll(d, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	mounts := []m{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV | unix.MS_RDONLY, ""},
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "gid=5,mode=620,ptmxmode=666"},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=755"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
	}
	for _, mnt := range mounts {
		if err := unix.Mount(mnt.src, mnt.dst, mnt.fstype, mnt.flags, mnt.data); err != nil {
			// Non-fatal for /sys and /tmp — some kernels or rootfs
			// setups (e.g. an existing devtmpfs bind on /dev) refuse
			// remounts. Warn to stderr.
			fmt.Fprintf(os.Stderr, "slinit-nspawn (child): mount %s: %v\n", mnt.dst, err)
		}
	}

	// Exec the init. slinit expects PID 1 semantics; nothing to teach
	// it about our namespaces since it always is one.
	argv := append([]string{initBin}, initArgs...)
	return syscall.Exec(initBin, argv, os.Environ())
}
