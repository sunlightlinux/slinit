// slinit-nspawn — a systemd-nspawn-alike container launcher for
// slinit. Scope: enough kernel plumbing to boot a rootfs as an
// isolated PID-namespaced container running slinit (or any /sbin/init)
// as PID 1, then register it under /run/slinit/machines/<name> so
// slinit-journalctl -M and slinit-machinectl see it.
//
// What's inside:
//   - Namespaces: CLONE_NEWPID, CLONE_NEWNS, CLONE_NEWUTS, CLONE_NEWIPC,
//     CLONE_NEWCGROUP always; CLONE_NEWNET optional via --private-network.
//   - Mount setup: mount --rbind rootfs → temp dir + MS_PRIVATE, then
//     pivot_root inside the child. Standard virtual FS bind mounts:
//     /proc (rw, private), /sys (ro, private, rebound), /dev (fresh
//     nodev tmpfs with the standard nodes bound in from the host),
//     /dev/pts (new instance), /dev/shm, /run and /tmp (tmpfs). The
//     rootfs is nodev, not nosuid. /proc/sys and nspawn's other
//     read-only /proc entries are read-only, its inaccessible ones
//     are covered with /dev/null.
//   - Capabilities: bounding set narrowed to systemd-nspawn's default
//     retained set.
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
	"runtime"
	"strconv"
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
	Name        string   // registry name
	Rootfs      string   // absolute path to the container rootfs
	Init        string   // init binary inside the container (default /sbin/slinit)
	InitArgs    []string // args passed after --
	PrivateNet  bool     // CLONE_NEWNET
	Hostname    string   // container UTS hostname (default = name)
	RegistryDir string   // override for machine.SetDir (default /run/slinit/machines)
}

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("slinit-nspawn version %s\n", version)
		return
	}
	if os.Getenv(reexecEnv) != "" {
		if err := childMain(); err != nil {
			fmt.Fprintf(os.Stderr, "slinit-nspawn (child): %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := parentMain(); err != nil {
		var st exitStatus
		if errors.As(err, &st) {
			os.Exit(int(st))
		}
		fmt.Fprintf(os.Stderr, "slinit-nspawn: %v\n", err)
		os.Exit(1)
	}
}

// exitStatus carries the container's exit status out of runParent,
// so that its deferred registry cleanup runs before main exits with it.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("container exited %d", int(e)) }

// exitCode is the status a shell would report for ee: the exit code,
// or 128+signal for a container init killed by a signal.
func exitCode(ee *exec.ExitError) int {
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}

// parentMain parses flags, forks itself into new namespaces, writes
// the registry entry, proxies signals, and cleans up on exit.
func parentMain() error {
	fs := flag.NewFlagSet("slinit-nspawn", flag.ContinueOnError)
	var (
		name       = fs.String("name", "", "container name (registry key + default hostname); required")
		rootfs     = fs.String("boot", "", "path to the container rootfs; required")
		initBin    = fs.String("init", "/sbin/slinit", "init binary inside the container")
		hostname   = fs.String("hostname", "", "container UTS hostname (default: --name)")
		privateNet = fs.Bool("private-network", false, "create CLONE_NEWNET so container has an empty network stack")
		machineDir = fs.String("machine-dir", machine.DefaultDir, "registry directory to write into")
		showHelp   = fs.Bool("h", false, "show help")
	)
	// --help as well as -h. Only "h" was registered, so --help reached
	// Go's flag package as an undefined flag: it printed the usage and
	// returned ErrHelp, which this command turned into exit 1. Every
	// other slinit tool that takes a help flag accepts both spellings.
	fs.BoolVar(showHelp, "help", false, "show help")

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
	if p.PrivateNet {
		cmd.Env = append(cmd.Env, "_SLINIT_NSPAWN_PRIVATE_NET=1")
	}

	// CLONE_NEWCGROUP as systemd-nspawn: the container sees its own
	// cgroup as the root, not the host's hierarchy and its place in it.
	flagsUnshare := uintptr(unix.CLONE_NEWPID | unix.CLONE_NEWNS |
		unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWCGROUP)
	if p.PrivateNet {
		flagsUnshare |= unix.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:   flagsUnshare,
		Setpgid:      true,
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
			return exitStatus(exitCode(ee))
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
	privateNet := os.Getenv("_SLINIT_NSPAWN_PRIVATE_NET") != ""
	var initArgs []string
	if rawArgs != "" {
		// US (0x1f) separator — envs are C strings so NUL is unusable,
		// and 0x1f can't collide with a normal command-line arg.
		initArgs = strings.Split(rawArgs, "\x1f")
	}
	_ = name

	// The bounding and inheritable sets are per thread: the thread that
	// narrows them must be the one that execs the init.
	runtime.LockOSThread()

	// Nothing the launcher inherited may reach the container: a host
	// file or directory fd would outlive pivot_root. Marked
	// close-on-exec rather than closed, since the Go runtime holds fds
	// of its own until the exec. Done here, while the host /proc the
	// fallback reads is still our root.
	if err := cloexecFrom(3); err != nil {
		return fmt.Errorf("mark inherited fds close-on-exec: %w", err)
	}

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
	// nodev on the whole tree, so a device node shipped in the image is
	// unusable. systemd-nspawn leaves the root dev and closes devices
	// with the scope's DevicePolicy=closed instead; slinit-nspawn sets up
	// no device cgroup, so the mount flag is what stands in for it. Not
	// nosuid: the container's setuid binaries must keep working.
	if err := unix.MountSetattr(unix.AT_FDCWD, rootfs, unix.AT_RECURSIVE,
		&unix.MountAttr{Attr_set: unix.MOUNT_ATTR_NODEV}); err != nil {
		return fmt.Errorf("rootfs nodev: %w", err)
	}
	// The device nodes the container's /dev gets, taken while the host
	// /dev is still reachable.
	nodes, err := cloneDevNodes()
	if err != nil {
		return err
	}
	// /proc and /sys too: inside a user namespace the kernel mounts
	// them only while a full mount of the same kind is already visible
	// (mount_too_revealing), and once the old root is gone there is none.
	proc, procErr := newMount("proc", unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NOEXEC|unix.MOUNT_ATTR_NODEV)
	sys, sysErr := newMount("sysfs", unix.MOUNT_ATTR_RDONLY|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NOEXEC|unix.MOUNT_ATTR_NODEV)

	// pivot_root(".", ".") from inside the new root stacks the old
	// root on top of it, and the umount below takes it off again. No
	// put_old directory: creating one inside the image would follow
	// the image's own symlinks (an absolute /mnt link) on the host.
	if err := unix.Chdir(rootfs); err != nil {
		return fmt.Errorf("chdir %s: %w", rootfs, err)
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	// Detach the old root. MNT_DETACH does a lazy unmount so any
	// lingering handles from init setup don't block us.
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("umount old root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	// Standard virtual FS mounts.
	type m struct {
		src, dst, fstype string
		flags            uintptr
		data             string
	}
	// Create dirs first (idempotent) — some minimal rootfs don't ship
	// /proc /sys /dev/pts.
	for _, d := range []string{"/proc", "/sys", "/dev", "/run", "/tmp"} {
		if err := os.MkdirAll(d, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	if err := setupDev(nodes); err != nil {
		return err
	}
	mounts := []m{
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=620,gid=5"},
		{"tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_STRICTATIME, "mode=1777"},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=755"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
	}
	for _, mnt := range mounts {
		if err := unix.Mount(mnt.src, mnt.dst, mnt.fstype, mnt.flags, mnt.data); err != nil {
			// Non-fatal — some kernels or rootfs
			// setups (e.g. an existing devtmpfs bind on /dev) refuse
			// remounts. Warn to stderr.
			fmt.Fprintf(os.Stderr, "slinit-nspawn (child): mount %s: %v\n", mnt.dst, err)
		}
	}
	attach(sys, sysErr, "/sys")
	if attach(proc, procErr, "/proc") {
		if err := protectProc(privateNet); err != nil {
			return err
		}
	}

	if err := dropCaps(privateNet); err != nil {
		return err
	}

	// Exec the init. slinit expects PID 1 semantics; nothing to teach
	// it about our namespaces since it always is one.
	argv := append([]string{initBin}, initArgs...)
	if err := syscall.Exec(initBin, argv, os.Environ()); err != nil {
		// execve ENOENT is famously ambiguous — the target binary
		// missing, its dynamic interpreter missing, or a #! shebang
		// pointing at nothing all look identical. Investigate before
		// bubbling up so the operator gets a concrete pointer.
		hint := diagnoseExecFailure(initBin, err)
		return fmt.Errorf("exec %s: %w%s", initBin, err, hint)
	}
	return nil
}

// devNodes are the host device nodes a container's /dev gets: the
// required set of systemd-nspawn's copy_devnodes.
var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

// cloneDevNodes takes a detached bind mount of each host /dev node in
// devNodes, for setupDev to attach once the host is out of reach.
// systemd-nspawn mknods them and bind-mounts only where mknod is
// refused; a bind always works, in a user namespace too, and keeps
// working on a nodev /dev.
func cloneDevNodes() ([]int, error) {
	fds := make([]int, len(devNodes))
	for i, n := range devNodes {
		fd, err := unix.OpenTree(unix.AT_FDCWD, "/dev/"+n, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
		if err != nil {
			return nil, fmt.Errorf("clone /dev/%s: %w", n, err)
		}
		fds[i] = fd
	}
	return fds, nil
}

// setupDev gives the container a fresh /dev, as systemd-nspawn does:
// a tmpfs holding only the devNodes binds, the /dev/fd and stdio links,
// mount points for /dev/pts and /dev/shm, and /dev/ptmx pointing at
// the container's own devpts. The image's /dev, and any device node in
// it, is hidden underneath. The tmpfs is nodev, unlike nspawn's, which
// mknods into it: with no device cgroup here, a node the container
// creates must not open a host device.
func setupDev(nodes []int) error {
	if err := unix.Mount("tmpfs", "/dev", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_STRICTATIME, "mode=755"); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}
	for i, n := range devNodes {
		p := "/dev/" + n
		if err := os.WriteFile(p, nil, 0o666); err != nil {
			return fmt.Errorf("create %s: %w", p, err)
		}
		if err := unix.MoveMount(nodes[i], "", unix.AT_FDCWD, p, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
			return fmt.Errorf("bind %s: %w", p, err)
		}
		unix.Close(nodes[i])
	}
	for _, d := range []string{"/dev/pts", "/dev/shm"} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	for _, l := range [][2]string{
		{"/proc/self/fd", "/dev/fd"},
		{"/proc/self/fd/0", "/dev/stdin"},
		{"/proc/self/fd/1", "/dev/stdout"},
		{"/proc/self/fd/2", "/dev/stderr"},
		{"pts/ptmx", "/dev/ptmx"},
	} {
		if err := os.Symlink(l[0], l[1]); err != nil {
			return fmt.Errorf("symlink %s: %w", l[1], err)
		}
	}
	return nil
}

// newMount creates a detached mount of a new fstype instance, for the
// caller's namespaces, with the MOUNT_ATTR_* flags attr.
func newMount(fstype string, attr int) (int, error) {
	fsfd, err := unix.Fsopen(fstype, unix.FSOPEN_CLOEXEC)
	if err != nil {
		return -1, err
	}
	defer unix.Close(fsfd)
	if err := unix.FsconfigCreate(fsfd); err != nil {
		return -1, err
	}
	return unix.Fsmount(fsfd, unix.FSMOUNT_CLOEXEC, attr)
}

// attach moves the newMount result fd onto dst. A failure, of either,
// is reported and skipped, as for the other mounts.
func attach(fd int, err error, dst string) bool {
	if err == nil {
		err = unix.MoveMount(fd, "", unix.AT_FDCWD, dst, unix.MOVE_MOUNT_F_EMPTY_PATH)
		unix.Close(fd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-nspawn (child): mount %s: %v\n", dst, err)
		return false
	}
	return true
}

// procReadOnly and procMasked are systemd-nspawn's read-only and
// inaccessible /proc entries (nspawn-mount.c, mount_all): /proc/sys
// holds host-wide tunables such as kernel.core_pattern, which runs a
// program as host root; the others expose hardware tunables or kernel
// internals.
var (
	procReadOnly = []string{"/proc/sys", "/proc/acpi", "/proc/apm", "/proc/asound",
		"/proc/bus", "/proc/fs", "/proc/irq", "/proc/scsi"}
	procMasked = []string{"/proc/kallsyms", "/proc/kcore", "/proc/keys",
		"/proc/sysrq-trigger", "/proc/timer_list"}
)

// protectProc makes procReadOnly read-only and covers procMasked with
// /dev/null. With a private network namespace /proc/sys/net stays
// writable, as under systemd-nspawn: its sysctls are the container's
// own. It is bound over the /proc/sys bind, which the read-only
// remount then leaves alone. An entry this kernel lacks is skipped.
func protectProc(privateNet bool) error {
	for _, p := range procReadOnly {
		err := unix.Mount(p, p, "", unix.MS_BIND, "")
		if err == nil && p == "/proc/sys" && privateNet {
			err = unix.Mount("/proc/sys/net", "/proc/sys/net", "", unix.MS_BIND, "")
		}
		if err == nil {
			err = unix.Mount("", p, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|
				unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV, "")
		}
		if err != nil && err != unix.ENOENT {
			return fmt.Errorf("make %s read-only: %w", p, err)
		}
	}
	for _, p := range procMasked {
		if err := unix.Mount("/dev/null", p, "", unix.MS_BIND, ""); err != nil && err != unix.ENOENT {
			return fmt.Errorf("mask %s: %w", p, err)
		}
	}
	return nil
}

// retainedCaps is systemd-nspawn's default capability set (nspawn.c,
// arg_caps_retain): what a full init needs inside the container,
// CAP_SYS_ADMIN included. Everything else, CAP_SYS_MODULE, SYS_RAWIO,
// SYS_TIME, MAC_ADMIN, NET_ADMIN and the rest, leaves the container.
// As there, CAP_NET_ADMIN stays with a private network namespace, to
// configure it.
var retainedCaps = []uintptr{
	unix.CAP_AUDIT_CONTROL, unix.CAP_AUDIT_WRITE, unix.CAP_CHOWN,
	unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH, unix.CAP_FOWNER,
	unix.CAP_FSETID, unix.CAP_IPC_OWNER, unix.CAP_KILL, unix.CAP_LEASE,
	unix.CAP_LINUX_IMMUTABLE, unix.CAP_MKNOD, unix.CAP_NET_BIND_SERVICE,
	unix.CAP_NET_BROADCAST, unix.CAP_NET_RAW, unix.CAP_SETFCAP,
	unix.CAP_SETGID, unix.CAP_SETPCAP, unix.CAP_SETUID, unix.CAP_SYS_ADMIN,
	unix.CAP_SYS_BOOT, unix.CAP_SYS_CHROOT, unix.CAP_SYS_NICE,
	unix.CAP_SYS_PTRACE, unix.CAP_SYS_RESOURCE, unix.CAP_SYS_TTY_CONFIG,
}

// dropCaps narrows the calling thread's bounding set to retainedCaps,
// as systemd-nspawn's drop_capabilities does, so the init and all it
// runs can never hold the others. The inheritable set is narrowed too:
// an exec as root adds it to the permitted set, bounding set or not,
// and ambient capabilities go with it.
func dropCaps(privateNet bool) error {
	var keep uint64
	for _, c := range retainedCaps {
		keep |= 1 << c
	}
	if privateNet {
		keep |= 1 << unix.CAP_NET_ADMIN
	}
	// Past the kernel's last capability PR_CAPBSET_DROP is EINVAL.
	for c := uintptr(0); c < 64; c++ {
		if keep&(1<<c) != 0 {
			continue
		}
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, c, 0, 0, 0); err != nil && err != unix.EINVAL {
			return fmt.Errorf("drop capability %d: %w", c, err)
		}
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return fmt.Errorf("capget: %w", err)
	}
	data[0].Inheritable &= uint32(keep)
	data[1].Inheritable &= uint32(keep >> 32)
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("capset: %w", err)
	}
	return nil
}

// cloexecFrom sets FD_CLOEXEC on every fd >= first: close_range(2)
// with CLOSE_RANGE_CLOEXEC (Linux 5.11), else one fcntl per entry in
// /proc/self/fd.
func cloexecFrom(first int) error {
	if unix.CloseRange(uint(first), ^uint(0), unix.CLOSE_RANGE_CLOEXEC) == nil {
		return nil
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd < first {
			continue
		}
		// EBADF: the fd ReadDir itself used, already closed.
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && err != unix.EBADF {
			return fmt.Errorf("fd %d: %w", fd, err)
		}
	}
	return nil
}

// diagnoseExecFailure looks at the failed exec target and produces a
// human hint for the two most common causes: file-not-found and
// glibc-vs-musl interpreter mismatch. Returns "" when nothing useful
// can be surfaced (in that case the raw errno wins).
func diagnoseExecFailure(path string, execErr error) string {
	if !errors.Is(execErr, syscall.ENOENT) {
		return ""
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf(" (%s does not exist inside the container — did setup-rootfs.sh copy it?)", path)
	}
	// Binary exists. Read the ELF interpreter path (PT_INTERP)
	// via the standard bytes at the start of the file to detect a
	// glibc-vs-musl mismatch.
	interp := readELFInterpreter(path)
	if interp == "" {
		return ""
	}
	if _, err := os.Stat(interp); errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf(" (interpreter %s missing — %s is dynamically linked; rebuild with CGO_ENABLED=0 for musl containers)", interp, path)
	}
	return ""
}

// readELFInterpreter returns the PT_INTERP segment payload from an
// ELF64 binary, or "" if the file isn't a supported ELF or the
// segment can't be located quickly. Only meant as a diagnostic hint
// — a proper ELF parser lives in debug/elf but pulling that in for
// a one-shot error message is overkill.
func readELFInterpreter(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 64 {
		return ""
	}
	// ELF64 magic + class byte 2 (64-bit)
	if string(data[:4]) != "\x7fELF" || data[4] != 2 {
		return ""
	}
	// Walk .interp section by scanning for the well-known interpreter
	// path prefixes. This is a heuristic — sufficient for the two
	// distros we care about (musl vs glibc) and avoids a full ELF
	// parse for a hint-only string.
	for _, prefix := range []string{"/lib64/ld-linux", "/lib/ld-linux", "/lib/ld-musl"} {
		if idx := indexOfString(data, prefix); idx >= 0 {
			// Read until NUL to get the full interpreter path.
			end := idx
			for end < len(data) && data[end] != 0 {
				end++
			}
			return string(data[idx:end])
		}
	}
	return ""
}

func indexOfString(haystack []byte, needle string) int {
	nb := []byte(needle)
	for i := 0; i+len(nb) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(nb); j++ {
			if haystack[i+j] != nb[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
