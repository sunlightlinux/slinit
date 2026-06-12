//go:build !linux || !amd64

package seccomp

// Stub syscall table for non-Linux-amd64 builds. The full lookup table
// lives in syscalls_linux_amd64.go; on other arches the parser still
// needs SyscallNumber to compile but the runner refuses to install a
// filter (Install returns ErrUnsupportedArch). Keeping the table empty
// makes "unknown syscall" the parse-time error, which is the safer
// failure mode — better to refuse the config than to silently drop it.
var syscallNumbers = map[string]int{}

// SyscallNumber on non-target arches always reports "unknown" so the
// config parser surfaces the architecture mismatch instead of treating
// it as a configuration bug.
func SyscallNumber(name string) (int, bool) {
	_ = name
	return 0, false
}

func allSyscalls() map[string]int { return syscallNumbers }
