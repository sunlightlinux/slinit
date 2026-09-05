#!/bin/bash
# run.sh — drive slinit performance benchmarks against a live remote
# install over SSH. Same env-var contract as tests/acceptance/ssh/
# so the same target VM (typically ceres) serves both.
#
# Each case runs REMOTELY on the target and measures its own local
# wall-clock via `date +%s%N` — so numbers reflect the control-socket
# / IPC latency of slinit itself, not the SSH round-trip. Bench-
# style lines print to stdout: median + p95 + min in milliseconds.
#
# Required env: ACCEPTANCE_HOST, ACCEPTANCE_PORT, ACCEPTANCE_USER
# Optional env:
#   ACCEPTANCE_SSH_KEY  — explicit identity file
#   VERBOSE=1           — echo each SSH invocation
#   ITERS=N             — iterations per case (default 30)
#   KEEP_REMOTE=1       — leave /tmp/slinit-perf.$$ on target for inspection
#
# Usage:
#   VERBOSE=1 ACCEPTANCE_HOST=ceres.ionutnechita.ro \
#     ACCEPTANCE_PORT=40003 ACCEPTANCE_USER=root ./run.sh
#   ./run.sh cases/10-ctl-status.sh   # subset

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CASES_DIR="${SCRIPT_DIR}/cases"
LIB_DIR="${SCRIPT_DIR}/lib"

# ---- env validation -----------------------------------------------------
missing=()
for v in ACCEPTANCE_HOST ACCEPTANCE_PORT ACCEPTANCE_USER; do
    if [ -z "${!v:-}" ]; then
        missing+=("$v")
    fi
done
if [ ${#missing[@]} -gt 0 ]; then
    echo "ERROR: required env var(s) not set: ${missing[*]}" >&2
    echo "" >&2
    echo "Example:" >&2
    echo "  VERBOSE=1 ACCEPTANCE_HOST=ceres.ionutnechita.ro \\" >&2
    echo "  ACCEPTANCE_PORT=40003 ACCEPTANCE_USER=root \\" >&2
    echo "  $0" >&2
    exit 2
fi

VERBOSE="${VERBOSE:-0}"
ITERS="${ITERS:-30}"
KEEP_REMOTE="${KEEP_REMOTE:-0}"

# ---- colors -------------------------------------------------------------
if [ -t 1 ]; then
    GREEN='\033[0;32m'; YELLOW='\033[0;33m'; BOLD='\033[1m'; RESET='\033[0m'
else
    GREEN=''; YELLOW=''; BOLD=''; RESET=''
fi

# ---- ssh helpers (shared with tests/acceptance/ssh/ via symlink) --------
# shellcheck source=lib/ssh.sh
. "${LIB_DIR}/ssh.sh"

# ---- probe --------------------------------------------------------------
echo -e "${BOLD}Probing ${REMOTE}:${ACCEPTANCE_PORT}...${RESET}"
if ! ssh_run 'test -S /run/slinit.socket && slinitctl --version >/dev/null'; then
    echo "ERROR: ssh probe failed or slinit not running on target" >&2
    exit 3
fi
REMOTE_VERSION=$(ssh_run 'slinitctl --version' | awk '{print $NF}')
echo -e "  target : ${GREEN}${REMOTE}${RESET}  slinit ${GREEN}${REMOTE_VERSION}${RESET}"
echo -e "  iters  : ${ITERS}"

# ---- case selection -----------------------------------------------------
if [ $# -gt 0 ]; then
    CASES=("$@")
else
    mapfile -t CASES < <(find "${CASES_DIR}" -maxdepth 1 -type f -name '*.sh' | sort)
fi

if [ "${#CASES[@]}" -eq 0 ]; then
    echo "No cases found in ${CASES_DIR}" >&2
    exit 2
fi

# ---- remote scratch dir + prelude upload --------------------------------
REMOTE_DIR="/tmp/slinit-perf.$$"
ssh_run "mkdir -p ${REMOTE_DIR}"
scp_to "${LIB_DIR}/remote-prelude.sh" "${REMOTE_DIR}/remote-prelude.sh"

cleanup_remote() {
    if [ "$KEEP_REMOTE" = "1" ]; then
        echo "KEEP_REMOTE=1; leaving ${REMOTE_DIR} on target."
    else
        ssh_run "rm -rf ${REMOTE_DIR}" || true
    fi
    # Chain ssh.sh's mux cleanup so the ControlMaster socket doesn't
    # leak on our exit (bash EXIT trap is single-slot; setting our
    # own overrides ssh.sh's).
    _ssh_mux_cleanup
}
trap cleanup_remote EXIT

# ---- runner -------------------------------------------------------------
echo
for case_path in "${CASES[@]}"; do
    case_name="$(basename "$case_path" .sh)"
    echo -e "${BOLD}--- ${case_name} ---${RESET}"
    scp_to "$case_path" "${REMOTE_DIR}/${case_name}.sh"
    if [ "$VERBOSE" = "1" ]; then
        echo "  (ssh: cd ${REMOTE_DIR} && ITERS=${ITERS} sh -c ...)"
    fi
    ssh_run "cd ${REMOTE_DIR} && ITERS=${ITERS} sh -c '. ./remote-prelude.sh && . ./${case_name}.sh'"
    echo
done

echo -e "${GREEN}done.${RESET}"
