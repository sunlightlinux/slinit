#!/bin/bash
# run-tests.sh - Run functional tests in QEMU VMs.
#
# Each test case gets its own VM boot with custom services injected.
# Results are collected via virtio-serial.
#
# Usage:
#   ./run-tests.sh                              # run all tests
#   ./run-tests.sh tests/functional/cases/01-*  # run specific test(s)
#   KEEP_BUILD=1 ./run-tests.sh                 # keep build artifacts
#   VERBOSE=1 ./run-tests.sh                    # show full VM output
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/_output"
BUILD_DIR="${SCRIPT_DIR}/_build"
CASES_DIR="${SCRIPT_DIR}/cases"

TIMEOUT="${TIMEOUT:-60}"
PASSED=0
FAILED=0
SKIPPED=0
TOTAL=0

# Colors (if terminal)
if [ -t 1 ]; then
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    GREEN='' RED='' YELLOW='' BOLD='' RESET=''
fi

log_pass() { echo -e "  ${GREEN}PASS${RESET} $1"; }
log_fail() { echo -e "  ${RED}FAIL${RESET} $1"; }
log_skip() { echo -e "  ${YELLOW}SKIP${RESET} $1"; }

# Check prerequisites
check_prereqs() {
    local missing=0
    for cmd in qemu-system-x86_64 cpio gzip curl; do
        if ! command -v "$cmd" &>/dev/null; then
            echo "Missing required command: $cmd"
            missing=1
        fi
    done
    if [ "$missing" -eq 1 ]; then
        echo "Install missing dependencies and retry."
        exit 1
    fi
}

# Build the base VM image (once)
build_base() {
    if [ -f "${OUTPUT_DIR}/initramfs-base.cpio.gz" ] && [ -f "${OUTPUT_DIR}/vmlinuz-virt" ]; then
        # Rebuild if binaries are stale
        local slinit_src="${PROJECT_DIR}/cmd/slinit/main.go"
        if [ "${OUTPUT_DIR}/initramfs-base.cpio.gz" -nt "$slinit_src" ]; then
            echo "Using cached VM image (pass KEEP_BUILD=0 to force rebuild)"
            return 0
        fi
    fi
    echo "Building base VM image..."
    bash "${SCRIPT_DIR}/build-vm.sh"
}

# Run a single test case.
# $1 = path to test case directory or script
run_test() {
    local test_path="$1"
    local test_name
    test_name="$(basename "$test_path" .sh)"
    local test_dir="${BUILD_DIR}/test-${test_name}"
    local result_file="${test_dir}/result.txt"
    local console_log="${test_dir}/console.log"
    local services_dir="${CASES_DIR}/${test_name}.d"

    TOTAL=$((TOTAL + 1))

    rm -rf "${test_dir}"
    mkdir -p "${test_dir}"

    # Build per-test initramfs overlay:
    # - inject test script as /test/current-test.sh
    # - inject test-specific services from NN-name.d/ if present
    # - inject a "test-runner" service that executes the test
    local overlay_dir="${test_dir}/overlay"
    mkdir -p "${overlay_dir}/test"
    mkdir -p "${overlay_dir}/etc/slinit.d"

    cp "${test_path}" "${overlay_dir}/test/current-test.sh"
    chmod +x "${overlay_dir}/test/current-test.sh"

    # Base services: system-init is always needed
    cat > "${overlay_dir}/etc/slinit.d/system-init" <<'SVC'
type = scripted
command = /bin/sh -c "mount -t proc proc /proc 2>/dev/null; mount -t sysfs sysfs /sys 2>/dev/null; mount -t devtmpfs devtmpfs /dev 2>/dev/null; mkdir -p /dev/pts && mount -t devpts devpts /dev/pts 2>/dev/null; mkdir -p /run"
stop-command = /bin/true
SVC

    # Test runner service: runs after system-init, executes the test.
    # Uses process type so slinit considers it "started" while the script runs.
    cat > "${overlay_dir}/etc/slinit.d/test-runner" <<'SVC'
type = process
command = /bin/sh /test/guest-runner.sh
depends-on: system-init
start-timeout = 55
SVC

    # Boot target: waits-for test-runner so boot can reach STARTED
    # while the test is still running.
    cat > "${overlay_dir}/etc/slinit.d/boot" <<'SVC'
type = internal
depends-on: system-init
waits-for: test-runner
SVC

    # Copy test-specific services if a .d directory exists.
    # These can override the default boot/system-init definitions above.
    if [ -d "${services_dir}" ]; then
        cp "${services_dir}"/* "${overlay_dir}/etc/slinit.d/" 2>/dev/null || true
    fi

    # Create the overlay cpio and concatenate with base
    local overlay_cpio="${test_dir}/overlay.cpio.gz"
    (cd "${overlay_dir}" && find . | cpio -o -H newc 2>/dev/null | gzip) > "${overlay_cpio}"

    # Concatenate base + overlay (cpio archives are appendable)
    cat "${OUTPUT_DIR}/initramfs-base.cpio.gz" "${overlay_cpio}" > "${test_dir}/initramfs.cpio.gz"

    # Create a Unix socket for virtio-serial result channel.
    # Use /tmp to avoid the 108-byte Unix socket path limit.
    local chardev_path
    chardev_path=$(mktemp -u "/tmp/slinit-test-${test_name}-XXXXXX.sock")

    # Detect KVM
    local kvm_args="-cpu qemu64"
    if [ -w /dev/kvm ] 2>/dev/null; then
        kvm_args="-enable-kvm -cpu host"
    fi

    # Launch QEMU in background
    qemu-system-x86_64 \
        ${kvm_args} \
        -kernel "${OUTPUT_DIR}/vmlinuz-virt" \
        -initrd "${test_dir}/initramfs.cpio.gz" \
        -append "console=ttyS0 rdinit=/sbin/init loglevel=3 quiet" \
        -m 256 \
        -nographic \
        -no-reboot \
        -serial file:"${console_log}" \
        -device virtio-serial \
        -chardev socket,id=testresult,path="${chardev_path}",server=on,wait=off \
        -device virtserialport,chardev=testresult,name=test.0 \
        &>"${test_dir}/qemu-stderr.log" &
    local qemu_pid=$!

    # Wait for QEMU socket, then read results
    local elapsed=0
    local got_result=0

    # Read from the virtio-serial socket with timeout
    while [ "$elapsed" -lt "$TIMEOUT" ]; do
        if ! kill -0 "$qemu_pid" 2>/dev/null; then
            break
        fi
        # Try to connect and read from the socket
        if [ -S "${chardev_path}" ]; then
            # Use socat if available, otherwise nc
            if command -v socat &>/dev/null; then
                timeout $((TIMEOUT - elapsed)) socat -u UNIX-CONNECT:"${chardev_path}" STDOUT > "${result_file}" 2>/dev/null && got_result=1 || true
            else
                timeout $((TIMEOUT - elapsed)) nc -U "${chardev_path}" > "${result_file}" 2>/dev/null && got_result=1 || true
            fi
            if [ -s "${result_file}" ]; then
                got_result=1
                break
            fi
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done

    # Give QEMU a moment to shut down cleanly, then kill
    sleep 2
    if kill -0 "$qemu_pid" 2>/dev/null; then
        kill "$qemu_pid" 2>/dev/null || true
        wait "$qemu_pid" 2>/dev/null || true
    else
        wait "$qemu_pid" 2>/dev/null || true
    fi

    # Cleanup temp socket
    rm -f "${chardev_path}"

    # Evaluate results
    if [ "$got_result" -eq 0 ] || [ ! -s "${result_file}" ]; then
        # No result file — check console log for clues
        log_fail "${test_name} (no result received, timeout=${TIMEOUT}s)"
        if [ "${VERBOSE:-0}" = "1" ] && [ -f "${console_log}" ]; then
            echo "    --- console log ---"
            tail -20 "${console_log}" | sed 's/^/    /'
            echo "    ---"
        fi
        FAILED=$((FAILED + 1))
        return 1
    fi

    local result_line
    result_line=$(grep "^TEST_RESULT:" "${result_file}" | tail -1)

    if [ "${VERBOSE:-0}" = "1" ]; then
        echo "    --- test output ---"
        grep -v "^TEST_RESULT:" "${result_file}" | sed 's/^/    /'
        echo "    ---"
    fi

    case "${result_line}" in
        "TEST_RESULT:PASS")
            log_pass "${test_name}"
            PASSED=$((PASSED + 1))
            return 0
            ;;
        *)
            log_fail "${test_name}"
            # Show assertion failures
            grep "^FAIL:" "${result_file}" 2>/dev/null | sed 's/^/    /' || true
            FAILED=$((FAILED + 1))
            return 1
            ;;
    esac
}

# --- Main ---

check_prereqs
build_base

# Determine which tests to run
if [ $# -gt 0 ]; then
    test_cases=("$@")
else
    test_cases=("${CASES_DIR}"/*.sh)
fi

echo ""
echo -e "${BOLD}Running ${#test_cases[@]} functional test(s)${RESET}"
echo ""

for tc in "${test_cases[@]}"; do
    if [ ! -f "$tc" ]; then
        echo "Warning: test case not found: $tc"
        SKIPPED=$((SKIPPED + 1))
        continue
    fi
    run_test "$tc" || true
done

echo ""
echo -e "${BOLD}Results: ${PASSED} passed, ${FAILED} failed, ${SKIPPED} skipped (${TOTAL} total)${RESET}"

# Cleanup unless KEEP_BUILD is set
if [ "${KEEP_BUILD:-0}" != "1" ]; then
    rm -rf "${BUILD_DIR}"
fi

[ "$FAILED" -eq 0 ]
