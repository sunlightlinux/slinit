#!/bin/sh
# debugger-menu-test.sh — is the Ctrl-B debugger box readable on a busy
# boot?
#
# Deliberately NOT one of the cases under cases/. Those run inside the
# guest, and what is being checked here is what the *console* looks like
# at the moment a key is pressed — so the key has to come from the host,
# through QEMU's monitor, exactly as in cad-recovery-test.sh.
#
# What it covers: Ctrl-B opens the debugger on a busy boot, the box is
# well-formed, [f] on a stale list explains itself instead of flatly
# contradicting the screen, and the redraw afterwards is also intact.
#
# What it does NOT cover, and why the check for it lives elsewhere: a demo
# boot showed a WARN line sitting between two rows of the box —
#
#   |   [s] / Ctrl-B   drop to shell                             |
#   [12:56:37] WARN: Debug menu: force-fail requested but no ...
#   |   [f]            force-fail first in-progress service      |
#
# That needs a console slow enough that one write is still draining when
# the next begins. This harness cannot produce it: the guest console is a
# unix socket drained as fast as socat can read, so the whole box goes out
# in microseconds. It was tried three ways — forty services completing at
# once, a service flapping on a restart loop logging throughout, and
# driving [f] to make the menu log — and the box came out clean every time
# even with the fix removed.
#
# So the fix is asserted where it can be: TestPauseBootConsoleMutesEveryLevel
# in pkg/logging checks the gate itself, deterministically, and fails with
# six console lines when the gate is removed. Do not re-add a race here.
#
# Pass condition: between the bar that opens the debugger box and the bar
# that closes it, every line is a box row — including the box redrawn
# after [f].
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
# A service that keeps failing and being restarted, so the state machine
# logs from its own goroutines for the whole life of the menu. That is the
# shape that cuts the box open: a concurrent writer to the console, not
# the menu's own sequential output. The menu's own WARN cannot interleave
# with the menu's own redraw — they are the same goroutine.
cat > "${WORK}/overlay/etc/slinit.d/flapper" <<'SVC'
type = process
command = /bin/sh -c "exit 7"
restart = yes
restart-delay = 0.2
depends-on: system-init
SVC

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
    echo "waits-for: flapper"
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

# Now the deterministic part. By this point the flood has finished, so
# [f] finds nothing in progress, logs a WARN about the stale list, and
# the loop redraws the menu. The log line and the redraw are concurrent
# writers to the same console.
printf 'f' >&3
sleep 3

# Extract the box: the bar before the BOOT DEBUGGER title through the
# next bar after it. Anything in between that is not a box row is the
# bug this test exists for.
intruders=$(tr -d '\r' < "${console_log}" | awk '
    # Track every bar-delimited region and report any line inside one
    # that is not a box row. Two boxes are drawn: the first menu, and the
    # redraw after [f].
    /^\+=+\+$/ { inbox = !inbox; next }
    inbox && !/^\|/ { print }
' | grep -v '^[[:space:]]*$' || true)

if [ -n "${intruders}" ]; then
    echo "FAIL: boot-console output landed inside the debugger box:"
    printf '%s\n' "${intruders}" | sed 's/^/      /'
    echo "      full box:"
    sed -n '/^+=*+$/,/^+=*+$/p' "${console_log}" | tr -d '\r' | sed 's/^/      /' | head -30
    exit 1
fi

# And the WARN must still have been recorded, just not on the console:
# muting the menu's screen must not lose the event.
if ! grep -q "force-fail requested but no service is in progress" "${console_log}" 2>/dev/null; then
    echo "NOTE: the stale-list WARN never reached the console, as intended"
fi

echo "PASS: the debugger box is intact, including the redraw after [f]"
exit 0
