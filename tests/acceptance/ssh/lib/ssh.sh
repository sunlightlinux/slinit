#!/bin/sh
# ssh.sh — host-side ssh/scp helpers. Sourced by run.sh.
# All vars come from ACCEPTANCE_* env. Already validated by run.sh.

# SSH multiplexing: without ControlMaster, run.sh opens a fresh TCP
# connection for every scp_to + ssh_run — hundreds per suite run. On
# some ISPs/routers that trips per-source rate limiting and later
# connects come back with "Connection refused" even though the target
# sshd is fine. ControlMaster=auto with a persistent control socket
# folds every subsequent call into the single already-established
# session (~1 real connect per suite instead of 300+).
_TMPDIR="${TMPDIR:-/tmp}"
SSH_CTLDIR="${_TMPDIR}/slinit-ssh-mux-$$"
mkdir -p "$SSH_CTLDIR"
chmod 0700 "$SSH_CTLDIR"
# Clean up the control socket + dir on exit. Best-effort so a stuck
# master doesn't strand shell state.
_ssh_mux_cleanup() {
    ssh -O exit -o "ControlPath=${SSH_CTLDIR}/%C" \
        -p "${ACCEPTANCE_PORT}" "${REMOTE}" 2>/dev/null || true
    rm -rf "$SSH_CTLDIR" 2>/dev/null || true
}
trap _ssh_mux_cleanup EXIT

# Common ssh/scp flags: BatchMode forces non-interactive (fail if key prompt),
# accept-new auto-trusts unknown hosts (first run) without TOFU prompt.
SSH_OPTS="-p ${ACCEPTANCE_PORT}
          -o BatchMode=yes
          -o ConnectTimeout=10
          -o StrictHostKeyChecking=accept-new
          -o ServerAliveInterval=15
          -o ControlMaster=auto
          -o ControlPath=${SSH_CTLDIR}/%C
          -o ControlPersist=5m
          -o LogLevel=ERROR"

# Optional explicit identity file
if [ -n "${ACCEPTANCE_SSH_KEY:-}" ]; then
    SSH_OPTS="$SSH_OPTS -i $ACCEPTANCE_SSH_KEY"
fi

SCP_OPTS="-P ${ACCEPTANCE_PORT}
          -o BatchMode=yes
          -o ConnectTimeout=10
          -o StrictHostKeyChecking=accept-new
          -o ControlMaster=auto
          -o ControlPath=${SSH_CTLDIR}/%C
          -o ControlPersist=5m
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
