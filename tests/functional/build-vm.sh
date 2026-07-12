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

# Build slinit binaries (static, no CGO for portability).
# The kernel-touching + supervisor helpers are shipped in the VM too so
# their end-to-end behaviour can be exercised against a real /proc/sys,
# binfmt_misc, and process tree — unit tests can't reach those paths.
echo "[3/5] Building slinit binaries (static)..."
cd "${PROJECT_DIR}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit" ./cmd/slinit
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinitctl" ./cmd/slinitctl
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-check" ./cmd/slinit-check
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-monitor" ./cmd/slinit-monitor
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-runner" ./cmd/slinit-runner
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-binfmt" ./cmd/slinit-binfmt
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-sysctl" ./cmd/slinit-sysctl
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-svc-value" ./cmd/slinit-svc-value
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-start-stop-daemon" ./cmd/slinit-start-stop-daemon
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-supervise-daemon" ./cmd/slinit-supervise-daemon
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-fstabinfo" ./cmd/slinit-fstabinfo
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-mountinfo" ./cmd/slinit-mountinfo
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-einfo" ./cmd/slinit-einfo
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-shell-var" ./cmd/slinit-shell-var
# slinit-shutdown: needed for the reboot(8) flag-surface test and any
# case that inspects `slinit-shutdown --help` output. Not exercised via
# a real syscall inside the guest — --help short-circuits early.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-shutdown" ./cmd/slinit-shutdown
# slinit-init-maker / slinit-seedrng: exercised by functional tests
# 133 / 134 (bootable-tree generator, seed rotation).
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-init-maker" ./cmd/slinit-init-maker
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o "${BUILD_DIR}/slinit-seedrng" ./cmd/slinit-seedrng

# Prepare rootfs
echo "[4/5] Preparing rootfs..."
rm -rf "${ROOTFS_DIR}"
mkdir -p "${ROOTFS_DIR}"
tar xzf "${ROOTFS_TAR}" -C "${ROOTFS_DIR}"

install -m 755 "${BUILD_DIR}/slinit" "${ROOTFS_DIR}/sbin/slinit"
install -m 755 "${BUILD_DIR}/slinitctl" "${ROOTFS_DIR}/usr/bin/slinitctl"
install -m 755 "${BUILD_DIR}/slinit-check" "${ROOTFS_DIR}/usr/bin/slinit-check"
install -m 755 "${BUILD_DIR}/slinit-monitor" "${ROOTFS_DIR}/usr/bin/slinit-monitor"
# slinit-runner must sit next to the slinit binary (/sbin): findSlinitRunner
# checks the slinit binary's own directory first. Without it, services
# using apparmor-switch / debug / mlockall silently skip the runner wrap.
install -m 755 "${BUILD_DIR}/slinit-runner" "${ROOTFS_DIR}/sbin/slinit-runner"
install -m 755 "${BUILD_DIR}/slinit-binfmt" "${ROOTFS_DIR}/usr/bin/slinit-binfmt"
install -m 755 "${BUILD_DIR}/slinit-sysctl" "${ROOTFS_DIR}/usr/bin/slinit-sysctl"
install -m 755 "${BUILD_DIR}/slinit-svc-value" "${ROOTFS_DIR}/usr/bin/slinit-svc-value"
install -m 755 "${BUILD_DIR}/slinit-start-stop-daemon" "${ROOTFS_DIR}/usr/bin/slinit-start-stop-daemon"
install -m 755 "${BUILD_DIR}/slinit-supervise-daemon" "${ROOTFS_DIR}/usr/bin/slinit-supervise-daemon"
install -m 755 "${BUILD_DIR}/slinit-fstabinfo" "${ROOTFS_DIR}/usr/bin/slinit-fstabinfo"
install -m 755 "${BUILD_DIR}/slinit-mountinfo" "${ROOTFS_DIR}/usr/bin/slinit-mountinfo"
install -m 755 "${BUILD_DIR}/slinit-einfo" "${ROOTFS_DIR}/usr/bin/slinit-einfo"
install -m 755 "${BUILD_DIR}/slinit-shell-var" "${ROOTFS_DIR}/usr/bin/slinit-shell-var"
install -m 755 "${BUILD_DIR}/slinit-shutdown" "${ROOTFS_DIR}/usr/bin/slinit-shutdown"
install -m 755 "${BUILD_DIR}/slinit-init-maker" "${ROOTFS_DIR}/usr/bin/slinit-init-maker"
install -m 755 "${BUILD_DIR}/slinit-seedrng" "${ROOTFS_DIR}/usr/bin/slinit-seedrng"
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
