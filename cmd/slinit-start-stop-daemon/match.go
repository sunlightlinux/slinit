package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// MatchCriteria narrows the /proc scan when locating running processes.
// Empty fields are wildcards; a pidfile short-circuits to a single PID.
type MatchCriteria struct {
	Exec        string // full path to executable (matched against /proc/PID/exe symlink)
	Name        string // /proc/PID/comm (kernel 15-char task name)
	UID         int    // -1 = don't match
	PidFile     string
	Interpreted bool // --interpreted: match cmdline[1] when exe is an interpreter
}

// FindMatchingPIDs walks /proc looking for processes that satisfy every
// non-empty criterion. When PidFile is set and readable, the search is
// confined to that PID; a stale pidfile (PID missing or fails criteria)
// yields nil, nil (caller decides between "not running" and "stale").
func FindMatchingPIDs(m MatchCriteria) ([]int, error) {
	if m.PidFile != "" {
		pid, err := readPIDFile(m.PidFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if pid <= 0 {
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
	self := os.Getpid()
	var out []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pid == self {
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
	exePath, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if m.Exec != "" {
		want := m.Exec
		got := exePath
		if m.Interpreted && isInterpreter(got) {
			// Interpreter dispatch: match script path from argv[1] when
			// the exe is a shell/python/perl/etc. wrapper.
			got = readInterpretedTarget(pid)
		}
		if got != want {
			return false
		}
	}
	if m.Name != "" {
		nameSrc := commOf(pid)
		if m.Interpreted && isInterpreter(exePath) {
			// Script's argv[1] basename is what an operator thinks of as
			// the "process name" for an interpreted daemon.
			if scriptPath := readInterpretedTarget(pid); scriptPath != "" {
				nameSrc = filepath.Base(scriptPath)
			}
		}
		if nameSrc != m.Name {
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

// readPIDFile parses the first integer from a pidfile. Returns 0 with
// nil error when the file exists but is empty or malformed — caller
// treats that as "not running" (dinit/OpenRC behaviour).
func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	if line == "" {
		return 0, nil
	}
	pid, err := strconv.Atoi(line)
	if err != nil || pid <= 0 {
		return 0, nil
	}
	return pid, nil
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
