# Security Policy

## Supported Versions

| Version | Supported |
|---|---|
| 4.0.x (LTS) | Yes — bug and security fixes until six months after the next LTS line is released |
| 4.1 and later | Yes — the newest minor, until the one after it |
| 3.x | No — the development line closed at 3.22.x. Upgrade to 4.0, which changes nothing documented in 3.22 |
| < 3.0.0 | No — 2.x and 1.x are closed; upgrade to 4.0 |

[STABILITY.md](STABILITY.md#long-term-support) says what an LTS line
takes: fixes only, never a new feature.

This table says which versions receive fixes. What a fix is allowed to
change — and what stays put between releases — is a separate question,
answered in [STABILITY.md](STABILITY.md). Security fixes are one of the
two exceptions it allows to break a stable interface in any release,
patch releases included: if keeping the old behaviour keeps the
vulnerability, the behaviour changes, and the CHANGELOG says so under
`Security` with a `Compat` note.

## Reporting a Vulnerability

If you discover a security vulnerability in slinit, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please send an email to: ionut_n2001@yahoo.com

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

You should receive a response within 48 hours. We will work with you to understand the issue and coordinate a fix before any public disclosure.

## Scope

slinit runs as PID 1 (init system) with root privileges. Security-relevant areas include:

- Control socket authentication and authorization (`SO_PEERCRED`
  peer-uid check on every accepted connection)
- Process execution (fork/exec with capabilities, securebits,
  ambient caps, dynamic-user transient UID allocation)
- Service configuration parsing (path traversal, injection,
  D-Bus well-known name grammar, absolute-path enforcement)
- LSM domain transitions (AppArmor, SELinux, SMACK — all
  fail-closed on missing LSM per **slinit-runner**(8))
- Seccomp filter compilation and installation
  (`system-call-filter`, `restrict-*` hardening cluster,
  `memory-deny-write-execute`, and `io-uring-filter`, which filters
  io_uring requests like the system calls they stand for on Linux
  7.0+)
- D-Bus services that change system settings: `slinit-timedated`,
  `slinit-hostnamed` and `slinit-localed` authorize every change
  through polkit with systemd's action ids, and `slinit-logind`
  checks each caller against the rules systemd-logind applies when
  built without polkit
- Signal handling and shutdown sequence
- Credentials pipeline (tmpfs-backed `$CREDENTIALS_DIRECTORY`
  with mode 0400 files, ro-remount before hand-off)

### Fail-closed contracts

The following are load-bearing invariants tested to fail
CLOSED — i.e. the service refuses to start rather than run
with weaker protection than the operator declared:

- **apparmor-switch** / **selinux-context** / **smack-process-label**
  require the LSM's sysfs indicator
  (`/sys/kernel/security/apparmor`, `/sys/fs/selinux`,
  `/sys/fs/smackfs`) to be present.
- **PR_SET_NO_NEW_PRIVS** is auto-implied whenever any
  seccomp filter, hardening knob, or bounding-cap set is
  configured.
- **memory-deny-write-execute** and **memory-ksm** fail-close
  on kernels older than 6.3 / 6.4 respectively.
- `slinit-timedated`, `slinit-hostnamed` and `slinit-localed`
  allow only root when polkitd is not on the bus, and refuse when
  polkit cannot be asked: no answer is never read as yes.
