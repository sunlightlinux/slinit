#!/bin/sh
# Boot the Alpine container under slinit-nspawn.
# Must run as root (namespaces + pivot_root).

set -eu

cd "$(dirname "$0")"

if [ "$(id -u)" != "0" ]; then
    echo "run.sh must be root — sudo $0" >&2
    exit 1
fi

# Locate slinit-nspawn similarly to setup-rootfs.sh.
NSPAWN="${NSPAWN:-}"
if [ -z "${NSPAWN}" ]; then
    for c in ../../slinit-nspawn ../../cmd/slinit-nspawn/slinit-nspawn; do
        if [ -x "${c}" ]; then
            NSPAWN="$(readlink -f "${c}")"
            break
        fi
    done
fi
if [ -z "${NSPAWN}" ]; then
    echo "→ no prebuilt slinit-nspawn found; running 'go build ./cmd/slinit-nspawn'"
    (cd ../.. && go build -o slinit-nspawn ./cmd/slinit-nspawn)
    NSPAWN="$(readlink -f ../../slinit-nspawn)"
fi

ROOTFS="$(readlink -f rootfs)"
NAME="${NAME:-alpine-demo}"

echo "→ launching container '${NAME}' from ${ROOTFS}"
echo "→ query from host with: slinit-journalctl -M ${NAME} -u ticker -f"
echo ""
exec "${NSPAWN}" --name "${NAME}" --boot "${ROOTFS}" -- --system
