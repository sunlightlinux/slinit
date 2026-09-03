# slinit + Alpine nspawn demo

End-to-end walkthrough of slinit's nspawn integration: fetch a minimal
Alpine rootfs, drop a copy of `slinit` + a handful of demo services
into it, boot the whole thing as a container under `slinit-nspawn`,
and query its journal from the host with `slinit-journalctl -M`.

This is the smallest end-to-end that exercises all three Tier layers:

- **Tier 1** — `pkg/machine` registry files under `/run/slinit/machines/`
- **Tier 2** — `slinit-machinectl list` sees the container immediately
- **Tier 3** — `slinit-nspawn` creates the namespaces + registers the
  container atomically

Contrast with `demo/` (sibling): that one boots Alpine in a full QEMU
VM to prove slinit works as PID 1 on real kernel init. This one keeps
slinit's outer instance intact and adds a namespaced container on top.

## Requirements

- Linux 4.6+ (pivot_root + all the CLONE_NEW* used here)
- Root (or CAP_SYS_ADMIN + CAP_SYS_CHROOT) — namespace and pivot_root
  are privileged operations
- `curl`, `tar`, `gzip` for rootfs fetch
- Built slinit binaries (`slinit`, `slinit-nspawn`, `slinit-machinectl`,
  `slinit-journalctl`). From repo root:
  ```
  go build ./cmd/slinit ./cmd/slinit-nspawn \
           ./cmd/slinit-machinectl ./cmd/slinit-journalctl
  ```

## Layout

```
demo/alpine-nspawn/
├── README.md         — this file
├── fetch-rootfs.sh   — download + extract Alpine minirootfs into rootfs/
├── setup-rootfs.sh   — install slinit + demo services into rootfs/
├── run.sh            — launch slinit-nspawn against rootfs/
├── stop.sh           — SIGTERM the container + verify registry cleanup
└── services/         — slinit service files copied to rootfs/etc/slinit.d/
    ├── boot          — boot bundle
    ├── ticker        — emits a JOURNAL event every 5s (proves -M works)
    ├── httpd         — busybox httpd on port 8080 (proves user services)
    └── shell         — /bin/sh on tty1 (foreground-visible)
```

`rootfs/` and any downloaded tarball are ignored by git (see
`.gitignore`) — the demo is a self-contained working set, not a
committed artifact.

## Steps

Run each script from `demo/alpine-nspawn/` unless noted:

### 1. Fetch Alpine

```bash
./fetch-rootfs.sh
```

Downloads `alpine-minirootfs-<ver>-x86_64.tar.gz` from the Alpine CDN
into `_cache/`, then extracts into `rootfs/`. Idempotent — re-runs
skip already-downloaded tarballs.

### 2. Install slinit into the rootfs

```bash
./setup-rootfs.sh
```

Copies the host-built `slinit` binary (from `../../slinit-*` or
`$SLINIT_BIN`) into `rootfs/sbin/slinit`, installs the demo services
under `rootfs/etc/slinit.d/`, and wires `rootfs/etc/slinit.d/boot`
as the root bundle.

### 3. Boot the container

```bash
sudo ./run.sh
```

Executes:

```
slinit-nspawn --name alpine-demo --boot ./rootfs [-- --system]
```

You should see:

- Container output on your terminal (slinit's boot log from inside
  the container)
- On the host, `slinit-machinectl list` reports `alpine-demo` with
  the container's host-visible PID

### 4. Query the container's journal from the host

In another shell:

```bash
# Show the ticker service's messages, streaming
sudo ./slinit-journalctl -M alpine-demo -u ticker -f
```

Expected output shape: one line every 5 seconds, produced by the
`ticker` service running inside the container, coming out of the
container's slinit event bus and forwarded through
`/proc/<container-pid>/root/run/slinit/events.sock` to the host CLI.

### 5. Poke around

```bash
# Full container journal
sudo ./slinit-journalctl -M alpine-demo -n 50

# One-shot on-disk read (works even if the container's slinit-journald
# hasn't been started)
sudo ./slinit-journalctl -M alpine-demo --file $(readlink -f rootfs)/var/log/slinit-journal/*.jsonl

# What's registered?
sudo ./slinit-machinectl list
sudo ./slinit-machinectl status alpine-demo
```

### 6. Shut down

```bash
sudo ./stop.sh
```

Sends SIGTERM to the container's slinit (which cleanly stops every
service inside), waits for exit, and verifies
`/run/slinit/machines/alpine-demo` is gone.

## What this demo proves

- **`pkg/machine` file format is enough** — no D-Bus, no daemon.
  `slinit-machinectl list` reflects reality by reading the same files
  `slinit-nspawn` wrote.
- **`-M` routes into container journals** without any per-container
  magic — the `/proc/<pid>/root/run/slinit/events.sock` bind hits the
  container's slinit directly from the host namespace.
- **Signal proxy works** — SIGTERM from `stop.sh` reaches slinit
  inside the container as PID 1, so its shutdown sequence runs
  cleanly and the registry entry disappears.

## Troubleshooting

- **`operation not permitted` at `pivot_root`**: not running as root
  or lacking `CAP_SYS_ADMIN`.
- **`no such file` at `/proc/<pid>/root/run/slinit/events.sock`**:
  the container's slinit hasn't opened its journal socket yet; give
  it a second, or use the `--file` fallback (Step 5) which reads
  on-disk JSONL directly.
- **Alpine minirootfs missing `/sbin/init`**: expected — this demo
  execs slinit as PID 1 via `--init=/sbin/slinit`; Alpine's default
  init is unused.
- **`ticker` never fires**: check `slinit-machinectl status
  alpine-demo` — if Alive=no, the container crashed; check the
  scrollback for its slinit's error output.
