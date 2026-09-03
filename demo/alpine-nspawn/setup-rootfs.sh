#!/bin/sh
# Install slinit + demo services into rootfs/. Assumes fetch-rootfs.sh
# has already extracted an Alpine minirootfs.

set -eu

cd "$(dirname "$0")"

if [ ! -e rootfs/etc/alpine-release ]; then
    echo "rootfs/ missing — run ./fetch-rootfs.sh first" >&2
    exit 1
fi

# Locate the slinit binary. Prefer $SLINIT_BIN; fall back to the repo
# root's compile output; last resort, `go build` on the fly.
SLINIT_BIN="${SLINIT_BIN:-}"
if [ -z "${SLINIT_BIN}" ]; then
    for c in ../../slinit ../../cmd/slinit/slinit ../../_build/slinit; do
        if [ -x "${c}" ]; then
            SLINIT_BIN="$(readlink -f "${c}")"
            break
        fi
    done
fi
if [ -z "${SLINIT_BIN}" ]; then
    echo "→ no prebuilt slinit found; running 'go build ./cmd/slinit'"
    (cd ../.. && go build -o slinit ./cmd/slinit)
    SLINIT_BIN="$(readlink -f ../../slinit)"
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
# noise).
install -d -m 0755 rootfs/run rootfs/var/log/slinit-journal

echo "→ services installed:"
ls rootfs/etc/slinit.d/ | sed 's/^/    /'
echo "→ done. Next: sudo ./run.sh"
