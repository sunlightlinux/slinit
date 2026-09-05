#!/bin/bash
# fork-exec-throughput.sh — measure slinit's end-to-end throughput
# bringing up N minimal `type=scripted, command=/bin/true` services
# in parallel. Isolates the fork+exec+wait cost (kernel scheduler +
# slinit's SIGCHLD reaper + state-machine transitions) from the
# ServiceSet microbenchmarks in ../runtime/, which measure the
# state-machine alone with no exec.
#
# Setup:
#   - Generates mock-0001..mock-NNNN service files with a
#     `command = /bin/true` line each into demo/services/.
#   - Generates a `mock-all` aggregate: type=internal,
#     waits-for.d = mock-all.d/ — with a symlink to each mock.
#   - Injects perf-collect with depends-on: mock-all.
#   - Replaces demo/services/boot with a minimal variant that
#     depends only on system-init and waits-for perf-collect (mock
#     tree pulled in via perf-collect's dep).
#   - Rebuilds initramfs, runs QEMU N iterations, parses UPTIME.
#   - Trap restores demo tree.
#
# Usage:
#   tests/performance/demo/fork-exec-throughput.sh [NUM_SVCS] [ITERS]
#   defaults: 50 mock services, 5 iterations.
#
# Requires: qemu-system-x86_64, awk, sed, sort.

set -euo pipefail

NUM_SVCS="${1:-50}"
ITERATIONS="${2:-5}"

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=_lib.sh
. "${SELF_DIR}/_lib.sh"

REPO_ROOT="$(cd "${SELF_DIR}/../../.." && pwd)"
DEMO_DIR="${REPO_ROOT}/demo"
SVC_DIR="${DEMO_DIR}/services"
BOOT_SVC="${SVC_DIR}/boot"
MOCK_D="${SVC_DIR}/mock-all.d"

if [ ! -f "${DEMO_DIR}/build.sh" ] || [ ! -f "${DEMO_DIR}/run.sh" ]; then
    echo "fork-exec-throughput: expected demo/build.sh + demo/run.sh at ${DEMO_DIR}" >&2
    exit 2
fi

_boot_backup=""
cleanup() {
    if [ -n "${_boot_backup}" ] && [ -f "${_boot_backup}" ]; then
        mv -f "${_boot_backup}" "${BOOT_SVC}"
    fi
    if [ -f "${SVC_DIR}/perf-collect" ]; then
        rm -f "${SVC_DIR}/perf-collect"
    fi
    if [ -f "${SVC_DIR}/mock-all" ]; then
        rm -f "${SVC_DIR}/mock-all"
    fi
    if [ -d "${MOCK_D}" ]; then
        rm -rf "${MOCK_D}"
    fi
    if compgen -G "${SVC_DIR}/mock-*" > /dev/null; then
        rm -f "${SVC_DIR}"/mock-[0-9]*
    fi
}
trap cleanup EXIT INT TERM

echo "→ generating ${NUM_SVCS} mock services in ${SVC_DIR}/"
mkdir -p "${MOCK_D}"
for n in $(seq -f "%04g" 1 "${NUM_SVCS}"); do
    cat > "${SVC_DIR}/mock-${n}" <<EOF
type = scripted
command = /bin/true
EOF
    ln -sf "../mock-${n}" "${MOCK_D}/mock-${n}"
done

cat > "${SVC_DIR}/mock-all" <<'EOF'
# mock-all -- aggregate for fork-exec-throughput.sh benchmark.
# Restored by the harness's trap.
type = internal
depends-on: system-init
waits-for.d: mock-all.d
EOF

echo "→ injecting perf-collect (dep: mock-all) into ${SVC_DIR}/"
perf_write_collector "${SVC_DIR}/perf-collect" "mock-all"

_boot_backup="${BOOT_SVC}.fork-exec-bak"
cp "${BOOT_SVC}" "${_boot_backup}"
cat > "${BOOT_SVC}" <<'EOF'
# boot -- replaced by tests/performance/demo/fork-exec-throughput.sh.
# Restored by the harness's trap on exit.
type = internal
depends-on: system-init
waits-for: perf-collect
EOF

echo "→ rebuilding initramfs (demo/build.sh)"
(cd "${DEMO_DIR}" && ./build.sh >/dev/null 2>&1) || {
    echo "fork-exec-throughput: demo/build.sh failed" >&2
    exit 3
}

echo "→ running ${ITERATIONS} iterations (N=${NUM_SVCS})..."
_boot_samples=()
for i in $(seq 1 "${ITERATIONS}"); do
    _log=$(mktemp)
    # Larger N -> longer boot; scale timeout by (5 + 0.1*N) sec, cap at 120.
    _tmo=$(awk -v n="${NUM_SVCS}" 'BEGIN{t=5+0.1*n; if(t>120)t=120; printf "%d",t}')
    timeout "${_tmo}s" bash -c "cd '${DEMO_DIR}' && ./run.sh --no-monitor </dev/null >'${_log}' 2>&1" || true

    _block=$(perf_extract_block "${_log}")
    if [ -z "${_block}" ] || ! grep -q "PERF-END" <<<"${_block}"; then
        _keep="/tmp/fork-exec-iter${i}-$$.log"
        mv "${_log}" "${_keep}"
        echo "  iter $i: FAIL (no PERF-BEGIN/END block) — log saved: ${_keep}"
        continue
    fi
    rm -f "${_log}"

    eval "$(perf_parse_block "${_block}")"
    if [ -z "${BOOT_NS}" ] || [ "${BOOT_NS}" = "0" ]; then
        _keep="/tmp/fork-exec-iter${i}-$$.log"
        printf "%s\n" "${_block}" > "${_keep}"
        echo "  iter $i: FAIL (parse: boot=${BOOT_NS}) — block saved: ${_keep}"
        continue
    fi

    _boot_ms=$(awk -v b="${BOOT_NS}" 'BEGIN{printf "%.1f", b/1e6}')
    echo "  iter $i: boot=${_boot_ms}ms"
    _boot_samples+=("${BOOT_NS}")
done

if [ "${#_boot_samples[@]}" -eq 0 ]; then
    echo "fork-exec-throughput: no successful iterations, aborting" >&2
    exit 4
fi

_boot_med=$(perf_median "${_boot_samples[@]}")
_boot_p95=$(perf_p95    "${_boot_samples[@]}")
_boot_med_ms=$(awk -v b="${_boot_med}" 'BEGIN{printf "%.1f", b/1e6}')
_boot_p95_ms=$(awk -v b="${_boot_p95}" 'BEGIN{printf "%.1f", b/1e6}')
# Total boot = kernel + slinit-init + N * fork-exec. Subtract the
# minimal-boot baseline (system-init-only) to isolate the per-service
# cost. Baseline is nominal ~800ms; adjust if the machine has a
# significantly different kernel-boot time. Throughput = 1 / per_svc.
_baseline_ns=800000000
_delta_ns=$(awk -v m="${_boot_med}" -v b="${_baseline_ns}" \
    'BEGIN{d=m-b; if(d<0)d=0; printf "%d", d}')
_per_svc_us=$(awk -v d="${_delta_ns}" -v n="${NUM_SVCS}" \
    'BEGIN{printf "%.0f", (d/1000) / n}')
_thr_pure=$(awk -v d="${_delta_ns}" -v n="${NUM_SVCS}" \
    'BEGIN{if(d==0){print "inf"; exit} printf "%.0f", n / (d/1e9)}')

echo
echo "=== fork-exec-throughput summary (n=${#_boot_samples[@]}/${ITERATIONS}, N=${NUM_SVCS}) ==="
printf "BenchmarkForkExec_TotalBoot_N%d  %3d   %s ms (p95 %s ms)\n" \
    "${NUM_SVCS}" "${#_boot_samples[@]}" "${_boot_med_ms}" "${_boot_p95_ms}"
printf "BenchmarkForkExec_PerSvc_N%d      %3d   %s us (over %.0f ms baseline)\n" \
    "${NUM_SVCS}" "${#_boot_samples[@]}" "${_per_svc_us}" \
    "$(awk -v b="${_baseline_ns}" 'BEGIN{printf "%.0f", b/1e6}')"
printf "BenchmarkForkExec_PureThroughput_N%d %3d   %s svc/s\n" \
    "${NUM_SVCS}" "${#_boot_samples[@]}" "${_thr_pure}"
