#!/bin/bash
# cold-boot.sh — measure slinit cold-boot time + PID-1 footprint under
# the demo/QEMU harness. Runs N iterations, extracts a PERF-METRICS
# line emitted by the tests/performance/demo/perf-collect service,
# and prints benchstat-compatible summary lines suitable for
# docs/PERFORMANCE.md and cross-commit diffs.
#
# Setup:
#   - Copies perf-collect into demo/services/ and adds it to
#     all-services.d/ so it auto-runs on every boot in this session.
#   - Rebuilds initramfs via demo/build.sh.
#   - Runs QEMU headless N times, captures serial stdout, greps for
#     "PERF-METRICS" line, parses fields.
#   - Restores demo/services/ + all-services.d/ on exit (trap).
#
# Usage:
#   tests/performance/demo/cold-boot.sh [ITERATIONS]      (default 5)
#
# Requires: qemu-system-x86_64, awk, sort, printf.

set -euo pipefail

ITERATIONS="${1:-5}"

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
DEMO_DIR="${REPO_ROOT}/demo"
SVC_DIR="${DEMO_DIR}/services"
LINK_DIR="${SVC_DIR}/all-services.d"
PERF_SRC="${REPO_ROOT}/tests/performance/demo/perf-collect"

if [ ! -f "${PERF_SRC}" ]; then
    echo "cold-boot: missing ${PERF_SRC}" >&2
    exit 2
fi
if [ ! -f "${DEMO_DIR}/build.sh" ] || [ ! -f "${DEMO_DIR}/run.sh" ]; then
    echo "cold-boot: expected demo/build.sh + demo/run.sh at ${DEMO_DIR}" >&2
    exit 2
fi

# Backup + inject. Trap restores unconditionally so a Ctrl-C mid-run
# leaves the demo tree unchanged.
_backup_svc=""
_backup_link=""
cleanup() {
    if [ -n "${_backup_svc}" ] && [ -f "${SVC_DIR}/perf-collect" ]; then
        rm -f "${SVC_DIR}/perf-collect"
    fi
    if [ -n "${_backup_link}" ] && [ -L "${LINK_DIR}/perf-collect" ]; then
        rm -f "${LINK_DIR}/perf-collect"
    fi
}
trap cleanup EXIT INT TERM

echo "→ injecting perf-collect into ${SVC_DIR}/"
cp "${PERF_SRC}" "${SVC_DIR}/perf-collect"
_backup_svc="yes"
ln -sf "../perf-collect" "${LINK_DIR}/perf-collect"
_backup_link="yes"

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
for i in $(seq 1 "${ITERATIONS}"); do
    _log=$(mktemp)
    # QEMU exits on poweroff (-no-reboot). Timeout 30s protects
    # against a stuck boot never printing the marker.
    if ! timeout 30s bash -c "cd '${DEMO_DIR}' && ./run.sh --no-monitor </dev/null >'${_log}' 2>&1"; then
        # timeout / non-zero exit — still parse the log for a marker
        :
    fi

    _line=$(grep -m1 "^PERF-METRICS" "${_log}" || true)
    rm -f "${_log}"

    if [ -z "${_line}" ]; then
        echo "  iter $i: FAIL (no PERF-METRICS line)"
        continue
    fi

    # Parse key=value pairs. Fields are integers, space-separated.
    _boot=$(echo "${_line}" | awk '{for(i=1;i<=NF;i++){split($i,a,"="); if(a[1]=="boot_ns") print a[2]}}')
    _rss=$(echo "${_line}" | awk '{for(i=1;i<=NF;i++){split($i,a,"="); if(a[1]=="pid1_rss_kb") print a[2]}}')
    _peak=$(echo "${_line}" | awk '{for(i=1;i<=NF;i++){split($i,a,"="); if(a[1]=="pid1_vmpeak_kb") print a[2]}}')
    _bin=$(echo "${_line}" | awk '{for(i=1;i<=NF;i++){split($i,a,"="); if(a[1]=="slinit_bytes") print a[2]}}')

    if [ -z "${_boot}" ] || [ -z "${_rss}" ]; then
        echo "  iter $i: FAIL (malformed marker: ${_line})"
        continue
    fi

    _boot_ms=$(awk -v b="${_boot}" 'BEGIN{printf "%.1f", b/1e6}')
    echo "  iter $i: boot=${_boot_ms}ms rss=${_rss}kB peak=${_peak}kB"
    _boot_samples+=("${_boot}")
    _rss_samples+=("${_rss}")
    _peak_samples+=("${_peak}")
    _bin_bytes="${_bin}"
done

if [ "${#_boot_samples[@]}" -eq 0 ]; then
    echo "cold-boot: no successful iterations, aborting" >&2
    exit 4
fi

# Median = middle element of sorted array. p95 = 95th-percentile
# index (nearest-rank). Uses awk for the math.
median() {
    printf "%s\n" "$@" | sort -n | awk 'BEGIN{c=0} {a[c++]=$1} END{
        if (c%2) print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2
    }'
}
p95() {
    printf "%s\n" "$@" | sort -n | awk 'BEGIN{c=0} {a[c++]=$1} END{
        i=int(0.95*(c-1)+0.5); print a[i]
    }'
}

_boot_med=$(median "${_boot_samples[@]}")
_boot_p95=$(p95 "${_boot_samples[@]}")
_rss_med=$(median "${_rss_samples[@]}")
_peak_med=$(median "${_peak_samples[@]}")

_boot_med_ms=$(awk -v b="${_boot_med}" 'BEGIN{printf "%.1f", b/1e6}')
_boot_p95_ms=$(awk -v b="${_boot_p95}" 'BEGIN{printf "%.1f", b/1e6}')
_bin_kb=$(awk -v b="${_bin_bytes}" 'BEGIN{printf "%.0f", b/1024}')

echo
echo "=== cold-boot summary (n=${#_boot_samples[@]}/${ITERATIONS}) ==="
printf "BenchmarkColdBoot_Demo         %3d   %s ms (p95 %s ms)\n" \
    "${#_boot_samples[@]}" "${_boot_med_ms}" "${_boot_p95_ms}"
printf "BenchmarkPID1RSS_Demo          %3d   %d kB\n" \
    "${#_rss_samples[@]}" "${_rss_med}"
printf "BenchmarkPID1VmPeak_Demo       %3d   %d kB\n" \
    "${#_peak_samples[@]}" "${_peak_med}"
printf "BenchmarkSlinitBinarySize        1   %d kB (%d bytes)\n" \
    "${_bin_kb}" "${_bin_bytes}"
