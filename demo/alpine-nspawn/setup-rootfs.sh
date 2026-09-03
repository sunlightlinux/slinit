#!/bin/sh
# Install slinit + demo services into rootfs/. Assumes fetch-rootfs.sh
# has already extracted an Alpine minirootfs.

set -eu

cd "$(dirname "$0")"

if [ ! -e rootfs/etc/alpine-release ]; then
    echo "rootfs/ missing — run ./fetch-rootfs.sh first" >&2
    exit 1
fi

# Build slinit STATICALLY into demo-scoped output so it runs against
# Alpine's musl. A cgo-linked glibc binary hits `execve` ENOENT
# because Alpine has /lib/ld-musl-* not /lib64/ld-linux-*, which is
# indistinguishable from "binary missing" in the error message.
# CGO_ENABLED=0 forces the Go net + os path to use pure-Go, so slinit
# becomes a self-contained ELF with no interpreter requirement.
STATIC_BIN="_build/slinit"
mkdir -p _build
echo "→ building static slinit (CGO_ENABLED=0) into ${STATIC_BIN}"
(cd ../.. && CGO_ENABLED=0 go build -o "demo/alpine-nspawn/${STATIC_BIN}" ./cmd/slinit)
SLINIT_BIN="$(readlink -f "${STATIC_BIN}")"

# Sanity check: a fully static binary reports "statically linked" or
# "not a dynamic executable" from ldd/file. Warn (don't fail) so an
# operator with an unusual toolchain can still proceed.
if file "${SLINIT_BIN}" 2>/dev/null | grep -q "dynamically linked"; then
    echo "→ WARNING: ${SLINIT_BIN} is dynamically linked" >&2
    echo "  Alpine's musl won't resolve glibc interpreters — expect execve ENOENT." >&2
    echo "  Rebuild with: CGO_ENABLED=0 go build -o slinit ./cmd/slinit" >&2
fi
echo "→ using slinit binary: ${SLINIT_BIN}"

# Install into rootfs/sbin/slinit. Overwrite so a re-run picks up
# updated code.
install -D -m 0755 "${SLINIT_BIN}" rootfs/sbin/slinit

# Services directory + individual service files.
install -d -m 0755 rootfs/etc/slinit.d
for svc in services/*; do
    [ -f "${svc}" ] || continue
    install -m 0644 "${svc}" "rootfs/etc/slinit.d/$(basename "${svc}")"
done

# Ensure /run and /var/log/slinit-journal directories exist so the
# container's slinit-journald can write immediately (Phase 3 daemon
# handles this on its own, but pre-creating avoids first-boot ENOENT
# noise). /var/log/slinit hosts per-service logfiles that LogRotator
# writes to when log-type=file is set — pre-created so services don't
# fail their first write.
install -d -m 0755 rootfs/run rootfs/var/log/slinit-journal rootfs/var/log/slinit

echo "→ services installed:"
ls rootfs/etc/slinit.d/ | sed 's/^/    /'
echo "→ done. Next: sudo ./run.sh"
