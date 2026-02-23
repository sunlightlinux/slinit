#!/bin/bash
# run.sh - Launch slinit demo in QEMU
# Exit VM: Ctrl+A, X  |  Clean shutdown: slinitctl shutdown reboot
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/_output"
KERNEL="${OUTPUT_DIR}/vmlinuz-virt"
INITRAMFS="${OUTPUT_DIR}/initramfs.cpio.gz"
MEMORY="${MEMORY:-256}"

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

echo "Starting slinit QEMU demo (Ctrl+A, X to exit)"
echo ""

exec qemu-system-x86_64 \
    ${KVM_ARGS} \
    -kernel "${KERNEL}" \
    -initrd "${INITRAMFS}" \
    -append "console=ttyS0 rdinit=/sbin/init loglevel=4" \
    -m "${MEMORY}" \
    -nographic \
    -no-reboot \
    -serial mon:stdio
