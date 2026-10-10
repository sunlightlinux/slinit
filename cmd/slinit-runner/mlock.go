package main

import (
	"bufio"
	"bytes"
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
	return checkLibcMatch(tf, lf, path, lib)
}

// checkLibcMatch refuses a library built against a different libc from
// the program it is to be preloaded into.
//
// This was the hole the other checks left. The library is built by
// whoever packages slinit, against whatever libc that machine has; the
// service can be a musl binary on the same system, or the whole rootfs
// can be musl while the library was built on a glibc host. ld.so then
// cannot resolve the library's DT_NEEDED, treats the preload as a
// warning, and runs the program anyway — so `mlockall = current+future`
// produced a service that was not locked and said nothing. In the
// functional VM that passed locally and failed in CI, which is what a
// silent preload failure looks like from the outside.
//
// The test is the libc each side names, not a version: musl's loader
// answers DT_NEEDED for `libc.so` and `libc.musl-<arch>.so.1` itself,
// while glibc's is `libc.so.6`. A library naming neither (built
// freestanding) is left alone — it needs no libc and loads under both.
func checkLibcMatch(target, lib *elf.File, targetPath, libPath string) error {
	libLibc, err := libcFlavourOfNeeded(lib)
	if err != nil {
		return fmt.Errorf("%s: %w", libPath, err)
	}
	if libLibc == libcUnknown {
		return nil // no libc dependency to mismatch
	}

	targetLibc := libcFlavourOfInterp(target)
	if targetLibc == libcUnknown {
		// An interpreter we do not recognise. Say so rather than
		// guessing: a wrong guess here either refuses a service that
		// would have worked or lets a silent no-op through, and the
		// second is the failure this function exists to stop.
		return fmt.Errorf("cannot tell which libc %s uses, so cannot tell whether "+
			"%s (%s) will load into it", targetPath, libPath, libLibc)
	}
	if targetLibc != libLibc {
		return fmt.Errorf("%s is %s but %s is built for %s: the loader would skip "+
			"the preload and the service would run unlocked",
			targetPath, targetLibc, libPath, libLibc)
	}
	return nil
}

type libcFlavour string

const (
	libcUnknown libcFlavour = ""
	libcGlibc   libcFlavour = "glibc"
	libcMusl    libcFlavour = "musl"
)

func (l libcFlavour) String() string {
	if l == libcUnknown {
		return "an unrecognised libc"
	}
	return string(l)
}

// libcFlavourOfNeeded reads the library's DT_NEEDED entries.
func libcFlavourOfNeeded(f *elf.File) (libcFlavour, error) {
	needed, err := f.DynString(elf.DT_NEEDED)
	if err != nil {
		return libcUnknown, err
	}
	for _, n := range needed {
		switch {
		case n == "libc.so.6":
			return libcGlibc, nil
		case n == "libc.so" || strings.HasPrefix(n, "libc.musl-"):
			return libcMusl, nil
		}
	}
	return libcUnknown, nil
}

// libcFlavourOfInterp reads the program interpreter, which names the
// libc that will do the loading.
func libcFlavourOfInterp(f *elf.File) libcFlavour {
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		buf := make([]byte, p.Filesz)
		if _, err := p.ReadAt(buf, 0); err != nil {
			return libcUnknown
		}
		interp := string(bytes.TrimRight(buf, "\x00"))
		switch {
		case strings.Contains(interp, "ld-musl"):
			return libcMusl
		case strings.Contains(interp, "ld-linux") || strings.Contains(interp, "ld.so"):
			return libcGlibc
		}
		return libcUnknown
	}
	return libcUnknown
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
