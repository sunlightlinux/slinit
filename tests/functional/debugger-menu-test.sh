#!/bin/sh
# debugger-menu-test.sh — is the Ctrl-B debugger box readable on a busy
# boot?
#
# Deliberately NOT one of the cases under cases/. Those run inside the
# guest, and what is being checked here is what the *console* looks like
# at the moment a key is pressed — so the key has to come from the host,
# through QEMU's monitor, exactly as in cad-recovery-test.sh.
#
# What it covers: Ctrl-B opens the debugger, the box is well-formed, and
# the boot console stays out of it — a regression test for the renderer
# and for the PauseBootConsole wiring.
#
# What it does NOT cover, stated plainly so nobody mistakes it for
# coverage: a demo boot was reported showing five "[ OK ] name" lines
# between the top bar and the title, with the box cut open. This harness
# does not reproduce that. Ctrl-B is driven into a boot with forty
# services completing at the same instant, and the box comes out clean
# three times out of three even with the settle and the clear-screen in
# pkg/recovery removed. Two service-timing designs were tried (one second
# apart, and all simultaneous); neither reproduced it. Whatever happens
# in the demo VM is something this does not model, so that report is
# still open.
#
# Pass condition: between the bar that opens the debugger box and the bar
# that closes it, every line is a box row.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUTPUT_DIR="${SCRIPT_DIR}/_output"
WORK="${SCRIPT_DIR}/_build/debugger-menu"
TIMEOUT="${TIMEOUT:-60}"

if [ ! -f "${OUTPUT_DIR}/initramfs-base.cpio.gz" ] || [ ! -f "${OUTPUT_DIR}/vmlinuz-virt" ]; then
    echo "debugger-menu-test: base VM image missing — run ./run-tests.sh once first" >&2
    exit 2
fi

rm -rf "${WORK}"
mkdir -p "${WORK}/overlay/etc/slinit.d"

cat > "${WORK}/overlay/etc/slinit.d/system-init" <<'SVC'
type = scripted
command = /bin/sh -c "mount -t proc proc /proc 2>/dev/null; mount -t sysfs sysfs /sys 2>/dev/null; mount -t devtmpfs devtmpfs /dev 2>/dev/null; mkdir -p /run"
stop-command = /bin/true
SVC

# Forty services finishing at the SAME instant, plus one marker that
# finishes a moment earlier to key off.
#
# The count and the simultaneity both matter. A write of one status line
# to a serial console at 115200 baud takes milliseconds, so a goroutine
# already inside bootStatus is parked in write() for that long; forty of
# them queue up and drain over a window wide enough to interleave with
# whatever the menu is drawing. Services finishing a second apart never
# reproduce it — there is only ever one write in flight.
cat > "${WORK}/overlay/etc/slinit.d/marker" <<'SVC'
type = scripted
command = /bin/sh -c "sleep 2.9"
depends-on: system-init
SVC

i=1
while [ "$i" -le 40 ]; do
    cat > "${WORK}/overlay/etc/slinit.d/flood-$i" <<'SVC'
type = scripted
command = /bin/sh -c "sleep 3"
depends-on: system-init
SVC
    i=$((i + 1))
done

{
    echo "type = internal"
    echo "depends-on: system-init"
    echo "waits-for: marker"
    i=1
    while [ "$i" -le 40 ]; do
        echo "waits-for: flood-$i"
        i=$((i + 1))
    done
} > "${WORK}/overlay/etc/slinit.d/boot"

(cd "${WORK}/overlay" && find . | cpio -o -H newc 2>/dev/null | gzip) > "${WORK}/overlay.cpio.gz"
cat "${OUTPUT_DIR}/initramfs-base.cpio.gz" "${WORK}/overlay.cpio.gz" > "${WORK}/initramfs.cpio.gz"

console_log="${WORK}/console.log"
serial_sock=$(mktemp -u "/tmp/slinit-dbgmenu-XXXXXX.sock")
input_fifo="${WORK}/console.in"
mkfifo "${input_fifo}"

kvm_args="-cpu qemu64"
if [ -w /dev/kvm ] 2>/dev/null; then
    kvm_args="-enable-kvm -cpu host"
fi

# shellcheck disable=SC2086
qemu-system-x86_64 \
    ${kvm_args} \
    -kernel "${OUTPUT_DIR}/vmlinuz-virt" \
    -initrd "${WORK}/initramfs.cpio.gz" \
    -append "console=ttyS0 rdinit=/sbin/init loglevel=3" \
    -m 256 \
    -nographic \
    -no-reboot \
    -serial "unix:${serial_sock},server,nowait" \
    &>"${WORK}/qemu-stderr.log" &
qemu_pid=$!

# The console has to be bidirectional, which `-serial file:` is not.
# Ctrl-B on a serial console is the byte 0x02 arriving on the line, not a
# keyboard event — which is why the QEMU monitor's `sendkey` is no use
# here and why cad-recovery-test.sh can get away with it: Ctrl+Alt+Del is
# handled by the kernel's keyboard driver, not read as a character.
#
# socat copies the serial socket into the log and the fifo into the
# serial socket. fd 3 holds the fifo open so socat does not see EOF and
# close the write side before the key is sent.
sleep 0.5
socat "UNIX-CONNECT:${serial_sock}" - >"${console_log}" <"${input_fifo}" 2>/dev/null &
socat_pid=$!
exec 3>"${input_fifo}"

cleanup() {
    exec 3>&- 2>/dev/null || true
    kill "${socat_pid}" 2>/dev/null || true
    kill "${qemu_pid}" 2>/dev/null || true
    rm -f "${serial_sock}"
}
trap cleanup EXIT

# Press the moment the marker reports, which is a tenth of a second
# before forty services report at once. The debugger polls its console on
# a 200ms tick, so the menu draws squarely inside that flood.
waited=0
reached=0
while [ "${waited}" -lt 800 ]; do
    if grep -q "OK.*marker" "${console_log}" 2>/dev/null; then
        reached=1
        break
    fi
    sleep 0.05
    waited=$((waited + 1))
done

if [ "${reached}" -ne 1 ]; then
    echo "FAIL: boot never reached the marker service"
    tail -25 "${console_log}" 2>/dev/null | sed 's/^/      /'
    exit 1
fi

# Ctrl-B is 0x02 on the wire.
printf '\002' >&3

# Give the menu time to settle and draw.
sleep 3

if ! grep -q "BOOT DEBUGGER" "${console_log}" 2>/dev/null; then
    echo "FAIL: Ctrl-B did not open the debugger menu"
    tail -30 "${console_log}" 2>/dev/null | sed 's/^/      /'
    exit 1
fi

# Extract the box: the bar before the BOOT DEBUGGER title through the
# next bar after it. Anything in between that is not a box row is the
# bug this test exists for.
intruders=$(awk '
    /^\+=+\+/ { if (inbox) { exit } ; bar_seen = 1; buf = ""; next }
    bar_seen && /BOOT DEBUGGER/ { inbox = 1 }
    bar_seen { if (!/^\|/) { print } }
' "${console_log}" | tr -d '\r' | grep -v '^[[:space:]]*$' || true)

if [ -n "${intruders}" ]; then
    echo "FAIL: boot-console output landed inside the debugger box:"
    printf '%s\n' "${intruders}" | sed 's/^/      /'
    echo "      full box:"
    sed -n '/^+=*+$/,/^+=*+$/p' "${console_log}" | tr -d '\r' | sed 's/^/      /' | head -30
    exit 1
fi

echo "PASS: the debugger box is intact on a busy boot"
exit 0
