#!/bin/bash
# run.sh - Launch slinit demo in QEMU
# Exit VM: Ctrl+A, X  |  Clean shutdown: slinitctl shutdown reboot
#
# Optional bootmode selectors (systemd-parity, from Phases 1-5 of the
# recovery+boot refactor). Combine as needed:
#
#   ./run.sh --rescue         # boot straight to sulogin, mount -a first
#   ./run.sh --emergency      # boot straight to sulogin, no fs mounts
#   ./run.sh --confirm-spawn  # prompt [y/n] before each service start
#   ./run.sh --crash-shell    # spawn shell on PID-1 panic (before reboot)
#   ./run.sh --debug          # slinit.debug — verbose console log
#   ./run.sh --log-level=X    # slinit.log-level=X (debug/info/notice/...)
#   ./run.sh --panic-after=N  # PID-1 panic after N seconds (needs -tags
#                             #   paniconce build, which demo/build.sh
#                             #   sets by default). Pair with --crash-shell
#                             #   to validate the recovery drop-to-sulogin.
#   ./run.sh --debug-shell    # sulogin on /dev/tty9 — note: -nographic
#                             #   serial-only demo cannot switch to VT9,
#                             #   provided for completeness (works on ISO
#                             #   on real hardware with VGA output).
#   ./run.sh --no-monitor     # drop the QEMU monitor mux (`-serial stdio`
#                             #   instead of `-serial mon:stdio`). Use when
#                             #   the recovery shell shows heartbeat-like
#                             #   newlines or truncated commands — the
#                             #   `mon:stdio` demultiplexer occasionally
#                             #   injects sync bytes on some host/xterm
#                             #   combinations. Trade-off: Ctrl+A, X no
#                             #   longer exits QEMU; use `slinitctl
#                             #   shutdown poweroff` from inside, or kill
#                             #   the qemu process from another terminal.
#
# Flags append tokens to the kernel -append line so bootmode.ParseFromProc
# in slinit picks them up. Multiple flags compose: --rescue --debug
# gives you a verbose-log rescue boot.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/_output"
KERNEL="${OUTPUT_DIR}/vmlinuz-virt"
INITRAMFS="${OUTPUT_DIR}/initramfs.cpio.gz"
MEMORY="${MEMORY:-256}"

# Base kernel cmdline: unchanged from the pre-refactor demo so a plain
# `./run.sh` behaves as before.
APPEND="console=ttyS0 rdinit=/sbin/init loglevel=4 i6300esb.heartbeat=60"

# QEMU serial mode. Default keeps the monitor-multiplexed stdio channel
# (Ctrl+A, X exits the VM); --no-monitor peels the mux off — see the
# header comment for the trade-off.
SERIAL_ARGS=(-serial mon:stdio)
EXIT_HINT="Ctrl+A, X to exit"

# Parse selectors — each is a whole flag, KEY=VALUE forms handled inline.
for arg in "$@"; do
    case "$arg" in
        --rescue)         APPEND+=" slinit.rescue" ;;
        --emergency)      APPEND+=" slinit.emergency" ;;
        --confirm-spawn)  APPEND+=" slinit.confirm-spawn" ;;
        --crash-shell)    APPEND+=" slinit.crash-shell" ;;
        --debug-shell)    APPEND+=" slinit.debug-shell" ;;
        --debug)          APPEND+=" slinit.debug" ;;
        --log-level=*)    APPEND+=" slinit.log-level=${arg#--log-level=}" ;;
        --panic-after=*)  APPEND+=" slinit.panic-after=${arg#--panic-after=}" ;;
        --no-monitor)
            SERIAL_ARGS=(-serial stdio -monitor none)
            EXIT_HINT="poweroff from inside, or kill qemu from another terminal"
            ;;
        --help|-h)
            grep -E '^# ' "$0" | sed 's/^# //'
            exit 0 ;;
        *)
            echo "Unknown option: $arg (see --help)" >&2
            exit 1 ;;
    esac
done

if [ ! -f "${KERNEL}" ] || [ ! -f "${INITRAMFS}" ]; then
    echo "Error: Build artifacts not found. Run ./build.sh first."
    exit 1
fi

# Detect KVM support
KVM_ARGS=""
if [ -w /dev/kvm ] 2>/dev/null; then
    KVM_ARGS="-enable-kvm -cpu host"
else
    echo "Note: KVM not available, using software emulation (slower)"
    KVM_ARGS="-cpu qemu64"
fi

echo "Starting slinit QEMU demo (${EXIT_HINT})"
echo "kernel cmdline: ${APPEND}"
echo ""

exec qemu-system-x86_64 \
    ${KVM_ARGS} \
    -kernel "${KERNEL}" \
    -initrd "${INITRAMFS}" \
    -append "${APPEND}" \
    -m "${MEMORY}" \
    -nographic \
    -no-reboot \
    "${SERIAL_ARGS[@]}" \
    -nic none \
    -device i6300esb
