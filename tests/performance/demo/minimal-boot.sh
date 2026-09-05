#!/bin/bash
# minimal-boot.sh — single-service cold-boot benchmark. Boots slinit
# with a stripped-down boot target: only `system-init` (mount /proc
# + /sys + cgroup v2) plus the `perf-collect` measurement service.
# Every other file under demo/services/ stays on disk but is
# unreferenced by boot's deps, so slinit never starts it.
#
# Matches the shape of the systemd-alternatives comparison
# literature ("systemd 1.8s, dinit 0.7s, runit 0.4s single-SSH cold
# boot") so slinit's number is directly comparable.
#
# Setup:
#   - Copies perf-collect into demo/services/, rewriting its
#     dependency from `all-services` (default, for full-demo boot)
#     to `system-init` (minimal boot has no all-services aggregate).
#   - Replaces demo/services/boot with a minimal variant:
#         type=internal; depends-on: system-init; waits-for: perf-collect
#   - Rebuilds initramfs via demo/build.sh.
#   - Runs QEMU headless N times, captures serial stdout, parses.
#   - Restores demo/services/boot + drops the injected perf-collect
#     on exit (trap).
#
# Usage:
#   tests/performance/demo/minimal-boot.sh [ITERATIONS]     (default 5)
#
# Requires: qemu-system-x86_64, awk, sed, sort.

set -euo pipefail

ITERATIONS="${1:-5}"

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=_lib.sh
. "${SELF_DIR}/_lib.sh"

REPO_ROOT="$(cd "${SELF_DIR}/../../.." && pwd)"
DEMO_DIR="${REPO_ROOT}/demo"
SVC_DIR="${DEMO_DIR}/services"
BOOT_SVC="${SVC_DIR}/boot"

if [ ! -f "${DEMO_DIR}/build.sh" ] || [ ! -f "${DEMO_DIR}/run.sh" ]; then
    echo "minimal-boot: expected demo/build.sh + demo/run.sh at ${DEMO_DIR}" >&2
    exit 2
fi

# Backup + inject. Trap restores unconditionally so a Ctrl-C mid-run
# leaves the demo tree unchanged.
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

echo "→ injecting perf-collect (dep: system-init) into ${SVC_DIR}/"
perf_write_collector "${SVC_DIR}/perf-collect" "system-init"

_boot_backup="${BOOT_SVC}.minimal-boot-bak"
cp "${BOOT_SVC}" "${_boot_backup}"
cat > "${BOOT_SVC}" <<'EOF'
# boot -- replaced by tests/performance/demo/minimal-boot.sh for the
# duration of this benchmark run. Original saved next to it as
# boot.minimal-boot-bak; the trap in minimal-boot.sh restores it.
type = internal
depends-on: system-init
waits-for: perf-collect
EOF

echo "→ rebuilding initramfs (demo/build.sh)"
(cd "${DEMO_DIR}" && ./build.sh >/dev/null 2>&1) || {
    echo "minimal-boot: demo/build.sh failed" >&2
    exit 3
}

echo "→ running ${ITERATIONS} iterations..."
_boot_samples=(); _rss_samples=(); _peak_samples=(); _bin_bytes=0
for i in $(seq 1 "${ITERATIONS}"); do
    _log=$(mktemp)
    # Minimal boot is <1s; 30s covers a stuck boot with headroom.
    timeout 30s bash -c "cd '${DEMO_DIR}' && ./run.sh --no-monitor </dev/null >'${_log}' 2>&1" || true

    _block=$(perf_extract_block "${_log}")
    if [ -z "${_block}" ] || ! grep -q "PERF-END" <<<"${_block}"; then
        _keep="/tmp/minimal-boot-iter${i}-$$.log"
        mv "${_log}" "${_keep}"
        echo "  iter $i: FAIL (no PERF-BEGIN/END block) — log saved: ${_keep}"
        continue
    fi
    rm -f "${_log}"

    eval "$(perf_parse_block "${_block}")"
    if [ -z "${BOOT_NS}" ] || [ "${BOOT_NS}" = "0" ] || [ -z "${RSS_KB}" ]; then
        _keep="/tmp/minimal-boot-iter${i}-$$.log"
        printf "%s\n" "${_block}" > "${_keep}"
        echo "  iter $i: FAIL (parse: boot=${BOOT_NS} rss=${RSS_KB} up=${UPTIME_LINE}) — block saved: ${_keep}"
        continue
    fi

    _boot_ms=$(awk -v b="${BOOT_NS}" 'BEGIN{printf "%.1f", b/1e6}')
    echo "  iter $i: boot=${_boot_ms}ms rss=${RSS_KB}kB peak=${VMPEAK_KB}kB threads=${THREADS} fds=${FDS}"
    _boot_samples+=("${BOOT_NS}")
    _rss_samples+=("${RSS_KB}")
    _peak_samples+=("${VMPEAK_KB}")
    _bin_bytes="${SLINIT_BYTES}"
done

if [ "${#_boot_samples[@]}" -eq 0 ]; then
    echo "minimal-boot: no successful iterations, aborting" >&2
    exit 4
fi

perf_summary "Minimal" "${#_boot_samples[@]}" "${ITERATIONS}" \
    "$(perf_median "${_boot_samples[@]}")" "$(perf_p95 "${_boot_samples[@]}")" \
    "$(perf_median "${_rss_samples[@]}")" "$(perf_median "${_peak_samples[@]}")" \
    "${_bin_bytes}"
