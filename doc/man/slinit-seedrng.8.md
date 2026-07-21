% SLINIT-SEEDRNG(8) slinit | Sunlight Linux
% Ionut Nechita
% 2026-07-21

# NAME

slinit-seedrng - persist entropy across reboots via the SeedRNG protocol

# SYNOPSIS

**slinit-seedrng** [**\--seed-dir** *DIR*] [**\--skip-credit**] [**\--quiet**]

# DESCRIPTION

**slinit-seedrng** implements one cycle of the SeedRNG protocol (Jason
Donenfeld, upstream at *https://git.zx2c4.com/seedrng/*): it feeds any
on-disk seed files to the Linux kernel RNG via **RNDADDENTROPY**, then
writes a fresh seed for the next boot. The systemd equivalent is
**systemd-random-seed**(8); the OpenRC equivalent is the **seedrng**
init.d script.

Typical wiring on a slinit-managed system is to invoke it twice:

- **Early at boot**, before any user service starts, to hand persisted
  entropy to the kernel while the pool is still under-mixed.
- **At shutdown**, so a fresh seed derived from the (well-seeded)
  running pool is on disk for next boot.

Every invocation:

1. Locks the seed directory (**flock(LOCK_EX)** on the dirfd) so two
   concurrent invocations cannot double-credit the same seed.
2. Consumes **seed.no-credit** (fed to the kernel *without* crediting).
3. Consumes **seed.credit** (fed to the kernel *with* crediting,
   unless **\--skip-credit** overrides).
4. Reads a fresh seed via **getrandom**(2) — **GRND_NONBLOCK** first;
   on **EAGAIN**/**ENOSYS** falls back to **GRND_INSECURE**, then to
   */dev/urandom*.
5. Mixes wallclock + boottime + prior state via SHA-256 and folds the
   digest into the trailing 32 bytes of the fresh seed.
6. Writes the fresh seed as **seed.no-credit** first, then
   **renameat(2)**-s to **seed.credit** iff **getrandom(GRND_NONBLOCK)**
   returned real entropy. If the kernel pool was not yet initialised,
   the seed stays non-creditable — replaying it on the next boot must
   not falsely inflate the entropy estimate.

Consumed seed files are **unlinkat(2)**-ed with a **fsync(2)** of the
dir before the ioctl runs, so a crash between "read" and "credit"
cannot silently replay the same entropy on the next boot.

# OPTIONS

**\--seed-dir** *DIR*
:   Directory holding seed files. Defaults to */var/lib/seedrng*
    (matches OpenRC + systemd conventions). Created if missing, with
    mode **0700**.

**\--skip-credit**
:   Do not claim entropy on the *incoming* seed, even if it was marked
    creditable. Use in environments where an attacker might have
    controlled the on-disk seed (image build, forensic recovery,
    factory reset).

**\--quiet**, **-q**
:   Suppress informational logging on stderr. Errors are always
    reported.

**\--version**
:   Print the binary version and exit.

# EXIT STATUS

**0**
:   All steps succeeded — old seeds consumed and credited (as
    requested), fresh seed persisted.

**1**
:   One or more steps failed. **slinit-seedrng** always tries to write
    a fresh seed even on partial failure; a non-zero exit signals that
    an operator should investigate (typical causes: filesystem
    read-only, **CAP_SYS_ADMIN** missing for the ioctl, pool not
    initialised so the new seed is non-creditable).

The program refuses to run as non-root (**RNDADDENTROPY** requires
**CAP_SYS_ADMIN**) and exits with status 1 immediately.

# FILES

*/var/lib/seedrng/seed.credit*
:   Present iff the last cycle wrote a creditable seed (i.e. the pool
    was initialised at that moment).

*/var/lib/seedrng/seed.no-credit*
:   Transient file: written first on every cycle, renamed to
    *seed.credit* if creditable. If **slinit-seedrng** crashes between
    write and rename the next boot will see this file and consume it
    without crediting — the safe default.

*/proc/sys/kernel/random/poolsize*
:   Read to size the new seed. Value clamped to **[32, 512]** bytes.

*/dev/urandom*
:   Target of the **RNDADDENTROPY** ioctl (yes, the ioctl target is
    */dev/urandom*, not */dev/random* — same underlying pool since
    Linux 5.6, and the historical device split does not matter for
    RNDADDENTROPY).

# INTEGRATION

Suggested service snippet (early boot):

```
# /etc/slinit.d/seedrng-early
type = scripted
command = /usr/bin/slinit-seedrng
before: boot
```

And at shutdown, via a shutdown-hook:

```
# /etc/slinit/shutdown-hook
#!/bin/sh
/usr/bin/slinit-seedrng --quiet
```

# SEE ALSO

**slinit**(8), **slinit-service**(5), **getrandom**(2),
**random**(4), **systemd-random-seed**(8)

# AUTHORS

Ported to Go for slinit by Ionut Nechita and contributors. Based on
Jason Donenfeld's SeedRNG (see upstream at
*https://git.zx2c4.com/seedrng/*) and OpenRC's C port. slinit is
licensed under Apache 2.0.
