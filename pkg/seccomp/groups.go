package seccomp

// Predefined syscall groups. Names mirror systemd's
// SystemCallFilter=@group identifiers so existing unit files port
// verbatim. Membership is the union of "what systemd documents" and
// "what's actually present in syscallNumbers" — an entry referenced by
// a group but missing from the table would be a silent gap, so the
// init test in groups_test.go verifies every group entry resolves.
var syscallGroups = map[string][]string{
	// @system-service — the broad default safe allowlist. Covers
	// roughly what an ordinary userspace daemon needs (I/O, memory,
	// signals, sockets, scheduling, time). Privileged operations are
	// explicitly NOT in here; combine with @privileged for those.
	"@system-service": {
		"read", "write", "open", "close", "openat", "openat2", "creat",
		"lseek", "pread64", "pwrite64", "readv", "writev",
		"preadv", "pwritev", "preadv2", "pwritev2",
		"dup", "dup2", "dup3", "pipe", "pipe2",
		"sendfile", "splice", "tee", "vmsplice", "copy_file_range",
		"ioctl", "fcntl", "fadvise64", "fallocate",
		"stat", "fstat", "lstat", "newfstatat", "statx",
		"statfs", "fstatfs",
		"access", "faccessat", "faccessat2",
		"getdents", "getdents64",
		"truncate", "ftruncate",
		"getcwd", "chdir", "fchdir",
		"rename", "renameat", "renameat2",
		"mkdir", "mkdirat", "rmdir",
		"unlink", "unlinkat",
		"link", "linkat", "symlink", "symlinkat", "readlink", "readlinkat",
		"chmod", "fchmod", "fchmodat",
		"chown", "fchown", "lchown", "fchownat",
		"umask",
		"sync", "syncfs", "fsync", "fdatasync", "sync_file_range",
		"brk", "mmap", "munmap", "mremap", "mprotect", "madvise",
		"mlock", "mlock2", "munlock", "mlockall", "munlockall",
		"mincore", "msync", "memfd_create",
		"fork", "vfork", "clone", "clone3", "execve", "execveat",
		"exit", "exit_group", "wait4", "waitid",
		"getpid", "getppid", "gettid",
		"getpgid", "getpgrp", "setpgid", "setsid", "getsid",
		"pidfd_open", "pidfd_send_signal", "pidfd_getfd",
		"kill", "tgkill", "tkill",
		"rt_sigaction", "rt_sigprocmask", "rt_sigreturn",
		"rt_sigpending", "rt_sigtimedwait", "rt_sigsuspend",
		"rt_sigqueueinfo", "rt_tgsigqueueinfo",
		"sigaltstack", "signalfd", "signalfd4", "pause",
		"getuid", "geteuid", "getgid", "getegid", "getgroups",
		"getresuid", "getresgid",
		"clock_gettime", "clock_getres", "clock_nanosleep",
		"gettimeofday", "time", "nanosleep",
		"alarm", "setitimer", "getitimer",
		"timer_create", "timer_settime", "timer_gettime",
		"timer_delete", "timer_getoverrun",
		"timerfd_create", "timerfd_settime", "timerfd_gettime",
		"poll", "ppoll", "select", "pselect6",
		"epoll_create", "epoll_create1", "epoll_ctl",
		"epoll_wait", "epoll_pwait", "epoll_pwait2",
		"eventfd", "eventfd2",
		"inotify_init", "inotify_init1",
		"inotify_add_watch", "inotify_rm_watch",
		"socket", "socketpair", "bind", "listen",
		"accept", "accept4", "connect",
		"getsockname", "getpeername",
		"sendto", "sendmsg", "sendmmsg",
		"recvfrom", "recvmsg", "recvmmsg",
		"shutdown", "setsockopt", "getsockopt",
		"sched_yield", "sched_getparam", "sched_getscheduler",
		"sched_get_priority_max", "sched_get_priority_min",
		"sched_rr_get_interval",
		"sched_getaffinity", "sched_getattr",
		"getrandom", "uname", "sysinfo",
		"prctl", "arch_prctl", "futex",
		"set_tid_address", "set_robust_list", "get_robust_list",
		"getrusage", "prlimit64", "getrlimit",
		"getpriority", "ioprio_get",
		"membarrier", "rseq", "close_range",
	},

	// @privileged — operations a service generally has no business
	// doing. Pair with @system-service for root daemons that genuinely
	// need a subset (e.g. networkd, mount helpers).
	"@privileged": {
		"mount", "umount2", "pivot_root", "chroot", "setns", "unshare",
		"setuid", "setgid", "setreuid", "setregid",
		"setresuid", "setresgid", "setfsuid", "setfsgid", "setgroups",
		"capget", "capset",
		"reboot", "kexec_load", "kexec_file_load",
		"sethostname", "setdomainname",
		"iopl", "ioperm",
		"swapon", "swapoff",
		"syslog", "acct",
		"ptrace", "process_vm_readv", "process_vm_writev",
		"perf_event_open", "bpf", "userfaultfd", "kcmp",
		"init_module", "finit_module", "delete_module",
		"mknod", "mknodat",
		"clock_settime", "clock_adjtime", "settimeofday", "adjtimex",
		"quotactl",
		"setrlimit", "setpriority", "ioprio_set",
		"sched_setparam", "sched_setscheduler",
		"sched_setaffinity", "sched_setattr",
	},

	"@network-io": {
		"socket", "socketpair", "bind", "listen",
		"accept", "accept4", "connect",
		"getsockname", "getpeername",
		"sendto", "sendmsg", "sendmmsg",
		"recvfrom", "recvmsg", "recvmmsg",
		"shutdown", "setsockopt", "getsockopt",
	},

	"@file-system": {
		"open", "openat", "openat2", "close", "creat",
		"read", "write", "pread64", "pwrite64",
		"readv", "writev", "preadv", "pwritev", "preadv2", "pwritev2",
		"stat", "fstat", "lstat", "newfstatat", "statx",
		"statfs", "fstatfs",
		"access", "faccessat", "faccessat2",
		"chdir", "fchdir", "getcwd",
		"chmod", "fchmod", "fchmodat",
		"chown", "fchown", "lchown", "fchownat",
		"mkdir", "mkdirat", "rmdir",
		"unlink", "unlinkat",
		"rename", "renameat", "renameat2",
		"link", "linkat", "symlink", "symlinkat",
		"readlink", "readlinkat",
		"truncate", "ftruncate", "fallocate",
		"sync", "fsync", "fdatasync", "syncfs",
		"getdents", "getdents64",
	},

	"@process": {
		"fork", "vfork", "clone", "clone3",
		"execve", "execveat",
		"exit", "exit_group",
		"wait4", "waitid",
		"kill", "tgkill", "tkill",
		"pidfd_open", "pidfd_send_signal", "pidfd_getfd",
		"getpid", "getppid", "gettid",
	},

	"@clock": {
		"clock_settime", "clock_adjtime", "settimeofday", "adjtimex",
	},

	"@debug": {
		"ptrace", "process_vm_readv", "process_vm_writev",
		"perf_event_open", "kcmp",
	},

	"@ipc": {
		"shmget", "shmat", "shmdt", "shmctl",
		"semget", "semop", "semctl", "semtimedop",
		"msgget", "msgsnd", "msgrcv", "msgctl",
		"mq_open", "mq_unlink",
		"mq_timedsend", "mq_timedreceive",
		"mq_notify", "mq_getsetattr",
	},

	"@mount": {
		"mount", "umount2", "pivot_root",
	},

	"@raw-io": {
		"iopl", "ioperm",
	},

	"@reboot": {
		"reboot", "kexec_load", "kexec_file_load",
	},

	"@swap": {
		"swapon", "swapoff",
	},
}

// ExpandGroup returns the syscall name list for a single @group token.
// ok is false when the name is unknown so the parser can surface a
// typo'd group at parse time. Names are NOT validated here; the parser
// validates after expansion so an out-of-group syscall (added by name)
// is caught the same way.
func ExpandGroup(name string) (syscalls []string, ok bool) {
	s, ok := syscallGroups[name]
	return s, ok
}

// Groups returns the names of all predefined groups. Useful for help
// output and tests.
func Groups() []string {
	out := make([]string, 0, len(syscallGroups))
	for name := range syscallGroups {
		out = append(out, name)
	}
	return out
}
