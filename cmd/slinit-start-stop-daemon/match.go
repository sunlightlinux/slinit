package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// MatchCriteria narrows the /proc scan when locating running processes.
// Empty fields are wildcards; a pidfile short-circuits to a single PID.
type MatchCriteria struct {
	Exec        string // full path to executable (matched against /proc/PID/exe symlink)
	Name        string // /proc/PID/comm (kernel 15-char task name)
	UID         int    // -1 = don't match
	PidFile     string
	Interpreted bool // --interpreted: match cmdline[1] when exe is an interpreter

	// execInfo is stat(Exec), taken once per scan by prepare(). Nil
	// when Exec is empty or does not exist (an upgrade removed it):
	// matching then falls back to comparing path strings.
	execInfo os.FileInfo
	prepared bool
}

// prepare stats Exec once so a /proc scan does not re-stat it for
// every process.
func (m MatchCriteria) prepare() MatchCriteria {
	if m.prepared {
		return m
	}
	m.prepared = true
	if m.Exec != "" {
		if fi, err := os.Stat(m.Exec); err == nil {
			m.execInfo = fi
		}
	}
	return m
}

// FindMatchingPIDs walks /proc looking for processes that satisfy every
// non-empty criterion. When PidFile is set and readable, the search is
// confined to that PID; a stale pidfile (PID missing or fails criteria)
// yields nil, nil (caller decides between "not running" and "stale").
// A pidfile that anyone but root (or us) could have written is refused
// with an error, as Debian's start-stop-daemon refuses it.
//
// Init, this process and its parent never match: a pidfile naming
// PID 1 would otherwise have it signalled (SIGTERM is a reboot under
// slinit), and a --name scan could catch the script calling us.
func FindMatchingPIDs(m MatchCriteria) ([]int, error) {
	m = m.prepare()
	self, parent := os.Getpid(), os.Getppid()
	if m.PidFile != "" {
		pid, err := readTrustedPIDFile(m.PidFile, m.Exec == "" && m.Name == "" && m.UID < 0)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if pid <= 1 || pid == self || pid == parent {
			return nil, nil
		}
		if !processMatches(pid, m) {
			return nil, nil
		}
		return []int{pid}, nil
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pid == 1 || pid == self || pid == parent {
			continue
		}
		if processMatches(pid, m) {
			out = append(out, pid)
		}
	}
	return out, nil
}

func processMatches(pid int, m MatchCriteria) bool {
	// Existence check: /proc/PID must be there. Stale pidfiles and dead
	// scanned pids drop out here before any per-criterion match.
	info, err := os.Stat("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return false
	}
	m = m.prepare()
	exeLink := "/proc/" + strconv.Itoa(pid) + "/exe"
	exePath, _ := os.Readlink(exeLink)
	// A daemon whose binary an upgrade replaced reads "<path> (deleted)";
	// it is still the daemon to stop, as OpenRC and Debian match it.
	exePath = strings.TrimSuffix(exePath, " (deleted)")
	if m.Exec != "" {
		if m.Interpreted && isInterpreter(exePath) {
			// Interpreter dispatch: match script path from argv[1] when
			// the exe is a shell/python/perl/etc. wrapper.
			if !sameExecutable(m, readInterpretedTarget(pid), "") {
				return false
			}
		} else if !sameExecutable(m, exePath, exeLink) {
			return false
		}
	}
	if m.Name != "" {
		nameSrc := commOf(pid)
		want := commName(m.Name)
		if m.Interpreted && isInterpreter(exePath) {
			// Script's argv[1] basename is what an operator thinks of as
			// the "process name" for an interpreted daemon.
			if scriptPath := readInterpretedTarget(pid); scriptPath != "" {
				nameSrc = filepath.Base(scriptPath)
				want = m.Name // not a comm: never truncated
			}
		}
		if nameSrc != want {
			return false
		}
	}
	if m.UID >= 0 {
		if uid, ok := statUID(info); !ok || uid != uint32(m.UID) {
			return false
		}
	}
	return true
}

// sameExecutable reports whether got (a path the kernel or argv names
// for the process) is the file --exec names. Like Debian's
// start-stop-daemon it compares device and inode, not spelling: the
// kernel reports the path it resolved, so on a merged-/usr system
// (/usr/sbin -> bin) a daemon started as /usr/sbin/foo reads back as
// /usr/bin/foo and a string compare never matched it -- --start
// launched duplicates and --stop reported "no matching process".
//
// statPath is what to stat for the process side (/proc/PID/exe, which
// follows to the running image even when its name was unlinked);
// empty means stat got itself. The string compare stays as the
// fallback for a binary an upgrade replaced: the running image is the
// old, deleted inode, which no stat of --exec can reach.
func sameExecutable(m MatchCriteria, got, statPath string) bool {
	if got == "" && statPath == "" {
		return false
	}
	if got == m.Exec {
		return true
	}
	if m.execInfo == nil {
		return false
	}
	if statPath == "" {
		if !filepath.IsAbs(got) {
			return false
		}
		statPath = got
	}
	fi, err := os.Stat(statPath)
	if err != nil {
		return false
	}
	return os.SameFile(m.execInfo, fi)
}

// commLen is the longest task name the kernel keeps (TASK_COMM_LEN
// 16, NUL included); /proc/PID/comm holds the first 15 bytes.
const commLen = 15

// commName is what /proc/PID/comm reads for a process named name. A
// --name longer than that never equalled any comm, so --start
// launched duplicates and --stop found nothing; it now matches on the
// first 15 bytes, as the kernel stored them.
func commName(name string) string {
	if len(name) > commLen {
		return name[:commLen]
	}
	return name
}

// nameLengthWarning is the warning for a --name the kernel truncates;
// "" when it fits. dpkg's start-stop-daemon warns the same way (but
// still compares the whole name, so it never matches).
func nameLengthWarning(name string) string {
	if len(name) <= commLen {
		return ""
	}
	return fmt.Sprintf("warning: --name %q is longer than the %d characters the kernel keeps "+
		"as a process name; matching on %q (use --exec to match exactly)",
		name, commLen, commName(name))
}

func commOf(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(data), "\n")
}

// readInterpretedTarget returns argv[1] from /proc/PID/cmdline — the
// script path when the process was exec'd as `interpreter script`.
// Empty when the process has no second argument.
func readInterpretedTarget(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// isInterpreter recognises the common shebang candidates. Not exhaustive
// — extend as init.d scripts elsewhere surface new interpreters.
func isInterpreter(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "sh", "bash", "dash", "ksh", "zsh", "ash",
		"python", "python2", "python3",
		"perl", "ruby", "lua", "tclsh", "node":
		return true
	}
	return false
}

// readTrustedPIDFile is readPIDFile for a pidfile whose pid we are
// about to signal, checked as Debian's start-stop-daemon checks it
// (dpkg 1.19.3) on the open file. pidfileOnly says nothing else will
// cross-check the process the file names.
func readTrustedPIDFile(path string, pidfileOnly bool) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	if why := process.PIDFileInsecure(&st, os.Getuid(), os.Getgid(), pidfileOnly); why != "" {
		return 0, fmt.Errorf("%s pidfile %s is insecure", why, path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, err
	}
	return parsePID(data), nil
}

// readPIDFile parses the first integer from a pidfile. Returns 0 with
// nil error when the file exists but is empty or malformed — caller
// treats that as "not running" (dinit/OpenRC behaviour).
func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return parsePID(data), nil
}

func parsePID(data []byte) int {
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	if line == "" {
		return 0
	}
	pid, err := strconv.Atoi(line)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// writePIDFile writes pid to path, refusing to follow a symlink.
//
// O_NOFOLLOW because the path comes from --pidfile and a pid-file's
// directory is routinely writable by the daemon's own user — that is
// how an unprivileged daemon rewrites its own pid-file. slinit says so
// itself where it *reads* these files: pkg/process/pidfile.go opens
// them O_NOFOLLOW with the note that "services often write their
// pid-file into world-writable locations". Writing them as root
// without the same flag made this a truncate-to-a-pid-line of whatever
// the name resolved to, which pkg/service/bgprocess.go calls "an
// arbitrary write primitive" where it opens service logs.
//
// A pre-existing symlink now fails with ELOOP rather than being
// followed; a stale regular pid-file is still replaced, which is what
// O_TRUNC is for.
func writePIDFile(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(strconv.Itoa(pid) + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
