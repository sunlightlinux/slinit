#!/bin/bash
# build-vm.sh - Build the QEMU test VM image
# Reuses demo/_cache for Alpine downloads to avoid redundant fetches.
set -euo pipefail

ALPINE_VERSION="3.21"
ALPINE_RELEASE="3.21.6"
ALPINE_ARCH="x86_64"
ALPINE_MIRROR="https://dl-cdn.alpinelinux.org/alpine"
MINIROOTFS_URL="${ALPINE_MIRROR}/v${ALPINE_VERSION}/releases/${ALPINE_ARCH}/alpine-minirootfs-${ALPINE_RELEASE}-${ALPINE_ARCH}.tar.gz"
KERNEL_URL="${ALPINE_MIRROR}/v${ALPINE_VERSION}/releases/${ALPINE_ARCH}/netboot/vmlinuz-virt"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CACHE_DIR="${PROJECT_DIR}/demo/_cache"
BUILD_DIR="${SCRIPT_DIR}/_build"
ROOTFS_DIR="${BUILD_DIR}/rootfs"
OUTPUT_DIR="${SCRIPT_DIR}/_output"

mkdir -p "${CACHE_DIR}" "${BUILD_DIR}" "${OUTPUT_DIR}"

# Download Alpine minirootfs
ROOTFS_TAR="${CACHE_DIR}/alpine-minirootfs-${ALPINE_RELEASE}-${ALPINE_ARCH}.tar.gz"
if [ ! -f "${ROOTFS_TAR}" ]; then
    echo "[1/5] Downloading Alpine minirootfs..."
    curl -fSL -o "${ROOTFS_TAR}.tmp" "${MINIROOTFS_URL}"
    mv "${ROOTFS_TAR}.tmp" "${ROOTFS_TAR}"
else
    echo "[1/5] Using cached Alpine minirootfs"
fi

# Download kernel
KERNEL="${CACHE_DIR}/vmlinuz-virt"
if [ ! -f "${KERNEL}" ]; then
    echo "[2/5] Downloading Alpine virt kernel..."
    curl -fSL -o "${KERNEL}.tmp" "${KERNEL_URL}"
    mv "${KERNEL}.tmp" "${KERNEL}"
else
    echo "[2/5] Using cached kernel"
fi

# Build slinit binaries (static, no CGO for portability)
echo "[3/5] Building slinit binaries (static)..."
cd "${PROJECT_DIR}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit" ./cmd/slinit
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinitctl" ./cmd/slinitctl

# Prepare rootfs
echo "[4/5] Preparing rootfs..."
rm -rf "${ROOTFS_DIR}"
mkdir -p "${ROOTFS_DIR}"
tar xzf "${ROOTFS_TAR}" -C "${ROOTFS_DIR}"

install -m 755 "${BUILD_DIR}/slinit" "${ROOTFS_DIR}/sbin/slinit"
install -m 755 "${BUILD_DIR}/slinitctl" "${ROOTFS_DIR}/usr/bin/slinitctl"
ln -sf slinit "${ROOTFS_DIR}/sbin/init"

mkdir -p "${ROOTFS_DIR}/run" "${ROOTFS_DIR}/dev" "${ROOTFS_DIR}/proc" "${ROOTFS_DIR}/sys"
mkdir -p "${ROOTFS_DIR}/etc/slinit.d"
mkdir -p "${ROOTFS_DIR}/test"

# Install the assertion library into the guest
install -m 755 "${SCRIPT_DIR}/lib/assert.sh" "${ROOTFS_DIR}/test/assert.sh"

# Install the guest-side test runner
install -m 755 "${SCRIPT_DIR}/lib/guest-runner.sh" "${ROOTFS_DIR}/test/guest-runner.sh"

# Create initramfs
echo "[5/5] Creating initramfs..."
cd "${ROOTFS_DIR}"
find . | cpio -o -H newc 2>/dev/null | gzip > "${OUTPUT_DIR}/initramfs-base.cpio.gz"
cp "${KERNEL}" "${OUTPUT_DIR}/vmlinuz-virt"

echo "VM build complete."
