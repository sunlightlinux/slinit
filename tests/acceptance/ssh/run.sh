#!/bin/bash
# run.sh — orchestrate acceptance tests against a remote slinit install over SSH.
#
# Required env: ACCEPTANCE_HOST, ACCEPTANCE_PORT, ACCEPTANCE_USER
# Optional env: ACCEPTANCE_SSH_KEY, VERBOSE=1, KEEP_REMOTE=1
#
# Usage:
#   ACCEPTANCE_HOST=... ACCEPTANCE_PORT=... ACCEPTANCE_USER=... ./run.sh
#   ./run.sh cases/04-start-stop.sh    # subset
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
    echo "  ACCEPTANCE_HOST=ceres.example.org \\" >&2
    echo "  ACCEPTANCE_PORT=40003 \\" >&2
    echo "  ACCEPTANCE_USER=root \\" >&2
    echo "  $0" >&2
    exit 2
fi

VERBOSE="${VERBOSE:-0}"
KEEP_REMOTE="${KEEP_REMOTE:-0}"

# ---- colors -------------------------------------------------------------
if [ -t 1 ]; then
    GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'
    BOLD='\033[1m'; RESET='\033[0m'
else
    GREEN=''; RED=''; YELLOW=''; BOLD=''; RESET=''
fi

# ---- ssh helpers --------------------------------------------------------
# shellcheck source=lib/ssh.sh
. "${LIB_DIR}/ssh.sh"

# ---- prerequisites ------------------------------------------------------
echo -e "${BOLD}Probing ${REMOTE}:${ACCEPTANCE_PORT}...${RESET}"
if ! ssh_run 'test -S /run/slinit.socket && slinitctl --version'; then
    echo "ERROR: ssh probe failed or slinit not running on target" >&2
    exit 2
fi

REMOTE_VERSION=$(ssh_run 'slinitctl --version' | awk '{print $NF}')
echo -e "${BOLD}Target slinit version:${RESET} ${REMOTE_VERSION}"
echo ""

# ---- case discovery -----------------------------------------------------
if [ "$#" -gt 0 ]; then
    CASES=("$@")
else
    mapfile -t CASES < <(find "${CASES_DIR}" -maxdepth 1 -name '*.sh' -type f | sort)
fi

if [ "${#CASES[@]}" -eq 0 ]; then
    echo "No cases found." >&2
    exit 2
fi

# ---- remote scratch dir -------------------------------------------------
REMOTE_DIR="/tmp/slinit-acceptance.$$"
ssh_run "mkdir -p ${REMOTE_DIR}"
scp_to "${LIB_DIR}/remote-prelude.sh" "${REMOTE_DIR}/remote-prelude.sh"

cleanup_remote() {
    if [ "$KEEP_REMOTE" = "1" ]; then
        echo "KEEP_REMOTE=1; leaving ${REMOTE_DIR} on target."
    else
        ssh_run "rm -rf ${REMOTE_DIR}" || true
    fi
}
trap cleanup_remote EXIT

# ---- runner -------------------------------------------------------------
PASSED=0
FAILED=0
declare -a FAILED_NAMES=()

run_case() {
    local case_path="$1"
    local case_name
    case_name="$(basename "$case_path" .sh)"

    scp_to "$case_path" "${REMOTE_DIR}/${case_name}.sh"
    # Wrap the case execution so it sources the prelude and tees output.
    # We use bash on the remote so [[ etc work if the cases use them.
    local out
    local rc
    out="$(ssh_run "cd ${REMOTE_DIR} && sh -c '. ./remote-prelude.sh && . ./${case_name}.sh'" 2>&1)" \
        && rc=0 || rc=$?

    # A case passes iff rc==0 AND no 'FAIL:' line slipped through.
    if [ "$rc" -eq 0 ] && ! grep -q '^FAIL:' <<<"$out"; then
        echo -e "  ${GREEN}PASS${RESET} ${case_name}"
        PASSED=$((PASSED + 1))
        if [ "$VERBOSE" = "1" ]; then
            sed 's/^/    /' <<<"$out"
        fi
    else
        echo -e "  ${RED}FAIL${RESET} ${case_name} (rc=$rc)"
        FAILED=$((FAILED + 1))
        FAILED_NAMES+=("$case_name")
        sed 's/^/    /' <<<"$out"
    fi
}

for case_path in "${CASES[@]}"; do
    if [ ! -f "$case_path" ]; then
        echo -e "  ${YELLOW}SKIP${RESET} $case_path (not a file)"
        continue
    fi
    run_case "$case_path"
done

# ---- summary ------------------------------------------------------------
echo ""
echo -e "${BOLD}=== Acceptance suite ===${RESET}"
echo -e "  Target:  ${REMOTE}:${ACCEPTANCE_PORT} (slinit ${REMOTE_VERSION})"
echo -e "  Passed:  ${GREEN}${PASSED}${RESET}"
echo -e "  Failed:  ${RED}${FAILED}${RESET}"

if [ "$FAILED" -gt 0 ]; then
    echo ""
    echo "Failed cases:"
    for n in "${FAILED_NAMES[@]}"; do
        echo "  - $n"
    done
    exit 1
fi
exit 0
