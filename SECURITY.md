# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.10.x  | Yes       |
| < 1.10  | No        |

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
  `memory-deny-write-execute`)
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
