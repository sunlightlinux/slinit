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

- Control socket authentication and authorization
- Process execution (fork/exec with capabilities, securebits)
- Service configuration parsing (path traversal, injection)
- Signal handling
- Shutdown sequence
