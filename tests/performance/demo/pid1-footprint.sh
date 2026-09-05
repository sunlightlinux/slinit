#!/bin/bash
# pid1-footprint.sh — measure PID-1 steady-state memory footprint.
# Boots slinit with the minimal service set (system-init only, like
# minimal-boot.sh), then perf-collect sleeps IDLE_SEC before
# reading /proc/1/status so the Go GC settles and any startup
# allocations get released. Answers "what does slinit as PID 1
# actually cost, quiescent?" — the number the comparison
# literature reports as "steady-state RSS".
#
# Compare RSS_Steady vs cold-boot.sh's RSS at the boot-STARTED
# moment to see how much of the initial reading was transient
# startup allocation vs settled resident set.
#
# Setup:
#   - Injects perf-collect with depends-on: system-init AND a
#     leading `sleep IDLE_SEC;` before the status dump.
#   - Replaces demo/services/boot with a minimal variant like
#     minimal-boot.sh does.
#   - Rebuilds initramfs, runs QEMU N iterations, parses.
#   - Trap restores demo tree.
#
# Usage:
#   tests/performance/demo/pid1-footprint.sh [IDLE_SEC] [ITERS]
#   defaults: 10s idle, 3 iterations. IDLE_SEC affects QEMU timeout.
#
# Requires: qemu-system-x86_64, awk, sed, sort.

set -euo pipefail

IDLE_SEC="${1:-10}"
ITERATIONS="${2:-3}"

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=_lib.sh
. "${SELF_DIR}/_lib.sh"

REPO_ROOT="$(cd "${SELF_DIR}/../../.." && pwd)"
DEMO_DIR="${REPO_ROOT}/demo"
SVC_DIR="${DEMO_DIR}/services"
BOOT_SVC="${SVC_DIR}/boot"

if [ ! -f "${DEMO_DIR}/build.sh" ] || [ ! -f "${DEMO_DIR}/run.sh" ]; then
    echo "pid1-footprint: expected demo/build.sh + demo/run.sh at ${DEMO_DIR}" >&2
    exit 2
fi

_boot_backup=""
cleanup() {
    if [ -f "${SVC_DIR}/perf-collect" ]; then
        rm -f "${SVC_DIR}/perf-collect"
    fi
    if [ -n "${_boot_backup}" ] && [ -f "${_boot_backup}" ]; then
        mv -f "${_boot_backup}" "${BOOT_SVC}"
    fi
}
trap cleanup EXIT INT TERM

echo "→ injecting perf-collect (dep: system-init, sleep ${IDLE_SEC}s) into ${SVC_DIR}/"
perf_write_collector "${SVC_DIR}/perf-collect" "system-init" "${IDLE_SEC}"

_boot_backup="${BOOT_SVC}.pid1-footprint-bak"
cp "${BOOT_SVC}" "${_boot_backup}"
cat > "${BOOT_SVC}" <<'EOF'
# boot -- replaced by tests/performance/demo/pid1-footprint.sh.
# Restored by the harness's trap on exit.
type = internal
depends-on: system-init
waits-for: perf-collect
EOF

echo "→ rebuilding initramfs (demo/build.sh)"
(cd "${DEMO_DIR}" && ./build.sh >/dev/null 2>&1) || {
    echo "pid1-footprint: demo/build.sh failed" >&2
    exit 3
}

echo "→ running ${ITERATIONS} iterations..."
_rss_samples=(); _peak_samples=(); _bin_bytes=0
# QEMU timeout: minimal-boot (~1s) + IDLE_SEC + drain + margin.
_tmo=$((30 + IDLE_SEC))
for i in $(seq 1 "${ITERATIONS}"); do
    _log=$(mktemp)
    timeout "${_tmo}s" bash -c "cd '${DEMO_DIR}' && ./run.sh --no-monitor </dev/null >'${_log}' 2>&1" || true

    _block=$(perf_extract_block "${_log}")
    if [ -z "${_block}" ] || ! grep -q "PERF-END" <<<"${_block}"; then
        _keep="/tmp/pid1-footprint-iter${i}-$$.log"
        mv "${_log}" "${_keep}"
        echo "  iter $i: FAIL (no PERF-BEGIN/END block) — log saved: ${_keep}"
        continue
    fi
    rm -f "${_log}"

    eval "$(perf_parse_block "${_block}")"
    if [ -z "${RSS_KB}" ]; then
        _keep="/tmp/pid1-footprint-iter${i}-$$.log"
        printf "%s\n" "${_block}" > "${_keep}"
        echo "  iter $i: FAIL (parse: rss=${RSS_KB}) — block saved: ${_keep}"
        continue
    fi

    echo "  iter $i: rss=${RSS_KB}kB peak=${VMPEAK_KB}kB threads=${THREADS} fds=${FDS}"
    _rss_samples+=("${RSS_KB}")
    _peak_samples+=("${VMPEAK_KB}")
    _bin_bytes="${SLINIT_BYTES}"
done

if [ "${#_rss_samples[@]}" -eq 0 ]; then
    echo "pid1-footprint: no successful iterations, aborting" >&2
    exit 4
fi

_rss_med=$(perf_median "${_rss_samples[@]}")
_peak_med=$(perf_median "${_peak_samples[@]}")
_bin_kb=$(awk -v b="${_bin_bytes}" 'BEGIN{printf "%.0f", b/1024}')

echo
echo "=== pid1-footprint summary (n=${#_rss_samples[@]}/${ITERATIONS}, idle=${IDLE_SEC}s) ==="
printf "BenchmarkPID1RSS_Steady          %3d   %d kB\n" \
    "${#_rss_samples[@]}" "${_rss_med}"
printf "BenchmarkPID1VmPeak_Steady       %3d   %d kB\n" \
    "${#_peak_samples[@]}" "${_peak_med}"
printf "BenchmarkSlinitBinarySize          1   %d kB (%d bytes)\n" \
    "${_bin_kb}" "${_bin_bytes}"
