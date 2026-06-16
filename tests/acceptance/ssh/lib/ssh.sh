#!/bin/sh
# ssh.sh — host-side ssh/scp helpers. Sourced by run.sh.
# All vars come from ACCEPTANCE_* env. Already validated by run.sh.

# Common ssh/scp flags: BatchMode forces non-interactive (fail if key prompt),
# accept-new auto-trusts unknown hosts (first run) without TOFU prompt.
SSH_OPTS="-p ${ACCEPTANCE_PORT}
          -o BatchMode=yes
          -o ConnectTimeout=10
          -o StrictHostKeyChecking=accept-new
          -o ServerAliveInterval=15
          -o LogLevel=ERROR"

# Optional explicit identity file
if [ -n "${ACCEPTANCE_SSH_KEY:-}" ]; then
    SSH_OPTS="$SSH_OPTS -i $ACCEPTANCE_SSH_KEY"
fi

SCP_OPTS="-P ${ACCEPTANCE_PORT}
          -o BatchMode=yes
          -o ConnectTimeout=10
          -o StrictHostKeyChecking=accept-new
          -o LogLevel=ERROR"

if [ -n "${ACCEPTANCE_SSH_KEY:-}" ]; then
    SCP_OPTS="$SCP_OPTS -i $ACCEPTANCE_SSH_KEY"
fi

REMOTE="${ACCEPTANCE_USER}@${ACCEPTANCE_HOST}"

# ssh_run CMD...
# Run CMD on the remote. Stdin is /dev/null so commands cannot consume the
# orchestrator's stdin by mistake.
ssh_run() {
    # shellcheck disable=SC2086
    ssh $SSH_OPTS "$REMOTE" "$@" < /dev/null
}

# scp_to LOCAL REMOTE_PATH
scp_to() {
    # shellcheck disable=SC2086
    scp $SCP_OPTS "$1" "${REMOTE}:$2" < /dev/null > /dev/null
}

# scp_dir_to LOCAL_DIR REMOTE_DIR
scp_dir_to() {
    # shellcheck disable=SC2086
    scp -r $SCP_OPTS "$1" "${REMOTE}:$2" < /dev/null > /dev/null
}
