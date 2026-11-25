//go:build !linux || !amd64

package seccomp

// Stub syscall table for non-Linux-amd64 builds. The full lookup table
// lives in zsyscalls_linux_amd64.go; on other arches it is empty, so the
// parser refuses every syscall name and the filter compilers refuse
// every rule (see absentSyscalls): the runner fails the start rather
// than run a service without the protection it asked for.
var syscallNumbers = map[string]int{}

// SyscallNumber on non-target arches always reports "unknown" so the
// config parser surfaces the architecture mismatch instead of treating
// it as a configuration bug.
func SyscallNumber(name string) (int, bool) {
	_ = name
	return 0, false
}

func allSyscalls() map[string]int { return syscallNumbers }

// absentSyscalls lists the syscalls a filter rule may name that do not
// exist on this arch, so cannot be called and need no rule. Empty
// without a table: nothing is known to be absent.
var absentSyscalls = map[string]bool{}
