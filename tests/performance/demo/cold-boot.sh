#!/bin/bash
# cold-boot.sh — full-demo cold-boot benchmark. Measures slinit boot
# time + PID-1 footprint after every one of the 34 demo/services/*
# workloads has reached STARTED. Runs N QEMU iterations, extracts a
# PERF-BEGIN..PERF-END block from serial stdout, prints
# benchstat-compatible summary lines for docs/PERFORMANCE.md.
#
# Setup:
#   - Copies perf-collect into demo/services/.
#   - Appends `waits-for: perf-collect` to demo/services/boot so the
#     boot aggregate holds STARTED until perf-collect finishes and
#     powers off.
#   - Rebuilds initramfs via demo/build.sh.
#   - Runs QEMU headless N times, captures serial stdout, parses.
#   - Restores demo/services/boot + drops the injected perf-collect
#     on exit (trap).
#
# For a stripped-down "single-service" cold-boot number that matches
# the systemd-alternatives comparison literature, see minimal-boot.sh.
#
# Usage:
#   tests/performance/demo/cold-boot.sh [ITERATIONS]      (default 5)
#
# Requires: qemu-system-x86_64, awk, sort, printf.

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
    echo "cold-boot: expected demo/build.sh + demo/run.sh at ${DEMO_DIR}" >&2
    exit 2
fi

# Backup + inject. Trap restores unconditionally so a Ctrl-C mid-run
# leaves the demo tree unchanged.
#
# Wiring: perf-collect goes into demo/services/ (a plain service
# file), and demo/services/boot gets an appended `waits-for:
# perf-collect` line so `boot` holds STARTED until perf-collect
# finishes and powers off. Cannot symlink into all-services.d/
# because that would make all-services soft-wait for perf-collect
# while perf-collect hard-depends on all-services -- classic cycle.
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

echo "→ injecting perf-collect (dep: all-services) into ${SVC_DIR}/"
perf_write_collector "${SVC_DIR}/perf-collect" "all-services"

_boot_backup="${BOOT_SVC}.perf-collect-bak"
cp "${BOOT_SVC}" "${_boot_backup}"
printf '\n# added by tests/performance/demo/cold-boot.sh\nwaits-for: perf-collect\n' \
    >> "${BOOT_SVC}"

echo "→ rebuilding initramfs (demo/build.sh)"
(cd "${DEMO_DIR}" && ./build.sh >/dev/null 2>&1) || {
    echo "cold-boot: demo/build.sh failed" >&2
    exit 3
}

# Collect samples. Each iteration boots the VM with perf-collect
# baked in, waits for the marker line, kills QEMU, records metrics.
_boot_samples=()
_rss_samples=()
_peak_samples=()
_bin_bytes=0

echo "→ running ${ITERATIONS} iterations..."
_boot_samples=(); _rss_samples=(); _peak_samples=(); _bin_bytes=0
for i in $(seq 1 "${ITERATIONS}"); do
    _log=$(mktemp)
    # QEMU exits on poweroff (-no-reboot). Timeout 60s covers a
    # full demo boot (~3s median) + perf-collect + shutdown drain.
    timeout 60s bash -c "cd '${DEMO_DIR}' && ./run.sh --no-monitor </dev/null >'${_log}' 2>&1" || true

    _block=$(perf_extract_block "${_log}")
    if [ -z "${_block}" ] || ! grep -q "PERF-END" <<<"${_block}"; then
        _keep="/tmp/cold-boot-iter${i}-$$.log"
        mv "${_log}" "${_keep}"
        echo "  iter $i: FAIL (no PERF-BEGIN/END block) — log saved: ${_keep}"
        continue
    fi
    rm -f "${_log}"

    eval "$(perf_parse_block "${_block}")"
    if [ -z "${BOOT_NS}" ] || [ "${BOOT_NS}" = "0" ] || [ -z "${RSS_KB}" ]; then
        _keep="/tmp/cold-boot-iter${i}-$$.log"
        printf "%s\n" "${_block}" > "${_keep}"
        echo "  iter $i: FAIL (parse: boot=${BOOT_NS} rss=${RSS_KB} up=${UPTIME_LINE}) — block saved: ${_keep}"
        continue
    fi

    _boot_ms=$(awk -v b="${BOOT_NS}" 'BEGIN{printf "%.1f", b/1e6}')
    echo "  iter $i: boot=${_boot_ms}ms rss=${RSS_KB}kB peak=${VMPEAK_KB}kB threads=${THREADS} fds=${FDS}"
    # Dump the per-service boot-time breakdown for iterations whose
    # total boot exceeds a spike threshold (default 3000ms, override
    # with COLD_BOOT_SPIKE_MS). Bimodal +1s spikes on the demo were
    # traced this way — dumping the boot-time block for the slow
    # iterations reveals which service accounts for the extra time.
    # Also dumps when VERBOSE=1 regardless of threshold.
    _spike_ms="${COLD_BOOT_SPIKE_MS:-3000}"
    _dump_boot_time="false"
    if [ "${VERBOSE:-0}" = "1" ]; then
        _dump_boot_time="true"
    elif awk -v b="${_boot_ms}" -v t="${_spike_ms}" 'BEGIN{exit !(b > t)}'; then
        _dump_boot_time="true"
    fi
    if [ "${_dump_boot_time}" = "true" ]; then
        _bt_block=$(printf '%s\n' "${_block}" \
            | sed -n '/^BOOT-TIME-BEGIN$/,/^BOOT-TIME-END$/p' \
            | sed '1d;$d')
        if [ -n "${_bt_block}" ]; then
            printf '%s\n' "${_bt_block}" | sed 's/^/      /'
        fi
    fi
    _boot_samples+=("${BOOT_NS}")
    _rss_samples+=("${RSS_KB}")
    _peak_samples+=("${VMPEAK_KB}")
    _bin_bytes="${SLINIT_BYTES}"
done

if [ "${#_boot_samples[@]}" -eq 0 ]; then
    echo "cold-boot: no successful iterations, aborting" >&2
    exit 4
fi

perf_summary "Demo" "${#_boot_samples[@]}" "${ITERATIONS}" \
    "$(perf_median "${_boot_samples[@]}")" "$(perf_p95 "${_boot_samples[@]}")" \
    "$(perf_median "${_rss_samples[@]}")" "$(perf_median "${_peak_samples[@]}")" \
    "${_bin_bytes}"
