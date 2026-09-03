#!/bin/sh
# Send SIGTERM to the container's slinit and verify the registry
# entry is cleaned up.

set -eu

cd "$(dirname "$0")"

NAME="${NAME:-alpine-demo}"
REGISTRY="${SLINIT_MACHINES_DIR:-/run/slinit/machines}/${NAME}"

if [ ! -f "${REGISTRY}" ]; then
    echo "→ no registry entry at ${REGISTRY} — is the container running?"
    exit 1
fi

PID=$(head -n1 "${REGISTRY}")
echo "→ sending SIGTERM to container ${NAME} (host PID ${PID})"
kill -TERM "${PID}"

# Poll for exit — bounded, tolerant of a slow shutdown chain inside.
for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [ ! -f "${REGISTRY}" ]; then
        echo "→ registry entry gone; container shut down cleanly"
        exit 0
    fi
    sleep 1
done

echo "→ registry entry ${REGISTRY} still present after 10s" >&2
echo "  container may be stuck; check host PID ${PID}" >&2
exit 1
