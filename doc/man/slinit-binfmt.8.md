% SLINIT-BINFMT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

# NAME

slinit-binfmt - register custom binary formats with the kernel
(systemd-binfmt clone)

# SYNOPSIS

**slinit-binfmt** [*OPTIONS*] [*FILE*...]

# DESCRIPTION

**slinit-binfmt** is a drop-in replacement for **systemd-binfmt**(1).
It reads **binfmt.d**(5) configuration files, one spec per line, and
writes each spec to **/proc/sys/fs/binfmt_misc/register** so the
kernel routes matching binaries into the interpreter named in the
spec. This is how QEMU user-mode emulation runs foreign-arch
binaries transparently, how Mono handles .NET **.exe** files, and
how WSL binds Linux binaries to a Windows-side loader.

Without positional arguments the tool scans, in order:

- **/usr/lib/binfmt.d**
- **/usr/local/lib/binfmt.d**
- **/run/binfmt.d**
- **/etc/binfmt.d**

Files whose basenames collide are resolved by last-directory-wins, so
an operator override at **/etc/binfmt.d/foo.conf** always beats the
distro-shipped **/usr/lib/binfmt.d/foo.conf**. Only files ending in
**.conf** are considered.

# CONFIG FORMAT

**binfmt.d**(5) files contain one spec per line. Blank lines and
lines whose first non-whitespace character is **#** or **;** are
ignored.

A spec has the shape:

    :NAME:TYPE:OFFSET:MAGIC:MASK:INTERPRETER:FLAGS

where the leading character (**:** by convention, but any single
non-alnum byte works) becomes the field delimiter for the rest of
the line. TYPE is **M** (magic bytes) or **E** (filename extension).
See the kernel documentation for **binfmt_misc** for the full
grammar and flag list.

# ACTIONS

Without options, every spec from every discovered file is written
to the register entry point. If a format with the same name is
already registered, it is unregistered first (write **-1** to
**/proc/sys/fs/binfmt_misc/**NAME) so the new definition wins.

**-u**, **--unregister**
:   Tear down every currently-registered format. Walks
    **/proc/sys/fs/binfmt_misc/** and writes **-1** to each entry
    that isn't **register** or **status**.

*FILE*...
:   Apply only the named files instead of scanning the standard
    directories.

# OPTIONS

**-v**, **--verbose**
:   Emit a one-line summary (**registered=N unregistered=M skipped=K
    errors=E**) to stderr after the pass.

**--root** *DIR*
:   Prefix *DIR* onto every hardcoded path: the binfmt.d/ scan roots
    **and** **/proc/sys/fs/binfmt_misc/**. Useful for previewing a
    config in a chroot or a fixture tree; never needed in production.

**-h**, **--help**
:   Print usage.

**-V**, **--version**
:   Print version string.

# EXIT STATUS

- **0**: every discovered spec applied cleanly.
- **1**: at least one spec failed (parse error, kernel refused it, etc.).
- **2**: bad usage / unknown flag.
- **3**: **binfmt_misc** kernel filesystem is not mounted (module not
  loaded); nothing to do.

# EXAMPLES

Apply every /etc/binfmt.d/*.conf on boot:

```
slinit-binfmt
```

Preview a single file without touching the system:

```
slinit-binfmt --root=/tmp/preview my-format.conf
```

Tear down every registered format (e.g. during container tear-down):

```
slinit-binfmt --unregister
```

# SEE ALSO

**binfmt.d**(5), **binfmt_misc**(8) (kernel documentation),
**systemd-binfmt**(1), **slinit**(8).
