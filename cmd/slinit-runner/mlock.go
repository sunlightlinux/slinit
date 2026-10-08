package main

import (
	"bufio"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// mlockall for the service itself.
//
// Memory locks are dropped at execve(2), and no syscall locks another
// process's memory, so the mlockall the runner makes on itself never
// reaches the service. The lock has to be taken inside the service: the
// runner preloads libslinit-mlock.so, whose constructor calls mlockall(2)
// with the flags from SLINIT_MLOCKALL before the program's main().
//
// ld.so treats a preload it cannot use as a warning and runs the program
// anyway, which would leave the service silently unlocked. So everything
// that would make the preload a no-op is checked here and refused: no
// library, a library others could replace, a static binary (no dynamic
// loader to honour LD_PRELOAD), a different ELF class or machine than
// the library, and a setuid/setgid or file-capability binary (secure
// execution ignores LD_PRELOAD paths).

const mlockLibName = "libslinit-mlock.so"

// mlockLibCandidates lists where the library is looked for: beside the
// runner (a build tree), then <prefix>/lib/slinit for the runner's own
// prefix, then the usual system prefixes.
func mlockLibCandidates() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		out = append(out,
			filepath.Join(dir, mlockLibName),
			filepath.Join(dir, "..", "lib", "slinit", mlockLibName))
	}
	return append(out,
		"/usr/lib/slinit/"+mlockLibName,
		"/usr/local/lib/slinit/"+mlockLibName,
		"/lib/slinit/"+mlockLibName)
}

// findMlockLib returns the first usable library among the candidates.
func findMlockLib(candidates []string) (string, error) {
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := checkMlockLib(p); err != nil {
			return "", err
		}
		return filepath.Clean(p), nil
	}
	return "", fmt.Errorf("%s not found (looked in %s)", mlockLibName,
		strings.Join(candidates, ", "))
}

// checkMlockLib refuses a library that someone other than root could
// have replaced: it is loaded into the service, which may run as root.
func checkMlockLib(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: writable by group or others", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 && st.Uid != 0 {
		return fmt.Errorf("%s: not owned by root", path)
	}
	return nil
}

// checkPreloadTarget verifies that ld.so will honour LD_PRELOAD for
// target: a dynamically linked ELF of the library's class and machine,
// without setuid/setgid bits or file capabilities. A "#!" script is
// judged by its interpreter, which is the program actually loaded.
func checkPreloadTarget(target, lib string) error {
	path, err := resolveInterpreter(target)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("%s is setuid/setgid: the loader ignores LD_PRELOAD for it", path)
	}
	if _, err := unix.Getxattr(path, "security.capability", nil); err == nil {
		return fmt.Errorf("%s has file capabilities: the loader ignores LD_PRELOAD for it", path)
	}

	tf, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("%s: not an ELF executable: %w", path, err)
	}
	defer tf.Close()
	dynamic := false
	for _, p := range tf.Progs {
		if p.Type == elf.PT_INTERP {
			dynamic = true
			break
		}
	}
	if !dynamic {
		return fmt.Errorf("%s is statically linked: there is no loader to preload into it", path)
	}

	lf, err := elf.Open(lib)
	if err != nil {
		return fmt.Errorf("%s: %w", lib, err)
	}
	defer lf.Close()
	if tf.Class != lf.Class || tf.Machine != lf.Machine {
		return fmt.Errorf("%s is %v/%v but %s is %v/%v", path, tf.Class, tf.Machine,
			lib, lf.Class, lf.Machine)
	}
	return nil
}

// resolveInterpreter follows a "#!" line one level: the kernel does not
// chain interpreters, and the one named there is what gets loaded.
func resolveInterpreter(target string) (string, error) {
	f, err := os.Open(target)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	if !strings.HasPrefix(line, "#!") {
		return target, nil
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 {
		return "", fmt.Errorf("%s: empty #! line", target)
	}
	return fields[0], nil
}

// setPreloadEnv puts the library first in LD_PRELOAD and hands it the
// flags and this PID — the service's, once we exec. The library locks
// only in that process (so an exec chain like `sh -c 'exec daemon'`
// stays locked) and clears all three in any child the service forks.
func setPreloadEnv(lib string, flags int) error {
	preload := lib
	if cur := os.Getenv("LD_PRELOAD"); cur != "" {
		preload += ":" + cur
	}
	if err := os.Setenv("LD_PRELOAD", preload); err != nil {
		return err
	}
	if err := os.Setenv("SLINIT_MLOCKALL_PID", strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	return os.Setenv("SLINIT_MLOCKALL", strconv.Itoa(flags))
}
