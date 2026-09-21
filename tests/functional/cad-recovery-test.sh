#!/bin/sh
# cad-recovery-test.sh — does Ctrl+Alt+Del work when slinit is parked at
# a recovery prompt?
#
# This is deliberately NOT one of the cases under cases/. Those run
# inside the guest, and the whole point here is a boot that never gets
# far enough to start anything: a service fails to load, slinit prints
# its recovery menu, and the operator reaches for Ctrl+Alt+Del. There is
# no test runner in that world, so the check has to be driven from the
# host — by pressing the actual key combination through QEMU's monitor.
#
# Why it exists: this exact scenario went wrong twice.
#   1. PID 1 died outright, because signal.Notify was only reached ~1600
#      lines into main and the Go runtime's default for SIGINT is to
#      exit. As PID 1 that is a kernel panic (fixed in ab3c58e).
#   2. With the signals claimed, nothing read them while a recovery
#      prompt was up, so the key did nothing at all and the operator's
#      only exit was the 60s auto-reboot (fixed in a506b36).
#
# The pass condition is simply: QEMU goes away. It runs with -no-reboot,
# so the guest rebooting means the process exits. A hang means the key
# was ignored; a panic also shows up as a hang, and the console log is
# kept either way so the two can be told apart.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/_output"
WORK="${SCRIPT_DIR}/_build/cad-recovery"
TIMEOUT="${TIMEOUT:-60}"

if [ ! -f "${OUTPUT_DIR}/initramfs-base.cpio.gz" ] || [ ! -f "${OUTPUT_DIR}/vmlinuz-virt" ]; then
    echo "cad-recovery-test: base VM image missing — run ./run-tests.sh once first" >&2
    exit 2
fi

rm -rf "${WORK}"
mkdir -p "${WORK}/overlay/etc/slinit.d"

# A boot service that cannot load: it depends on something that does not
# exist. This is the shape an operator hits when a service file is
# broken or a package half-installed, and it lands slinit on the
# load-failure menu rather than in a normal boot.
cat > "${WORK}/overlay/etc/slinit.d/system-init" <<'SVC'
type = scripted
command = /bin/sh -c "mount -t proc proc /proc 2>/dev/null; mount -t sysfs sysfs /sys 2>/dev/null; mount -t devtmpfs devtmpfs /dev 2>/dev/null; mkdir -p /run"
stop-command = /bin/true
SVC

cat > "${WORK}/overlay/etc/slinit.d/boot" <<'SVC'
type = internal
depends-on: system-init
depends-on: this-service-does-not-exist
SVC

(cd "${WORK}/overlay" && find . | cpio -o -H newc 2>/dev/null | gzip) > "${WORK}/overlay.cpio.gz"
cat "${OUTPUT_DIR}/initramfs-base.cpio.gz" "${WORK}/overlay.cpio.gz" > "${WORK}/initramfs.cpio.gz"

console_log="${WORK}/console.log"
monitor_sock=$(mktemp -u "/tmp/slinit-cad-XXXXXX.sock")

kvm_args="-cpu qemu64"
if [ -w /dev/kvm ] 2>/dev/null; then
    kvm_args="-enable-kvm -cpu host"
fi

# -no-reboot turns a guest reboot into a QEMU exit, which is the signal
# this test reads. The monitor is what lets us press the key.
# shellcheck disable=SC2086
qemu-system-x86_64 \
    ${kvm_args} \
    -kernel "${OUTPUT_DIR}/vmlinuz-virt" \
    -initrd "${WORK}/initramfs.cpio.gz" \
    -append "console=ttyS0 rdinit=/sbin/init loglevel=3" \
    -m 256 \
    -nographic \
    -no-reboot \
    -serial file:"${console_log}" \
    -monitor "unix:${monitor_sock},server,nowait" \
    &>"${WORK}/qemu-stderr.log" &
qemu_pid=$!

cleanup() {
    kill "${qemu_pid}" 2>/dev/null || true
    rm -f "${monitor_sock}"
}
trap cleanup EXIT

# Wait for slinit to reach the recovery menu. Keying before the prompt
# is up would test nothing, so look for the menu's own text.
waited=0
reached=0
while [ "${waited}" -lt 40 ]; do
    if grep -q "BOOT FAILURE\|Choose an action" "${console_log}" 2>/dev/null; then
        reached=1
        break
    fi
    if ! kill -0 "${qemu_pid}" 2>/dev/null; then
        echo "FAIL: the guest died before reaching the recovery prompt"
        echo "      (a kernel panic here means signals are unclaimed again)"
        tail -25 "${console_log}" 2>/dev/null | sed 's/^/      /'
        exit 1
    fi
    waited=$((waited + 1))
    sleep 1
done

if [ "${reached}" != "1" ]; then
    echo "FAIL: never reached the recovery prompt in 40s — the test's broken"
    echo "      service may no longer fail to load"
    tail -25 "${console_log}" 2>/dev/null | sed 's/^/      /'
    exit 1
fi
echo "OK: slinit is parked at the recovery prompt"

# Press the real key combination. The kernel turns this into SIGINT to
# PID 1 because InitPID1 disabled CAD.
printf 'sendkey ctrl-alt-delete\n' | timeout 5 socat - "UNIX-CONNECT:${monitor_sock}" >/dev/null 2>&1 \
    || { echo "FAIL: could not reach the QEMU monitor (socat missing?)"; exit 2; }
echo "OK: sent ctrl-alt-delete"

# The guest should reboot, and -no-reboot turns that into QEMU exiting.
waited=0
while [ "${waited}" -lt "${TIMEOUT}" ]; do
    if ! kill -0 "${qemu_pid}" 2>/dev/null; then
        echo "PASS: Ctrl+Alt+Del at the recovery prompt rebooted the guest"
        exit 0
    fi
    waited=$((waited + 1))
    sleep 1
done

echo "FAIL: guest still running ${TIMEOUT}s after Ctrl+Alt+Del — the key was ignored"
echo "      console tail:"
tail -25 "${console_log}" 2>/dev/null | sed 's/^/      /'
exit 1
