# slinit operator's guide

For people who already run systems and now have to run this one. The
reference is the man pages — [slinit(8)](man/slinit.8.md) for the daemon,
[slinit-service(5)](man/slinit-service.5.md) for every directive,
[slinitctl(8)](man/slinitctl.8.md) for the CLI — with worked examples in
[configuration.md](configuration.md) and the generated feature surface in
[features.md](features.md). This is the other thing: what to type, what
maps to what, and which differences will bite you.

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
| `systemd-analyze plot` | `slinitctl analyze plot` | SVG timeline on stdout; redirect it to a file |
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

## Scaling a hot port across workers

One process accepting on one socket is a ceiling. The usual answer is N
worker processes behind the same port, and slinit builds it out of pieces
it already has: a service template for the N, and `socket-reuseport` so
they may all bind the same address.

Write the worker once, as a template — a description whose name carries no
`@`:

```sh
cat > /etc/slinit.d/web <<'EOF'
type = process
command = /usr/bin/myserver --listen-fd 3
description = web worker $1
socket-listen = tcp:0.0.0.0:8080
socket-reuseport = yes
socket-activation = immediate
EOF
```

Then start as many as you want cores busy:

```sh
for i in 1 2 3 4; do slinitctl start "web@$i"; done
```

Each instance is an ordinary service: its own PID, its own `status`, its
own restart policy. slinit opens a listening socket per instance, all on
`0.0.0.0:8080`, and passes it as fd 3 under `LISTEN_FDS=1`. The kernel
hashes each incoming connection to one of them.

The reason to prefer this over one process handed a single shared fd is
what happens when a worker dies: only its own socket leaves the set, so
the remaining workers keep serving the port while slinit restarts that
one. A single shared listener dies with the process holding it.

Two constraints worth knowing before you build on it:

- **The program must accept on the inherited fd.** slinit binds and
  listens; it does not accept. A server that only knows how to bind its
  own port cannot be used this way — it needs `--listen-fd`-style support,
  or the systemd socket-activation convention (`LISTEN_FDS`, `LISTEN_PID`,
  and fd 3 upward).
- **`socket-reuseport` is for `tcp:` and `udp:` listeners.** On a Unix
  socket the kernel accepts the option and does nothing with it, so slinit
  does not set it there.

To see the listeners, `netstat -ltn` (or `ss -ltn`) shows one line per
instance on the shared port.

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

### My laptop does nothing when I close the lid

By default that is correct: every lid and power-key handler ships as
`ignore`. Turning them on would have changed what the hardware does on
upgrade with nothing having asked for it, so it is opt-in.

On GNOME or XFCE you may not want them on at all — both read logind's
`HandleLidSwitch` property to decide whether logind already owns the key,
and `ignore` tells them to handle it themselves, which they do, with a UI
and a per-user setting. Configure slinit's side for a bare window
manager, or when the policy must hold with no session running.

Create `/etc/slinit/logind.conf`:

```ini
[Login]
HandleLidSwitch=suspend
HandleLidSwitchExternalPower=ignore
HandlePowerKey=poweroff
HoldoffTimeoutSec=30
```

Then restart the daemon: `slinitctl restart slinit-logind`. It reads the
file once at start-up. If a value does not parse it says so on stderr and
keeps the default, so check the log rather than assuming it took.

`loginctl show-manager` reports what it actually believes, which is the
quickest way to tell a config that was read from one that was not. An
existing `/etc/elogind/logind.conf` is used when slinit's own file is
absent, so a machine migrating off elogind needs no copying.

### The screen does not lock when the machine suspends

The locker listens for logind's `PrepareForSleep` signal, so this means
either nothing is suspending through logind, or no locker is running.

`slinitctl suspend` goes through logind by default, so it does lock. The
exception is `slinitctl suspend --no-coordination`, which is the
low-level door straight to PID 1: the sleep hook still runs either side,
but no signal is emitted and no inhibitor is consulted, so nothing locks.
Use that one for testing a hook. `freeze` and `standby` always take the
direct path, because `org.freedesktop.login1` has no method for them —
the command tells you so on stderr.

To see whether the handshake is happening at all:

```sh
gdbus monitor --system --dest org.freedesktop.login1 | grep -i preparefor
```

Then suspend. Two lines, `true` then `false`, mean logind is announcing
it. None means the suspend went around it.

### Something is stopping the machine from suspending

```sh
loginctl list-inhibitors
```

A `block` lock on `sleep` refuses a suspend outright, and the error names
the holder and its pid. A `delay` lock only buys time — up to
`InhibitDelayMaxSec`, five seconds by default — and then the sleep
proceeds whether the holder let go or not, deliberately: a locker that
crashed holding one must not keep a lid-shut laptop awake.

A lock lasts until the file descriptor its holder was given is closed.
There is no release command, so a stuck lock means a stuck process; kill
it and the lock goes with it.

### Boot got slower and I want to see where

`slinitctl analyze time` ranks services by how long each took, which
answers "what is slow". It cannot answer "what was everything else
waiting for" — for that, `slinitctl analyze plot > boot.svg` draws the
same data as a timeline, one lane per service, and open it in a browser.

Read it by where bars *start*, not by how wide they are. A wide bar is a
slow service. A bar that starts late is a service that was ready and
blocked, and the thing that ends just before it is usually what it was
blocked on. `slinitctl analyze critical-chain` then names that sequence
in text.

The bar covers the wait too: it runs from the moment slinit asked the
service to start, which is before its dependencies were satisfied. That
is deliberate and matches what `analyze time` reports as one number, so
a service that does nothing slowly still shows a wide bar when it spent
the time queued behind something else.

Only the boot window is drawn. Anything started after the boot target
came up is a restart or an operator action; it is counted in the header
rather than plotted, because a service restarted a week into uptime
would otherwise compress the whole boot into one pixel.

### The boot stopped at a box asking me to press a letter

That is one of three rescue prompts, and the title line says which:

| Title | What happened | What to press |
|---|---|---|
| `BOOT FAILURE — cannot continue` | No boot service could be loaded at all — usually a typo in a name, or a missing file | `s` for a shell to fix it, then `c` to retry without rebooting |
| `BOOT COLLAPSE — all services stopped` | Everything that was up has gone down | `s` to restart the boot sequence, `e` to start the recovery service |
| `BOOT DEBUGGER — Ctrl-B intercepted` | You pressed Ctrl-B during boot; nothing is wrong | `c` to carry on, `f` to force-fail whatever is stuck |

All three also take `r` to reboot and `p` to power off, act on a single
keypress with no Enter, and auto-act if you say nothing — the countdown
row says what and when, which is a reboot for the first two and
*continue* for the debugger. `Ctrl-D` is an alias for continue and
`Ctrl-B` for the shell, so the same fingers work as in a bootloader.

The errors are printed in red inside the box. If the console is showing
no colour, either `TERM=dumb` or `EINFO_COLOR=no` is set, or the output
is not going to a terminal at all.

The shell you get from `s` is the first of `sulogin`, `bash`, `sh` that
exists. `sulogin` is tried first on purpose: it asks for the root
password before handing over, because physical console access is not the
same thing as a trusted user. `bash` is preferred over `sh` because
busybox `ash`'s line editor sends cursor-position queries on a serial
console and the replies land on its own stdin as stray commands. It runs
on the console with the boot frozen behind it. Exit it and the
prompt comes back. This is the one place to fix a service description
without install media — the file you need is under `/etc/slinit.d/`, and
`slinit-check /etc/slinit.d/<name>` will tell you whether your fix
parses before you press `c`.

### Where did my service's output go?

`slinit-journalctl -u X`. If the service sets `log-type = buffer`, output
goes to a per-service ring buffer instead — `slinitctl catlog X`. With
`log-type = file` it goes to the path in `logfile =`.

---

## See also

- [README](../README.md) — what slinit is, how to install it, and the
  index to everything else.
- **slinit-service(5)** — the service description format, directive by
  directive, with `(since X.Y.Z)` markers.
- **slinitctl(8)** — every subcommand and its exact semantics.
- **slinit(8)** — PID 1 behaviour, kernel command line, foreign service
  formats.
- **slinit-systemd-convert(8)** — the unit converter.
- [`demo/`](../demo/README.md) — a QEMU VM with 57 services covering most
  directives, including a stock nginx unit run straight from
  `/etc/systemd/system`.
