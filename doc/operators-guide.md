# slinit operator's guide

For people who already run systems and now have to run this one. The
[README](../README.md) is the reference — every directive, every flag,
every feature. This is the other thing: what to type, what maps to what,
and which differences will bite you.

If you know systemd, start at [Coming from systemd](#coming-from-systemd).
If you just need a service running, start at
[Your first service](#your-first-service).

---

## Coming from systemd

### The five differences that actually matter

Everything else is naming. These change what you do.

**1. slinit has no targets.** There is no `multi-user.target`, no
`network.target`. Ordering is expressed by dependencies between services,
and the boot milestone is a service called `boot`. A unit's
`After=network.target` is dropped on conversion, with a note, because
turning it into a dependency on a service named `network` would make the
unit fail to load on any system that has no such service.

**2. `slinitctl reload` is not `systemctl reload`.** slinit's `reload`
re-reads the *service description* from disk — it is `systemctl
daemon-reload` for one service. Telling a running daemon to reload its own
config is `slinitctl reload-signal`, and it needs a `reload-signal =`
directive naming the signal to send.

**3. `slinitctl enable` starts the service.** It is `systemctl enable
--now`, not `systemctl enable`. If you want the dependency recorded
without starting anything, that is not what `enable` does.

**4. There is no `.socket` / `.timer` / `.path` unit.** Those capabilities
exist, but as directives on the service itself — socket activation, the
`cron-calendar` / `cron-interval` family, path activation. One file per
service, always.

**5. A service can be running without being "enabled".** slinit
distinguishes *marked active* (something or someone asked for it
explicitly) from *running because a dependent needs it*. `slinitctl list`
shows the difference, and it decides whether a service survives its
dependents going away. See [Reading the state
column](#reading-the-state-column).

### Command cheat-sheet

| systemd | slinit | notes |
|---|---|---|
| `systemctl start X` | `slinitctl start X` | blocks until the service settles; `--no-wait` to return immediately |
| `systemctl stop X` | `slinitctl stop X` | |
| `systemctl restart X` | `slinitctl restart X` | |
| `systemctl status X` | `slinitctl status X` | |
| `systemctl cat X` | `slinitctl show X` | dumps effective config as `Key=Value`, not the file text |
| `systemctl list-units` | `slinitctl list` (`ls`) | |
| `systemctl is-active X` | `slinitctl is-started X` | exit status, no output |
| `systemctl is-failed X` | `slinitctl is-failed X` | |
| `systemctl enable X` | `slinitctl enable X` | **also starts it** — see difference 3 |
| `systemctl disable X` | `slinitctl disable X` | also stops it |
| `systemctl daemon-reload` | `slinitctl reload-all` | |
| `systemctl reload X` | `slinitctl reload-signal X` | needs `reload-signal =` on the service |
| — | `slinitctl reload X` | re-read *this* service's description from disk |
| `systemctl kill -s SIG X` | `slinitctl signal SIG X` | |
| `systemctl list-dependencies X` | `slinitctl dependents X` | the other direction; `slinitctl graph` for DOT output |
| `systemctl mask X` | `manual = yes` **+** `refuse-manual-start = yes` | neither alone is mask: `manual` blocks auto-activation but still allows an explicit start; `refuse-manual-start` blocks the explicit start but still allows activation as a dependency |
| `journalctl -u X` | `slinit-journalctl -u X` | the package also symlinks it as `journalctl` |
| `systemd-analyze` | `slinitctl boot-time` | |
| `systemctl poweroff` / `reboot` | `slinitctl poweroff` / `reboot` | or `slinitctl shutdown <type>` |
| `systemctl soft-reboot` | `slinitctl softreboot` | |
| — | `slinitctl start-all` | start everything not already running; no systemd equivalent |

`slinitctl --help` is the authoritative list; this table is the subset you
reach for daily.

### Running your existing units

slinit reads `.service` files directly. A name with no native description
resolves to a unit under `/etc/systemd/system`, `/run/systemd/system`,
`/usr/lib/systemd/system` or `/lib/systemd/system`, in that order, and it
is translated in memory — nothing is written to disk.

```sh
slinitctl start nginx          # finds /etc/systemd/system/nginx.service
```

Both spellings work: `nginx` and `nginx.service`. A native description in
`/etc/slinit.d` always wins, so migrating one service at a time is safe.
Only `.service` loads; timers, sockets and targets are not units slinit
pretends to understand.

To see what a unit becomes, and what did not survive:

```sh
slinit-systemd-convert -verbose /etc/systemd/system/nginx.service
```

It prints the slinit description on stdout and the notes on stderr. Read
the notes. The common ones:

- **`After=…​.target` dropped.** Add `depends-on:` or `waits-for:` naming
  a real service if the ordering matters.
- **`ExecReload` dropped.** slinit's reload is signal-based; add
  `reload-signal = HUP` (or whichever signal the daemon wants) to a native
  description. A unit cannot express it.
- **`PrivateDevices`, `PrivateNetwork`, `RestrictSUIDSGID` and a few
  others** have no slinit equivalent and are reported so you know the
  hardening is not there.

Directives that *do* carry over include the `Protect*` / `Restrict*`
cluster, `PrivateTmp`, `SystemCallFilter`, `RuntimeDirectory` and its
siblings, `Nice`, `OOMScoreAdjust`, `User`/`Group`, `EnvironmentFile`,
`PIDFile`, `Restart`, and the timeouts.

When a unit is loaded live rather than converted, those same notes go to
the daemon log at warning level, so a dropped hardening directive is
something you can find rather than something that silently did not happen.

---

## Your first service

A service is one file in `/etc/slinit.d/`, named after the service. No
extension, no `[Section]` headers, one `key = value` per line.

```sh
cat > /etc/slinit.d/hello <<'EOF'
type = process
command = /usr/bin/myserver --port 8080
restart = on-failure
EOF
```

No dependency line on purpose. A hard dependency on a service that does
not exist is fatal — the description will not load at all — and names
like `network` exist on some systems and not others. Add
`depends-on: <name>` once you have checked that `<name>` is really there,
with `slinitctl list`. `depends-on` accepts `:` as well as `=`, the dinit
convention for dependency keys; both work everywhere.

Check it before you run it — the linter parses the file exactly as the
daemon would:

```sh
slinit-check -d /etc/slinit.d hello
```

Then start it, and have it start at boot:

```sh
slinitctl start hello        # now
slinitctl enable hello       # at boot, and now (enable starts it)
```

### Picking a type

| the program… | `type =` | also needs |
|---|---|---|
| runs in the foreground and stays there | `process` | — |
| forks and the parent exits | `bgprocess` | `pid-file =` |
| does a job and exits | `scripted` | optional `stop-command =` |
| is just a grouping name | `internal` | — |
| should wait for a manual signal | `triggered` | `slinitctl trigger` |

Most daemons with a `--foreground` or `--no-daemon` flag are happier as
`process`: slinit supervises the PID it forked, with no pidfile in the
middle. Reach for `bgprocess` when the program insists on backgrounding
itself.

### Logging

Nothing extra is needed — a service's output goes to the journal, and
`slinit-journalctl -u hello` reads it. `log-type = buffer` keeps a
per-service ring buffer that `slinitctl catlog hello` prints, which is
useful for something noisy you do not want in the journal; `log-type =
file` with `logfile = /var/log/hello.log` writes to a file instead.

---

## Reading the state column

`slinitctl list` prints an eight-character indicator. The distinction that
matters is brackets versus braces:

```
[[+]     ] boot          started, marked active
[{+}     ] nginx         started, because something depends on it
[     {-}] worker        stopped
[{ }<<   ] slow-svc      starting
```

- `[+]` — **marked active.** Someone asked for this explicitly: an
  operator `start`, an `enable`, or the boot graph.
- `{+}` — **dependency only.** It is running because a dependent needs
  it. When that dependent stops, this may be released and stopped too.

This is why a freshly booted system and a system you have poked at by hand
do not look the same, even with everything running. It also explains a
service stopping "on its own" after you stop something else: it was never
marked active.

`slinitctl start-all` marks everything it starts as active. That is
convenient after a mess, but it is not the post-boot state — if you want
that exactly, `slinitctl shutdown softreboot` re-runs the boot.

---

## Troubleshooting

### A service will not start

```sh
slinitctl status X          # State, Target, Exit code
slinit-journalctl -u X      # what the daemon said
slinit-check -d /etc/slinit.d X   # is the description even valid
```

`Target:` is the field people miss. `Target: stop` means slinit does not
want this service running — it will not restart it no matter what
`restart =` says. `Target: start` with `State: STOPPED` means it tried and
failed.

### "service description not found" for a dependency

A missing hard dependency is fatal: the dependent will not load at all.
Most often this is a converted unit whose `depends-on` names something
that does not exist on this machine. Check with
`slinit-check -d /etc/slinit.d X`, which resolves dependencies.

### `slinitctl start` seems to hang

`start` waits for the service to settle. For a `triggered` service that is
forever, because it is waiting for `slinitctl trigger`. Use
`slinitctl --no-wait start X`.

### A `bgprocess` fails to start with no obvious error

It is usually the pidfile. slinit runs `command`, waits for it to exit,
and then reads `pid-file` to learn the daemon's PID. Check that the
directory exists at the moment the daemon writes there — `/run` is a
tmpfs, so a directory you created by hand is gone after a reboot. Use
`runtime-directory = name` and slinit creates `/run/name` before the
service starts.

### `stop command failed (exit code 5)`

Exit 5 from `slinit-start-stop-daemon` or `slinit-supervise-daemon` means
*stale pidfile*: the file exists, the process it names does not. Usually
something killed the daemon behind slinit's back. For
`slinit-start-stop-daemon`, `--oknodo` turns this into success when you do
not care.

### Everything stopped when I stopped one thing

Look at the state column. Services showing `{+}` rather than `[+]` are
running only because something depends on them, and they are released when
that dependent goes away. `slinitctl start X` (not `wake`) marks a service
active so it stays up on its own.

### Where did my service's output go?

`slinit-journalctl -u X`. If the service sets `log-type = buffer`, output
goes to a per-service ring buffer instead — `slinitctl catlog X`. With
`log-type = file` it goes to the path in `logfile =`.

---

## See also

- [README](../README.md) — the full reference: every directive, every flag.
- **slinit-service(5)** — the service description format, directive by
  directive, with `(since X.Y.Z)` markers.
- **slinitctl(8)** — every subcommand and its exact semantics.
- **slinit(8)** — PID 1 behaviour, kernel command line, foreign service
  formats.
- **slinit-systemd-convert(8)** — the unit converter.
- [`demo/`](../demo/README.md) — a QEMU VM with ~44 services covering most
  directives, including a stock nginx unit run straight from
  `/etc/systemd/system`.
