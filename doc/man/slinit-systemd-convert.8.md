% SLINIT-SYSTEMD-CONVERT(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-09-08

# NAME

slinit-systemd-convert - port a systemd .service unit to a slinit service file

# SYNOPSIS

**slinit-systemd-convert** [*flags*] *unit-file* [*unit-file* ...]

# DESCRIPTION

**slinit-systemd-convert** reads one or more systemd unit files
(**.service** only) and emits equivalent slinit service files.

Only **.service** units are handled. **timer**, **socket**, **path**,
**mount**, and **target** units are systemd-specific abstractions
that map onto different slinit facilities — timer to a cron-alike
service, socket to **socket-listen** on the target service, path to
**start-on-path-\*** directives, mount to **slinit-mount**(8), target
to a **type = internal** aggregate. Convert those by hand.

The unit is read as systemd reads it: a line ending in an unescaped
backslash continues on the next, and a comment line (first non-blank
character **#** or **;**) is skipped even inside such a continuation,
so an option commented out of a continued **ExecStart** stays out.

The converter recognises the [Unit], [Service] and [Install] keys that
have a slinit counterpart, among them **Description**, **Type**
(**simple**/**exec** → **process**, **forking** → **bgprocess**,
**oneshot** → **scripted**), **ExecStart**, **ExecStop**,
**ExecStartPre**, **ExecStartPost**, **Restart**,
**RestartSec**, **User** / **Group** (merged into **run-as**),
**EnvironmentFile**, **WorkingDirectory**, **RootDirectory**,
**PIDFile**, **KillSignal**, **TimeoutSec** / **TimeoutStartSec** /
**TimeoutStopSec**, **Nice**, **OOMScoreAdjust**, **UMask**,
**LimitNOFILE** / **LimitCORE** / **LimitDATA** / **LimitAS**, the
service directories (**RuntimeDirectory** and its siblings),
**Slice**, **Delegate**, the **Condition\*** keys slinit has,
**NoNewPrivileges**, **PrivateTmp**, **ProtectSystem**,
**ProtectHome**, the **ProtectKernel\***, **ProtectClock** and
**ProtectControlGroups** family, **SystemCallFilter** (several lines
merge as in systemd; when an allow-list is followed by a **~** line,
or the other way round, the groups are expanded into one list of
syscall names),
**RestrictAddressFamilies** (a **~** deny-list becomes the allow-list
of every other family, by number), **RestrictNamespaces** (a list of
namespace types becomes **yes**, which refuses them all, with a
warning), **ProtectHostname** (**private** becomes **yes**, with a
warning) and, since 4.0.3,
**ReadOnlyPaths**, **ReadWritePaths**, **InaccessiblePaths**,
**BindReadOnlyPaths**, **TemporaryFileSystem**, **ProtectProc**,
**ProcSubset**, **PrivateNetwork** (→ **namespace-net**; its loopback
interface stays down), **CapabilityBoundingSet** (with **~**) and
**AmbientCapabilities** (→ **capabilities**). A path with the **-**
prefix that does not exist when the unit is converted is left out, as
systemd would ignore it. What these cannot express is left out with a
warning: a **+** path (relative to **RootDirectory**), a specifier such
as **%h**, a **norbind** bind and the **noexec** option of a temporary
file system.

Confinement slinit cannot express makes the conversion fail, and a unit
loaded directly fail to load, rather than run less confined than the
unit says: a **CapabilityBoundingSet** that keeps no capability at all,
**PrivateDevices=yes**, and a **RestrictAddressFamilies=~** naming a
family the converter does not know.

**Restart** keeps its meaning where slinit has it (**always** →
**yes**, **on-success** → **on-success**; **on-failure**,
**on-abnormal** and **on-abort** → **on-failure**). slinit runs one
command per service, so of several **ExecStart** lines (a oneshot) only
the last is kept, with a warning naming the ones dropped; likewise only
the first **ExecStartPre** and **ExecStartPost**.

An **Exec** line is split into words as systemd splits it: single and
double quotes group, **""** is an empty argument, the C escapes
(**\\t**, **\\n**, **\\x41**, **\\s** ...) are decoded, specifiers are
expanded in each word once it is unquoted, and each word is written
back quoted so that slinit's parser yields the same argument. A lone
**;** separates commands and **\\;** is a literal **;** argument; each
command counts as one more line of its kind (only one **ExecStop** is
kept, the last, with a warning). An argument holding a newline cannot
be written in a slinit file: that command is dropped with a warning.
A line systemd refuses (unbalanced quotes after the path, an empty path) makes the
conversion fail, unless its command has the **-** prefix.

The prefixes of an **Exec** command: **@** gives **command-argv0**
(main command only); an argv[0] that a **command-argv0** line cannot
carry as it is (empty, holding a control character such as a newline,
or starting or ending with a blank) is dropped with a warning, and the
program gets its path as argv[0]. **:** is kept exactly: every **$** is
doubled, in **command-argv0** too, so slinit expands no variable
either. **|** runs the rest as one
**/bin/sh -c** script (systemd uses the login shell of **User=**, and
**/bin/sh** when it has none; with **@**, argv[0] is **-/bin/sh**),
after substituting variables in each word as above. **-** on
**ExecStartPre**, and on the **ExecStart** of a **oneshot**, runs the
command through **/bin/sh -c '"$@" || :'**, so that its failure,
a missing program included, is ignored as in systemd; on **ExecStartPost**
and **ExecStop** a failure already fails nothing in slinit. **-** on the
**ExecStart** of a long-running service, and **+** and **!** wherever
their meaning is lost, are reported as warnings.

slinit runs **pre-start-command**, **post-start-command** and
**stop-command** with the service's **run-as** user, group and
**supplementary-groups**, as systemd runs **ExecStartPre**,
**ExecStartPost** and **ExecStop** with **User=** and **Group=**
(since 4.0.3; until 4.0.3 the two start hooks ran as root, and a unit
with **User=** and such a hook got a warning). **+** on those three
lines becomes slinit's own **+** prefix (see **slinit-service**(5)), so
the command runs as root: **ExecStartPre=+/bin/mkdir -p /run/foo**
becomes **pre-start-command = +/bin/mkdir -p /run/foo**. **!** (run as
root, inside the sandbox) on **ExecStartPre** and **ExecStartPost**
becomes **+** too: slinit gives a start hook no sandbox either way, so
root is what is kept. **!** on **ExecStop**, and **+** and **!** on
**ExecStart**, stay warnings: slinit would drop the stop command's
sandbox, and its main **command** takes no prefix. A program whose own
path starts with **+** (systemd's **!+path**) cannot be written without
slinit reading that **+** as its prefix, and is dropped with a warning.

**TimeoutStartSec** becomes **start-timeout** and **TimeoutStopSec**
**stop-timeout**; **TimeoutSec** sets both. **infinity** and **0** both
mean no timeout, as in systemd, and are written as **0**. A value that
is no time span (**garbage**, **-1**, an empty one) is ignored with a
warning, as systemd ignores it, and the timeout keeps its earlier value
or default. A
**Type=oneshot** unit that sets neither **TimeoutStartSec** nor
**TimeoutSec** (to a valid span) gets **start-timeout = 0**: systemd
gives a oneshot no start timeout by default, where slinit's default is
60 seconds.

systemd substitutes a variable in an **Exec** line in two places only:
a word that is **$VAR** and nothing else (its value split into words,
none if it is empty or unset), and **${VAR}** anywhere in a word (its
value as one argument); **$$** is a **$**. Every other **$** is text to
systemd: **$VAR** inside a word, such as the **$HOME** of
**sh -c 'echo $HOME'**, **$1**, **$@**, **${VAR:-default}**, an
unclosed **${**. slinit expands every **$** when the service file is
loaded, so those are written **$$** and reach the command as systemd
passes them on; the path of the command is never expanded, as in
systemd. A word that is **$** followed by no valid name (**$1**,
**$HOME/x**) is dropped, and a **${...}** with no valid name inside
becomes empty, as systemd does at run time, with a warning. The
variables systemd would substitute are not what they are under
systemd, which takes them from the unit's **Environment** and
**EnvironmentFile** when it runs the command: slinit expands them when
the service file is loaded, from its own environment, so one the
env-file sets comes out empty. Each such line is warned about. slinit reads
a single **env-file**: of several **EnvironmentFile** lines the last is
kept, with a warning. slinit has no **$MAINPID**: an **ExecStop** of
the form **kill** [**-s** *SIG*] **$MAINPID** becomes
**term-signal =** *SIG* when *SIG* is **TERM**, **INT**, **QUIT**,
**KILL** or the signal **KillSignal** names (a **HUP** or **USR1** sent
that way reloads the daemon rather than stopping it, and such an
**ExecStop** is dropped with a warning), and any other **ExecStop**
using it is dropped with a warning, leaving slinit to stop the service with its
term-signal.

Dependencies: **Requires** and **BindsTo** become **depends-on**
(which stops this service when the dependency stops, as **BindsTo**
does); **Wants** becomes **waits-for**; **After** and **Before** become
**after** and **before**, ordering only, as in systemd (since 4.1.0;
through 4.0 **After** became **waits-for** and so also started the
other service, and **Before** was dropped); **Requisite** becomes
**assert-service-started**. Only **.service** and **.socket** references become
dependencies, a socket as the service it activates
(**Requires=dm-event.socket** is **depends-on: dm-event**); a unit's
reference to itself, such as its own socket, is dropped with a note. A
reference to a **.target**, **.mount**, **.device**, **.slice**,
**.path**, **.timer** or any other unit type is dropped with a note,
since slinit has no such services (and **sysroot.mount** is no
**sysroot** service). slinit fails to load a service whose
dependency of any kind does not exist, so each name written must exist
as a slinit service (or a unit slinit can load) on the target system.
A unit loaded directly is more forgiving, as systemd is: see
**slinit**(8). **PartOf**, **Upholds**, **PropagatesReloadTo**,
**PropagatesStopTo**, **JoinsNamespaceOf**, **RequiresMountsFor** and
**OnSuccess** have no slinit equivalent and are reported with what to
use instead; **WantedBy** / **RequiredBy** produce a note suggesting
**slinitctl enable**.

Not carried over, and reported: inline **Environment=** (slinit takes
an **EnvironmentFile** only), **ExecReload** (slinit's reload is a
signal — see **reload-signal**), **ExecStopPost**, **WatchdogSec**,
**LoadCredential** / **SetCredential** / **ImportCredential**,
**PrivateUsers**, and any
other key the converter does not know — **KillMode**,
**RemainAfterExit** and **LimitNPROC** among them.

Warnings — something was dropped or will not behave as it did — are
always printed on stderr; notes about mappings that did happen, or
about references dropped as expected (such as **.target** units), are
printed only with **-verbose**.

Single-input mode writes to standard output. Batch mode
(**--output-dir**) writes one file per input into *DIR*, named
after the input basename with the *.service* (and any trailing *.in*)
suffix stripped.

# FLAGS

**-output-dir** *DIR*
:   Batch mode: write one slinit file per input into *DIR*, which is
    created if missing. Without this flag, output goes to stdout and
    only one unit may be passed at a time.
    A file already at an output name is replaced, so a rerun
    regenerates it; each file is written to a temporary name in *DIR*
    and renamed into place, so an interrupted run never leaves a
    partial file. A symbolic link at an output name is refused, never
    written through or replaced.
    A replaced file keeps its permission bits (and, run as root, its
    owner and group); a new one is created with mode 0644 less the
    umask. A hard link to the old file keeps the old content.

**-dry-run**
:   With **-output-dir**: print each file that would be written, on
    stderr, without touching the filesystem.

**-verbose**
:   Also print the per-service conversion notes to stderr — dropped
    target dependencies and manual follow-ups. Warnings are printed
    without it.

# EXAMPLES

Convert a single unit and inspect the result:

    slinit-systemd-convert /etc/systemd/system/nginx.service > \
        /etc/slinit.d/nginx

Bulk-convert every systemd unit that ships with a package into a
staging area for review:

    mkdir /tmp/staging
    slinit-systemd-convert --output-dir=/tmp/staging \
      --verbose /usr/lib/systemd/system/*.service

Preview a bulk conversion:

    slinit-systemd-convert --output-dir=/tmp/staging --dry-run \
      --verbose /usr/lib/systemd/system/*.service

# EXIT STATUS

**0**
:   All requested conversions completed.

**1**
:   No input, several inputs without **-output-dir**, the output
    directory could not be created, or one or more inputs could not be
    read, parsed or written — a non-**.service** unit, or a symbolic
    link at an output name, included.

**2**
:   Unknown flag.

# SEE ALSO

**slinit-service**(5), **slinit-openrc-convert**(8),
**slinit-runit-convert**(8), **slinit-check**(8),
**slinit-runner**(8), **systemd.service**(5)
