# NAME

slinit-check - offline configuration linter for slinit service files

# SYNOPSIS

**slinit-check** [*options*] [*service-name*...]

# DESCRIPTION

**slinit-check** loads and validates slinit service descriptions without
requiring a running **slinit**(8) instance. It is the recommended way to
check service files before deploying them — either on a build host, in a
CI pipeline, or on a live system before a reload.

If no *service-name* is given, **boot** is checked by default.

The linter performs four passes over the loaded service graph:

1. **Parse** every service description reachable from the named roots.
2. **Detect dependency cycles** via DFS with explicit-stack pruning. The
   first cycle found is reported with the full path; the run then aborts
   with exit status 1.
3. **Check dependency depth** against **MaxDepDepth**. Services that
   exceed the limit are flagged as errors.
4. **Secondary checks** on every loaded service — see *CHECKS PERFORMED*
   below.

# OPTIONS

**-d**, **--services-dir** *DIR*
:   Add *DIR* to the list of service-description directories to search.
    May be repeated. If no **-d** is given (and **-s**/**-u** are also
    absent), the default system directories are used.

**-s**, **--system**
:   Use the default system service directories: */etc/slinit.d*,
    */usr/lib/slinit.d*, */lib/slinit.d*. This is the default when no
    explicit **-d** is given.

**-u**, **--user**
:   Use the default user service directories — *$XDG_CONFIG_HOME/slinit.d*
    (or *~/.config/slinit.d* if XDG_CONFIG_HOME is unset), then the
    system-wide user-mode dirs under */etc/slinit.d/user*,
    */usr/lib/slinit.d/user*, */usr/local/lib/slinit.d/user*.

**-n**, **--online**
:   Online mode. Connect to the running daemon, retrieve its current
    service-directory list and global environment, and use those for
    the lint pass. Useful for catching drift between a running system
    and freshly-edited service files.

**-p**, **--socket-path** *PATH*
:   Override the control-socket path used by **--online**. Defaults to
    */run/slinit.socket* when running as root, or
    *$XDG_RUNTIME_DIR/slinitctl* (falling back to *~/.slinitctl*)
    otherwise.

**-e**, **--env-file** *FILE*
:   Load environment variables from *FILE* (KEY=VALUE per line, blank
    lines and lines starting with **#** are ignored) before evaluating
    services. Variables loaded this way are visible to substitution
    inside service-description bodies.

**-h**, **--help**
:   Print a usage summary and exit.

# CHECKS PERFORMED

For every service that is successfully loaded, **slinit-check** verifies
that filesystem paths referenced by the description are usable:

- **command** and **stop-command** point to absolute paths that exist
  and are executable.
- **working-dir**, **chroot**, and **env-dir** point to existing
  directories.
- **env-file** exists.
- The directories holding **pid-file**, **logfile**, **lock-file**, and
  **socket-listen** exist.

Namespace settings are checked for self-consistency:

- **namespace-uid-map** / **namespace-gid-map** without
  **namespace-user** is an error.
- **namespace-user** without explicit UID/GID maps emits a warning
  (the daemon would fall back to a default 1:1 mapping).
- **namespace-pid** without **namespace-mount** emits a warning
  (the new PID namespace cannot have its own */proc*).
- Overlapping container-ID ranges in UID/GID maps are errors.
- Namespace flags on **internal** or **triggered** services emit a
  warning — there is no process for them to apply to.

Non-fatal findings are emitted as **WARNING**; fatal findings are
emitted as **ERROR**.

# EXIT STATUS

**0**
:   No errors and no warnings.

**1**
:   At least one error was reported. Warnings alone do not cause a
    nonzero exit; this lets CI pipelines distinguish "broken" from
    "questionable but tolerated".

**2**
:   A usage error or unrecoverable startup failure (bad option, missing
    required argument, daemon unreachable in **--online** mode).

# EXAMPLES

Lint the default boot graph using the system service directories:

```
slinit-check
```

Lint a specific service from a custom directory:

```
slinit-check -d /etc/slinit.d nginx
```

Lint multiple roots with the running daemon's view of the world:

```
slinit-check --online boot recovery
```

Pre-flight a packaged service against a staging env-file in CI:

```
slinit-check -d ./services -e ./ci.env nginx postgres
```

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5),
**slinit-monitor**(8)

# AUTHORS

Ionut Nechita and contributors. slinit is licensed under Apache 2.0.
