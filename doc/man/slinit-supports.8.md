% SLINIT-SUPPORTS(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-supports - query whether slinit supports a given directive, opcode, or option

# SYNOPSIS

**slinit-supports** [*flags*] *NAME*

**slinit-supports** [*flags*] **--list-directives** | **--list-opcodes** | **--list-options** | **--list-all**

# DESCRIPTION

**slinit-supports** answers two questions about the slinit build it
belongs to: "does this feature exist?" and "which upstream did it come
from?". The answers are auto-discovered from source at compile time
— the tool cannot go stale versus the binary that ships alongside
it. Provenance annotations (dinit / systemd / runit /
s6-linux-init / OpenRC / upstart / slinit) are hand-curated in a
lookup table also compiled in.

Three feature spaces are covered:

- **Directives** — every key the service-file parser accepts. Names
  like *depends-on*, *cgroup-cpu-max*, *file-descriptor-store-preserve*.
- **Opcodes** — every command the control-socket protocol accepts.
  Names like *CmdStartService*, *CmdReloadAll*, *CmdSetTrigger*.
- **Options** — every value valid under an *options:* directive
  line. They are named with an **option:** prefix, e.g.
  *option:starts-on-console*, *option:pass-cs-fd*; the prefix is
  needed for a look-up as well.

Look-up mode (positional *NAME*, no **--list-\***) prints "Yes", the
name, its kind, and its source, category and notes, and exits 0 if the
name is recognised; otherwise it prints a "No" line and exits 1.
Enumeration mode (**--list-\*** flag) prints the whole surface,
optionally regrouped or reformatted; a positional *NAME* given
alongside it is ignored.

# POSITIONAL

*NAME*
:   A single directive / opcode / option name to look up. Exact-match
    only. Omit if using **--list-\***.

# FLAGS

**--list-directives**
:   Enumerate every directive slinit's parser accepts.

**--list-opcodes**
:   Enumerate every control-protocol opcode.

**--list-options**
:   Enumerate every value valid under an *options:* directive line.

**--list-all**
:   Enumerate directives + opcodes + options together.

**--group-by=**\ *SPEC*
:   Group **--list-\*** output. *SPEC* is one of:

    - **source** — by upstream (dinit / systemd / runit / s6 / openrc / upstart / slinit)
    - **category** — by functional area (lifecycle / dependency / logging / cgroup / …)
    - **kind** — by feature space (directive / opcode / option)

    Without it, text output is one list sorted by name, and markdown
    output is grouped by **kind**. JSON output is always a single flat
    array. The **source** grouping is the one *doc/features.md* uses.

**--format=**\ *SPEC*
:   Output format for enumeration. *SPEC* is one of:

    - **text** (default) — plain aligned columns for terminal reading
    - **json** — machine-readable, one array of objects (name, kind,
      source, category, notes)
    - **markdown** — regenerable tables suitable for docs

**--version**
:   Print slinit-supports's version and exit.

**-h**, **--help**
:   Print a usage summary and exit.

# EXAMPLES

Check whether a specific directive is supported:

    slinit-supports restart
    slinit-supports memory-pressure-watch
    slinit-supports CmdReloadAll
    slinit-supports option:pass-cs-fd

Enumerate every directive the parser accepts:

    slinit-supports --list-directives

Regenerate *doc/features.md* verbatim (used as the canonical
source-of-truth for the feature surface in this repo):

    slinit-supports --format=markdown --list-all --group-by=source \
      > doc/features.md

Diff two slinit builds' feature surfaces to catch surprise
regressions or additions in a release-candidate:

    slinit-supports --format=json --list-all > /tmp/rc.json
    slinit-supports --format=json --list-all > /tmp/main.json     # from other build
    diff <(jq -S . /tmp/main.json) <(jq -S . /tmp/rc.json)

# EXIT STATUS

**0**
:   Look-up mode: the named feature is supported. Enumeration mode:
    output emitted successfully.

**1**
:   Look-up mode: the named feature is unknown. A "No" line is printed
    on stdout and "slinit-supports: no" on stderr. Also returned when
    neither a *NAME* nor a **--list-\*** flag is given.

**2**
:   Usage error (unknown flag, unknown *SPEC* or **--format** value).

# SEE ALSO

**slinit**(8), **slinitctl**(8), **slinit-service**(5),
**slinit-check**(8)

The compiled feature inventory this tool emits is checked into
*doc/features.md* in the source tree and refreshed on every release.
