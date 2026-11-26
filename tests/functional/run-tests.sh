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

# harness_skip NAME REASON — a case the host cannot boot as asked (no
# OVMF, no lts kernel). Reported like a guest-side skip_case, reason
# included, and never as a pass.
harness_skip() {
    log_skip "$1"
    echo "    SKIP: $2"
    SKIPPED=$((SKIPPED + 1))
}

# find_ovmf — set OVMF_CODE and OVMF_VARS to a raw, non-secure-boot,
# non-SMM OVMF pair. SLINIT_OVMF_CODE/SLINIT_OVMF_VARS override; then
# the paths distributions install to (openSUSE, Debian/Ubuntu, Fedora,
# Arch, Gentoo). Secure-boot and SMM builds are left out on purpose:
# they need -machine q35,smm=on and a different boot than the rest of
# the suite.
find_ovmf() {
    if [ -n "${SLINIT_OVMF_CODE:-}" ] && [ -n "${SLINIT_OVMF_VARS:-}" ]; then
        OVMF_CODE="${SLINIT_OVMF_CODE}"
        OVMF_VARS="${SLINIT_OVMF_VARS}"
        [ -r "${OVMF_CODE}" ] && [ -r "${OVMF_VARS}" ]
        return
    fi
    local pair
    for pair in \
        /usr/share/qemu/ovmf-x86_64-4m-code.bin:/usr/share/qemu/ovmf-x86_64-4m-vars.bin \
        /usr/share/qemu/ovmf-x86_64-code.bin:/usr/share/qemu/ovmf-x86_64-vars.bin \
        /usr/share/OVMF/OVMF_CODE_4M.fd:/usr/share/OVMF/OVMF_VARS_4M.fd \
        /usr/share/OVMF/OVMF_CODE.fd:/usr/share/OVMF/OVMF_VARS.fd \
        /usr/share/edk2/ovmf/OVMF_CODE.fd:/usr/share/edk2/ovmf/OVMF_VARS.fd \
        /usr/share/edk2/x64/OVMF_CODE.4m.fd:/usr/share/edk2/x64/OVMF_VARS.4m.fd \
        /usr/share/edk2/x64/OVMF_CODE.fd:/usr/share/edk2/x64/OVMF_VARS.fd \
        /usr/share/edk2-ovmf/x64/OVMF_CODE.fd:/usr/share/edk2-ovmf/x64/OVMF_VARS.fd \
        /usr/share/edk2-ovmf/OVMF_CODE.fd:/usr/share/edk2-ovmf/OVMF_VARS.fd
    do
        if [ -r "${pair%%:*}" ] && [ -r "${pair#*:}" ]; then
            OVMF_CODE="${pair%%:*}"
            OVMF_VARS="${pair#*:}"
            return 0
        fi
    done
    return 1
}

# Build the base VM image (once).
#
# Staleness check: rebuild if anything under cmd/ or pkg/ is newer than
# the cached initramfs. Previously we only watched cmd/slinit/main.go,
# which silently kept stale binaries in the VM when only a pkg/ file
# changed — leading to confusing "fix doesn't work" sessions when in
# fact the new code never reached the VM.
build_base() {
    if [ "${KEEP_BUILD:-1}" = "0" ]; then
        echo "KEEP_BUILD=0: forcing a rebuild"
    elif [ -f "${OUTPUT_DIR}/initramfs-base.cpio.gz" ] && [ -f "${OUTPUT_DIR}/vmlinuz-virt" ]; then
        local newest_src
        # build-vm.sh too, not just the Go sources: it decides which
        # packages land in the image, and a change there used to leave
        # the cache looking fresh while the image was missing whatever
        # had just been added.
        newest_src=$(find "${PROJECT_DIR}/cmd" "${PROJECT_DIR}/pkg" \
            -name '*.go' -type f -newer "${OUTPUT_DIR}/initramfs-base.cpio.gz" \
            -print -quit 2>/dev/null)
        # The project's lib/ too, and not only its Go. build-vm.sh
        # compiles lib/slinit-mlock/slinit-mlock.c into the
        # libslinit-mlock.so that slinit-runner preloads for `mlockall
        # =`, so a change there belongs in the image like any other —
        # and nothing here watched it. 125-mlockall then failed against
        # a cached image with no such library, reporting an empty
        # RLIMIT_MEMLOCK line and an empty VmLck as though the feature
        # were broken, when a rebuild passes. Third directory this check
        # has had to learn about; the pattern is that anything
        # build-vm.sh reads has to be watched here.
        if [ -z "$newest_src" ] && [ -d "${PROJECT_DIR}/lib" ]; then
            newest_src=$(find "${PROJECT_DIR}/lib" -type f \
                -newer "${OUTPUT_DIR}/initramfs-base.cpio.gz" \
                -print -quit 2>/dev/null)
        fi
        if [ -z "$newest_src" ] && [ "${SCRIPT_DIR}/build-vm.sh" -nt "${OUTPUT_DIR}/initramfs-base.cpio.gz" ]; then
            newest_src="tests/functional/build-vm.sh"
        fi
        # lib/ too. assert.sh and guest-runner.sh are installed INTO the
        # image by build-vm.sh (as /test/assert.sh and /test/guest-runner.sh),
        # so editing them and re-running kept the old copies in the VM and
        # the change simply had no effect — the same trap this check was
        # extended for once already, one directory over. It cost a round of
        # "the harness fix did nothing" before being noticed.
        if [ -z "$newest_src" ]; then
            newest_src=$(find "${SCRIPT_DIR}/lib" -type f \
                -newer "${OUTPUT_DIR}/initramfs-base.cpio.gz" -print -quit 2>/dev/null)
        fi
        if [ -z "$newest_src" ]; then
            echo "Using cached VM image (pass KEEP_BUILD=0 to force rebuild)"
            return 0
        fi
        echo "Detected newer source (${newest_src#${PROJECT_DIR}/}), rebuilding..."
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
        cp -r "${services_dir}"/* "${overlay_dir}/etc/slinit.d/" 2>/dev/null || true
    fi

    # Copy init.d scripts if an initd/ subdirectory exists in the test .d dir.
    if [ -d "${services_dir}/initd" ]; then
        mkdir -p "${overlay_dir}/etc/init.d"
        cp -r "${services_dir}/initd"/* "${overlay_dir}/etc/init.d/" 2>/dev/null || true
        # Remove from slinit.d (it was copied there too)
        rm -rf "${overlay_dir}/etc/slinit.d/initd" 2>/dev/null || true
    fi

    # Copy systemd units if a systemd/ subdirectory exists in the test .d
    # dir. They go to /etc/systemd/system, where slinit's fallback looks —
    # copying them into slinit.d would test nothing, since the loader would
    # then find a native description instead of resolving a unit.
    if [ -d "${services_dir}/systemd" ]; then
        mkdir -p "${overlay_dir}/etc/systemd/system"
        cp -r "${services_dir}/systemd"/* "${overlay_dir}/etc/systemd/system/" 2>/dev/null || true
        # Remove from slinit.d (it was copied there too)
        rm -rf "${overlay_dir}/etc/slinit.d/systemd" 2>/dev/null || true
    fi

    # Files a case needs outside /etc: rootfs/ in the .d dir is laid over
    # the guest root as-is (51 seeds /var/lib/slinit/clock this way).
    if [ -d "${services_dir}/rootfs" ]; then
        cp -a "${services_dir}/rootfs"/. "${overlay_dir}/" 2>/dev/null || true
        rm -rf "${overlay_dir}/etc/slinit.d/rootfs" 2>/dev/null || true
    fi

    # Extra QEMU options for a case that needs different hardware: a
    # second CPU for an affinity mask that can be wrong, a stale RTC for
    # the clock guard. One word per option, whitespace-separated.
    local extra_qemu_args=""
    if [ -f "${services_dir}/qemu-args" ]; then
        extra_qemu_args=$(grep -v '^#' "${services_dir}/qemu-args")
        rm -f "${overlay_dir}/etc/slinit.d/qemu-args"
    fi

    # Extra kernel command-line words, appended to the default line:
    # psi=1 for a kernel that builds PSI but leaves it off by default.
    local extra_kernel_args=""
    if [ -f "${services_dir}/kernel-args" ]; then
        extra_kernel_args=$(grep -v '^#' "${services_dir}/kernel-args" | tr '\n' ' ')
        rm -f "${overlay_dir}/etc/slinit.d/kernel-args"
    fi

    # A different kernel, by flavour: `lts` for 126, which needs
    # CONFIG_NUMA, and linux-virt is built without it. build-vm.sh fetches
    # vmlinuz-lts (pinned) next to vmlinuz-virt; when it could not, the
    # case is reported SKIP here with the reason, not booted on the wrong
    # kernel to skip less legibly inside.
    #
    # Only virt has virtio built in — lts makes it modules, and the image
    # carries none for it — so any other kernel gets the result channel
    # on a second UART (ttyS1) instead of virtio-serial, and is told so on
    # its command line; guest-runner.sh reads it from /proc/cmdline.
    local kernel_flavor="virt"
    if [ -f "${services_dir}/kernel" ]; then
        kernel_flavor=$(grep -v '^#' "${services_dir}/kernel" | tr -d ' \t\n')
        rm -f "${overlay_dir}/etc/slinit.d/kernel"
    fi
    local kernel_image="${OUTPUT_DIR}/vmlinuz-${kernel_flavor}"
    if [ ! -f "${kernel_image}" ]; then
        harness_skip "${test_name}" "kernel '${kernel_flavor}' not built (build-vm.sh could not fetch vmlinuz-${kernel_flavor})"
        return 0
    fi
    local result_args="-device virtio-serial -device virtserialport,chardev=testresult,name=test.0"
    if [ "${kernel_flavor}" != "virt" ]; then
        result_args="-device isa-serial,chardev=testresult,index=1"
        extra_kernel_args="${extra_kernel_args} slinit_test.result=/dev/ttyS1"
    fi

    # Firmware: SeaBIOS unless the case asks for `efi`, which boots OVMF
    # (still with -kernel: OVMF hands the kernel over through fw_cfg and
    # the EFI stub). Not the default, though not for speed: measured on
    # 01-boot-starts, OVMF was 2.4 s faster per case under KVM (6.6 s
    # against 9.0 s; SeaBIOS is slow to load the 39 MB initrd) and 1.7 s
    # slower under TCG, which is what CI runs (15.8 s against 14.0 s).
    # Switching every case's firmware is a change of platform for 277
    # cases written against SeaBIOS, and one case needs it. The variable
    # store is a per-run copy; the system's file is only ever read.
    local firmware_args=""
    if [ -f "${services_dir}/firmware" ]; then
        local firmware
        firmware=$(grep -v '^#' "${services_dir}/firmware" | tr -d ' \t\n')
        rm -f "${overlay_dir}/etc/slinit.d/firmware"
        case "${firmware}" in
            bios|"") ;;
            efi)
                if ! find_ovmf; then
                    harness_skip "${test_name}" "no OVMF firmware found (install edk2-ovmf/ovmf, or set SLINIT_OVMF_CODE and SLINIT_OVMF_VARS)"
                    return 0
                fi
                cp "${OVMF_VARS}" "${test_dir}/efivars.fd"
                chmod u+w "${test_dir}/efivars.fd"
                firmware_args="-drive if=pflash,format=raw,unit=0,readonly=on,file=${OVMF_CODE} -drive if=pflash,format=raw,unit=1,file=${test_dir}/efivars.fd"
                ;;
            *)
                log_fail "${test_name} (unknown firmware '${firmware}' in ${test_name}.d/firmware)"
                FAILED=$((FAILED + 1))
                return 1
                ;;
        esac
    fi

    # Create the overlay cpio and concatenate with base
    local overlay_cpio="${test_dir}/overlay.cpio.gz"
    # -R 0:0 as for the base image: the overlay's "." entry otherwise
    # makes the guest's / owned by the uid that built it, which a real
    # system never has, and slinit-tmpfiles' safe path resolution
    # rightly refuses to step from a user's / into root's /tmp.
    (cd "${overlay_dir}" && find . | cpio -o -H newc -R 0:0 2>/dev/null | gzip) > "${overlay_cpio}"

    # Concatenate base + overlay (cpio archives are appendable)
    cat "${OUTPUT_DIR}/initramfs-base.cpio.gz" "${overlay_cpio}" > "${test_dir}/initramfs.cpio.gz"

    # Create a Unix socket for virtio-serial result channel.
    # Use /tmp to avoid the 108-byte Unix socket path limit.
    local chardev_path
    chardev_path=$(mktemp -u "/tmp/slinit-test-${test_name}-XXXXXX.sock")

    # Detect KVM. SLINIT_NO_KVM=1 forces software emulation even where
    # acceleration is available, which is how the CI runner executes these
    # cases — it has no usable /dev/kvm. Several cases have turned out to
    # assert something that only holds at hardware speed (219's CPU tick
    # budget, 223's log sampling, 07's restart allowance), and each was
    # found on the runner rather than here. This is the switch that
    # reproduces the runner's conditions on a developer box.
    local kvm_args="-cpu qemu64"
    if [ -w /dev/kvm ] 2>/dev/null && [ "${SLINIT_NO_KVM:-0}" != "1" ]; then
        kvm_args="-enable-kvm -cpu host"
    fi

    # Launch QEMU in background.
    #
    # -m 384, not 256: the initramfs is unpacked into RAM next to the
    # compressed copy, and at 256 MB the base image had about 3 MB of
    # headroom. Adding slinit-mount (3.4 MB) tipped it over: "Initramfs
    # unpacking failed: write error", the case's overlay never landed and
    # every case failed with "service 'boot' not found".
    qemu-system-x86_64 \
        ${kvm_args} \
        -kernel "${kernel_image}" \
        -initrd "${test_dir}/initramfs.cpio.gz" \
        -append "console=ttyS0 rdinit=/sbin/init loglevel=3 quiet ${extra_kernel_args}" \
        -m 384 \
        ${firmware_args} \
        ${extra_qemu_args} \
        -nographic \
        -no-reboot \
        -serial file:"${console_log}" \
        -chardev socket,id=testresult,path="${chardev_path}",server=on,wait=off \
        ${result_args} \
        &>"${test_dir}/qemu-stderr.log" &
    local qemu_pid=$!

    # Wait for QEMU socket, then read results.
    #
    # elapsed is read off the clock, not counted in iterations. It used to
    # be incremented by one per pass while the socat inside the loop was
    # allowed to block for TIMEOUT-elapsed seconds on its own, so the real
    # worst case was 60+59+58+... — about half an hour for a nominal 60s
    # budget. A guest that boots, opens the port and then never writes
    # hits exactly that: socat connects and blocks, costing nearly the
    # whole budget per pass while charging one second against it. Measured
    # at 950s for TIMEOUT=60 on case 181 under software emulation, which
    # is what CI runs.
    local started
    started=$(date +%s)
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
            local left=$(( TIMEOUT - ($(date +%s) - started) ))
            [ "$left" -lt 1 ] && break
            if command -v socat &>/dev/null; then
                timeout "$left" socat -u UNIX-CONNECT:"${chardev_path}" STDOUT > "${result_file}" 2>/dev/null && got_result=1 || true
            else
                timeout "$left" nc -U "${chardev_path}" > "${result_file}" 2>/dev/null && got_result=1 || true
            fi
            if [ -s "${result_file}" ]; then
                got_result=1
                break
            fi
        fi
        sleep 1
        elapsed=$(( $(date +%s) - started ))
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
    # tr: a UART result channel (non-virt kernels) is a tty; the guest
    # sets it raw, and a stray CR must still not turn PASS into FAIL.
    result_line=$(grep "^TEST_RESULT:" "${result_file}" | tail -1 | tr -d '\r')

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
        "TEST_RESULT:SKIP")
            # The reason is always shown, pass or no pass: a skip is only
            # useful if you can see why, and a skip that quietly becomes
            # permanent is how 164-slice-hierarchy covered nothing for
            # months while reading as a pass.
            log_skip "${test_name}"
            grep "^SKIP:" "${result_file}" 2>/dev/null | sed 's/^/    /' || true
            SKIPPED=$((SKIPPED + 1))
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
