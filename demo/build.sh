#!/bin/bash
# build.sh - Build slinit QEMU demo environment
# Downloads Alpine Linux minirootfs, compiles slinit static binaries,
# and creates a bootable initramfs image.
set -euo pipefail

# Alpine Linux configuration
ALPINE_VERSION="3.21"
ALPINE_RELEASE="3.21.6"
ALPINE_ARCH="x86_64"
ALPINE_MIRROR="https://dl-cdn.alpinelinux.org/alpine"
MINIROOTFS_URL="${ALPINE_MIRROR}/v${ALPINE_VERSION}/releases/${ALPINE_ARCH}/alpine-minirootfs-${ALPINE_RELEASE}-${ALPINE_ARCH}.tar.gz"
KERNEL_URL="${ALPINE_MIRROR}/v${ALPINE_VERSION}/releases/${ALPINE_ARCH}/netboot/vmlinuz-virt"

# Directories
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${SCRIPT_DIR}/_build"
ROOTFS_DIR="${BUILD_DIR}/rootfs"
CACHE_DIR="${SCRIPT_DIR}/_cache"
OUTPUT_DIR="${SCRIPT_DIR}/_output"

mkdir -p "${CACHE_DIR}" "${BUILD_DIR}" "${OUTPUT_DIR}"

# Step 1: Download Alpine minirootfs
ROOTFS_TAR="${CACHE_DIR}/alpine-minirootfs-${ALPINE_RELEASE}-${ALPINE_ARCH}.tar.gz"
if [ ! -f "${ROOTFS_TAR}" ]; then
    echo "[1/6] Downloading Alpine minirootfs ${ALPINE_RELEASE}..."
    curl -fSL -o "${ROOTFS_TAR}.tmp" "${MINIROOTFS_URL}"
    mv "${ROOTFS_TAR}.tmp" "${ROOTFS_TAR}"
else
    echo "[1/6] Using cached Alpine minirootfs"
fi

# Step 2: Download Alpine virt kernel
KERNEL="${CACHE_DIR}/vmlinuz-virt"
if [ ! -f "${KERNEL}" ]; then
    echo "[2/6] Downloading Alpine virt kernel..."
    curl -fSL -o "${KERNEL}.tmp" "${KERNEL_URL}"
    mv "${KERNEL}.tmp" "${KERNEL}"
else
    echo "[2/6] Using cached kernel"
fi

# Step 3: Build slinit and slinitctl (static)
echo "[3/6] Building slinit and slinitctl (static)..."
cd "${PROJECT_DIR}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit" ./cmd/slinit
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinitctl" ./cmd/slinitctl

# Step 4: Prepare rootfs
echo "[4/6] Preparing rootfs..."
rm -rf "${ROOTFS_DIR}"
mkdir -p "${ROOTFS_DIR}"

# Extract Alpine minirootfs
tar xzf "${ROOTFS_TAR}" -C "${ROOTFS_DIR}"

# Install slinit binaries
install -m 755 "${BUILD_DIR}/slinit" "${ROOTFS_DIR}/sbin/slinit"
install -m 755 "${BUILD_DIR}/slinitctl" "${ROOTFS_DIR}/usr/bin/slinitctl"

# Make slinit the init system
ln -sf slinit "${ROOTFS_DIR}/sbin/init"

# Ensure directories exist
mkdir -p "${ROOTFS_DIR}/run"
mkdir -p "${ROOTFS_DIR}/dev"
mkdir -p "${ROOTFS_DIR}/proc"
mkdir -p "${ROOTFS_DIR}/sys"

# Step 5: Install service files
echo "[5/6] Installing service files..."
mkdir -p "${ROOTFS_DIR}/etc/slinit.d"
cp "${SCRIPT_DIR}/services/"* "${ROOTFS_DIR}/etc/slinit.d/"

# Step 6: Create initramfs
echo "[6/6] Creating initramfs..."
cd "${ROOTFS_DIR}"
find . | cpio -o -H newc 2>/dev/null | gzip > "${OUTPUT_DIR}/initramfs.cpio.gz"
cp "${KERNEL}" "${OUTPUT_DIR}/vmlinuz-virt"

echo ""
echo "Build complete!"
echo "  Kernel:    ${OUTPUT_DIR}/vmlinuz-virt"
echo "  Initramfs: ${OUTPUT_DIR}/initramfs.cpio.gz"
echo ""
echo "Run with: ./run.sh"
