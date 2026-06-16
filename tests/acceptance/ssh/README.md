# tests/acceptance/ssh

Acceptance tests for slinit run against a **live remote VM** over SSH —
distinct from the QEMU-based `tests/functional/` suite, which boots a
disposable initramfs.

## When to use

- After deploying a new slinit build to a real (or VM-resident) system.
- To verify a production install matches the expected feature contract.
- As part of a release gate before promoting an ISO.

These tests **do** mutate state on the target — they `load`/`start`/`stop`/
`unload` services in the `acceptance-test-*` namespace under
`/etc/slinit.d/`. Read-only cases (`01-03`) touch nothing.

## Required environment

The runner refuses to start without:

| Var                | Purpose                            |
|--------------------|------------------------------------|
| `ACCEPTANCE_HOST`  | SSH host (e.g. `ceres.example.org`) |
| `ACCEPTANCE_PORT`  | SSH port                            |
| `ACCEPTANCE_USER`  | SSH user (typically `root`)         |

Optional:

| Var                  | Default            |
|----------------------|--------------------|
| `ACCEPTANCE_SSH_KEY` | (use ssh agent / `~/.ssh/config`) |
| `VERBOSE`            | `0` — set to `1` for full per-case output |
| `KEEP_REMOTE`        | `0` — set to `1` to leave the remote scratch dir for forensics |

## Usage

```sh
ACCEPTANCE_HOST=ceres.example.org \
ACCEPTANCE_PORT=40003 \
ACCEPTANCE_USER=root \
./run.sh
```

Run a subset:

```sh
ACCEPTANCE_HOST=... ACCEPTANCE_PORT=... ACCEPTANCE_USER=... \
  ./run.sh cases/04-start-stop.sh cases/05-restart.sh
```

## Layout

```
run.sh             # orchestrator (host-side)
lib/
  ssh.sh           # ssh/scp helpers (host-side)
  remote-prelude.sh # sourced by each case on the remote
cases/
  01-version.sh
  02-control-socket.sh
  03-essential-services.sh
  04-start-stop.sh
  05-restart.sh
  06-reload-signal.sh
  07-dependency.sh
  08-scripted-failure.sh
  09-triggered.sh
  10-cleanup.sh
```

Each `cases/NN-*.sh` is a self-contained shell script. The runner:

1. `scp`'s it (and `lib/remote-prelude.sh`) to `/tmp/slinit-acceptance.<pid>/`.
2. Executes it remotely as a single `ssh` invocation.
3. Aggregates the line-counted `OK:` / `FAIL:` results from each case's
   `test_summary`.

## Why not Go test runner?

The host running the suite may not have a Go toolchain installed (think
release machines, CI bastions). Bash + ssh is the lowest common denominator
and matches the `tests/functional/` style.
