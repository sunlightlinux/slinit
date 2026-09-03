#!/bin/sh
# Fetch Alpine minirootfs into rootfs/ under this directory.
# Idempotent — an existing tarball in _cache/ is reused.

set -eu

cd "$(dirname "$0")"

# Pin a known-good release. Bump manually; the fetch is small enough
# that a bumped version invalidates nothing.
ALPINE_VER="${ALPINE_VER:-3.20.3}"
ALPINE_ARCH="${ALPINE_ARCH:-x86_64}"

TARBALL="alpine-minirootfs-${ALPINE_VER}-${ALPINE_ARCH}.tar.gz"
BRANCH="v${ALPINE_VER%.*}"
URL="https://dl-cdn.alpinelinux.org/alpine/${BRANCH}/releases/${ALPINE_ARCH}/${TARBALL}"

mkdir -p _cache rootfs

if [ ! -f "_cache/${TARBALL}" ]; then
    echo "→ fetch ${URL}"
    curl -fsSL -o "_cache/${TARBALL}" "${URL}"
else
    echo "→ using cached _cache/${TARBALL}"
fi

if [ -e rootfs/etc/alpine-release ]; then
    echo "→ rootfs/ already extracted (etc/alpine-release exists); skipping"
    echo "  remove it explicitly if you want to re-extract"
    exit 0
fi

echo "→ extract into rootfs/"
tar -C rootfs -xf "_cache/${TARBALL}"

echo "→ done. Next: ./setup-rootfs.sh"
