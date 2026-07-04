#!/bin/bash
# build.sh - Build slinit QEMU demo environment
# Downloads Alpine Linux minirootfs, compiles slinit static binaries,
# and creates a bootable initramfs image.
set -euo pipefail

# Alpine Linux configuration
ALPINE_VERSION="3.21"
ALPINE_RELEASE="3.21.6"
ALPINE_ARCH="x86_64"
ALPINE_MIRROR="https://dl-cdn.alpinelinux.org/alpine"
MINIROOTFS_URL="${ALPINE_MIRROR}/v${ALPINE_VERSION}/releases/${ALPINE_ARCH}/alpine-minirootfs-${ALPINE_RELEASE}-${ALPINE_ARCH}.tar.gz"
PACKAGES_URL="${ALPINE_MIRROR}/v${ALPINE_VERSION}/main/${ALPINE_ARCH}"

# Bash and its dependencies (Alpine APK filenames)
BASH_PKG="bash-5.2.37-r0.apk"
READLINE_PKG="readline-8.2.13-r0.apk"
LIBNCURSESW_PKG="libncursesw-6.5_p20241006-r3.apk"
NCURSES_TERMINFO_PKG="ncurses-terminfo-base-6.5_p20241006-r3.apk"

# Directories
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${SCRIPT_DIR}/_build"
ROOTFS_DIR="${BUILD_DIR}/rootfs"
CACHE_DIR="${SCRIPT_DIR}/_cache"
OUTPUT_DIR="${SCRIPT_DIR}/_output"

mkdir -p "${CACHE_DIR}" "${BUILD_DIR}" "${OUTPUT_DIR}"

# Step 1: Download Alpine minirootfs
ROOTFS_TAR="${CACHE_DIR}/alpine-minirootfs-${ALPINE_RELEASE}-${ALPINE_ARCH}.tar.gz"
if [ ! -f "${ROOTFS_TAR}" ]; then
    echo "[1/7] Downloading Alpine minirootfs ${ALPINE_RELEASE}..."
    curl -fSL -o "${ROOTFS_TAR}.tmp" "${MINIROOTFS_URL}"
    mv "${ROOTFS_TAR}.tmp" "${ROOTFS_TAR}"
else
    echo "[1/7] Using cached Alpine minirootfs"
fi

# Step 2: Download Alpine virt kernel + watchdog module
# We download the linux-virt APK which contains both vmlinuz and kernel modules.
# This guarantees the kernel and modules are the same version.
KERNEL="${CACHE_DIR}/vmlinuz-virt"
KSTAGE="${BUILD_DIR}/kernel-stage"

# Discover the current linux-virt package from the mirror
KVIRT_PKG_FILE="${CACHE_DIR}/.linux-virt-pkg"
if [ ! -f "${KERNEL}" ] || [ ! -f "${KVIRT_PKG_FILE}" ]; then
    echo "[2/7] Downloading Alpine virt kernel + modules..."
    KVIRT_PKG=$(curl -sL "${PACKAGES_URL}/" | grep -o "linux-virt-[0-9][^\"]*\.apk" | sort -V | tail -1)
    if [ -n "${KVIRT_PKG}" ]; then
        if [ ! -f "${CACHE_DIR}/${KVIRT_PKG}" ]; then
            echo "  Fetching ${KVIRT_PKG}..."
            curl -fSL -o "${CACHE_DIR}/${KVIRT_PKG}.tmp" "${PACKAGES_URL}/${KVIRT_PKG}"
            mv "${CACHE_DIR}/${KVIRT_PKG}.tmp" "${CACHE_DIR}/${KVIRT_PKG}"
        fi
        echo "${KVIRT_PKG}" > "${KVIRT_PKG_FILE}"
        # Extract into staging area
        rm -rf "${KSTAGE}"
        mkdir -p "${KSTAGE}"
        tar xzf "${CACHE_DIR}/${KVIRT_PKG}" -C "${KSTAGE}" 2>/dev/null || true
        # Find and cache the kernel
        KVMLINUZ=$(find "${KSTAGE}" -name 'vmlinuz-*' -o -name 'vmlinuz' 2>/dev/null | head -1)
        if [ -n "${KVMLINUZ}" ]; then
            cp "${KVMLINUZ}" "${KERNEL}"
            echo "  Kernel: $(basename "${KVMLINUZ}")"
        else
            echo "  Warning: vmlinuz not found in ${KVIRT_PKG}"
        fi
    else
        echo "  Warning: could not find linux-virt package on mirror"
    fi
else
    echo "[2/7] Using cached kernel"
    # Re-extract staging if needed
    if [ ! -d "${KSTAGE}/lib" ]; then
        KVIRT_PKG=$(cat "${KVIRT_PKG_FILE}")
        if [ -f "${CACHE_DIR}/${KVIRT_PKG}" ]; then
            rm -rf "${KSTAGE}"
            mkdir -p "${KSTAGE}"
            tar xzf "${CACHE_DIR}/${KVIRT_PKG}" -C "${KSTAGE}" 2>/dev/null || true
        fi
    fi
fi

# Step 3: Download bash and dependencies (APK packages)
echo "[3/7] Downloading bash packages..."
for pkg in "${BASH_PKG}" "${READLINE_PKG}" "${LIBNCURSESW_PKG}" "${NCURSES_TERMINFO_PKG}"; do
    if [ ! -f "${CACHE_DIR}/${pkg}" ]; then
        echo "  Fetching ${pkg}..."
        curl -fSL -o "${CACHE_DIR}/${pkg}.tmp" "${PACKAGES_URL}/${pkg}"
        mv "${CACHE_DIR}/${pkg}.tmp" "${CACHE_DIR}/${pkg}"
    else
        echo "  Using cached ${pkg}"
    fi
done

# Step 4: Build full slinit toolchain (static)
echo "[4/7] Building slinit toolchain (static)..."
cd "${PROJECT_DIR}"
for bin in slinit slinitctl slinit-check slinit-monitor \
           slinit-shutdown slinit-init-maker slinit-nuke slinit-mount \
           rc-service rc-update rc-status \
           slinit-runner slinit-checkpath slinit-seedrng \
           slinit-binfmt slinit-sysctl slinit-svc-value \
           slinit-start-stop-daemon slinit-supervise-daemon \
           slinit-fstabinfo slinit-mountinfo slinit-einfo slinit-shell-var; do
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
        go build -ldflags='-s -w' -o "${BUILD_DIR}/${bin}" "./cmd/${bin}"
done

# Step 5: Prepare rootfs
echo "[5/7] Preparing rootfs..."
rm -rf "${ROOTFS_DIR}"
mkdir -p "${ROOTFS_DIR}"

# Extract Alpine minirootfs
tar xzf "${ROOTFS_TAR}" -C "${ROOTFS_DIR}"

# Install bash and dependencies from APK packages
# Alpine APK files are gzipped tars; extract directly into rootfs
for pkg in "${NCURSES_TERMINFO_PKG}" "${LIBNCURSESW_PKG}" "${READLINE_PKG}" "${BASH_PKG}"; do
    tar xzf "${CACHE_DIR}/${pkg}" -C "${ROOTFS_DIR}" 2>/dev/null || true
done
# Clean up APK metadata extracted into rootfs
rm -rf "${ROOTFS_DIR}/.PKGINFO" "${ROOTFS_DIR}/.SIGN."* "${ROOTFS_DIR}/.post-install" "${ROOTFS_DIR}/.pre-install" "${ROOTFS_DIR}/.trigger"

# Install watchdog kernel module from the linux-virt package (extracted in step 2).
HAS_WATCHDOG_MOD=0
echo "  Installing watchdog kernel module..."
if [ -d "${KSTAGE}/lib/modules" ]; then
    # Copy the i6300esb watchdog module
    (cd "${KSTAGE}" && find lib/modules -name 'i6300esb.ko*' -exec install -D {} "${ROOTFS_DIR}/{}" \;) 2>/dev/null || true
    # Copy module metadata so modprobe can resolve dependencies
    (cd "${KSTAGE}" && find lib/modules \( -name 'modules.dep*' -o -name 'modules.alias*' \
        -o -name 'modules.order' -o -name 'modules.builtin*' \) \
        -exec install -D {} "${ROOTFS_DIR}/{}" \;) 2>/dev/null || true
    if find "${ROOTFS_DIR}/lib/modules" -name 'i6300esb.ko*' 2>/dev/null | grep -q .; then
        echo "  Watchdog module (i6300esb) installed"
        HAS_WATCHDOG_MOD=1
    else
        echo "  Warning: i6300esb module not found in kernel package"
    fi
else
    echo "  Warning: no kernel modules available"
fi

# Install slinit binaries
install -m 755 "${BUILD_DIR}/slinit"            "${ROOTFS_DIR}/sbin/slinit"
install -m 755 "${BUILD_DIR}/slinitctl"         "${ROOTFS_DIR}/usr/bin/slinitctl"
install -m 755 "${BUILD_DIR}/slinit-check"      "${ROOTFS_DIR}/usr/bin/slinit-check"
install -m 755 "${BUILD_DIR}/slinit-monitor"    "${ROOTFS_DIR}/usr/bin/slinit-monitor"
install -m 755 "${BUILD_DIR}/slinit-shutdown"   "${ROOTFS_DIR}/sbin/slinit-shutdown"
install -m 755 "${BUILD_DIR}/slinit-init-maker" "${ROOTFS_DIR}/usr/bin/slinit-init-maker"
install -m 755 "${BUILD_DIR}/slinit-nuke"       "${ROOTFS_DIR}/sbin/slinit-nuke"
install -m 755 "${BUILD_DIR}/slinit-mount"      "${ROOTFS_DIR}/usr/sbin/slinit-mount"
install -m 755 "${BUILD_DIR}/rc-service"        "${ROOTFS_DIR}/usr/sbin/rc-service"
install -m 755 "${BUILD_DIR}/rc-update"         "${ROOTFS_DIR}/usr/sbin/rc-update"
install -m 755 "${BUILD_DIR}/rc-status"         "${ROOTFS_DIR}/usr/sbin/rc-status"

# slinit-runner must sit next to the slinit binary in /sbin/ (looked
# up as a sibling of os.Executable()). Every other new binary is a
# user-facing utility, so /usr/bin/.
install -m 755 "${BUILD_DIR}/slinit-runner"            "${ROOTFS_DIR}/sbin/slinit-runner"
install -m 755 "${BUILD_DIR}/slinit-checkpath"         "${ROOTFS_DIR}/usr/bin/slinit-checkpath"
install -m 755 "${BUILD_DIR}/slinit-seedrng"           "${ROOTFS_DIR}/usr/bin/slinit-seedrng"
install -m 755 "${BUILD_DIR}/slinit-binfmt"            "${ROOTFS_DIR}/usr/bin/slinit-binfmt"
install -m 755 "${BUILD_DIR}/slinit-sysctl"            "${ROOTFS_DIR}/usr/bin/slinit-sysctl"
install -m 755 "${BUILD_DIR}/slinit-svc-value"         "${ROOTFS_DIR}/usr/bin/slinit-svc-value"
install -m 755 "${BUILD_DIR}/slinit-start-stop-daemon" "${ROOTFS_DIR}/usr/bin/slinit-start-stop-daemon"
install -m 755 "${BUILD_DIR}/slinit-supervise-daemon"  "${ROOTFS_DIR}/usr/bin/slinit-supervise-daemon"
install -m 755 "${BUILD_DIR}/slinit-fstabinfo"         "${ROOTFS_DIR}/usr/bin/slinit-fstabinfo"
install -m 755 "${BUILD_DIR}/slinit-mountinfo"         "${ROOTFS_DIR}/usr/bin/slinit-mountinfo"
install -m 755 "${BUILD_DIR}/slinit-einfo"             "${ROOTFS_DIR}/usr/bin/slinit-einfo"
install -m 755 "${BUILD_DIR}/slinit-shell-var"         "${ROOTFS_DIR}/usr/bin/slinit-shell-var"

# einfo multi-applet symlinks so init.d scripts can invoke `einfo`,
# `ewarn`, `eerror`, `ebegin`, `eend`, etc. by name.
for _ap in einfo einfon ewarn ewarnn eerror eerrorn ebegin eend ewend \
           veinfo veinfon vewarn vewarnn vebegin veend vewend \
           eindent eoutdent veindent veoutdent \
           esyslog elog ewaitfile eval_ecolors; do
    ln -sf slinit-einfo "${ROOTFS_DIR}/usr/bin/${_ap}"
done

# service_get_value / service_set_value / service_export dispatch
# via argv[0]. Same pattern.
for _ap in service_get_value get_options service_set_value \
           save_options service_export; do
    ln -sf slinit-svc-value "${ROOTFS_DIR}/usr/bin/${_ap}"
done

# Set up /sbin/init: if we have the watchdog module, use an init-wrapper
# that loads it before exec'ing slinit (so /dev/watchdog0 exists when
# slinit's auto-detection runs). Otherwise, link directly to slinit.
if [ "${HAS_WATCHDOG_MOD}" = "1" ]; then
    cat > "${ROOTFS_DIR}/sbin/init-wrapper" <<'INITEOF'
#!/bin/sh
mount -t proc proc /proc 2>/dev/null
mount -t devtmpfs devtmpfs /dev 2>/dev/null
modprobe i6300esb 2>/dev/null
umount /proc 2>/dev/null
exec /sbin/slinit "$@"
INITEOF
    chmod 755 "${ROOTFS_DIR}/sbin/init-wrapper"
    ln -sf init-wrapper "${ROOTFS_DIR}/sbin/init"
else
    ln -sf slinit "${ROOTFS_DIR}/sbin/init"
fi

# SysV compat symlinks (slinit dispatches on argv[0]: halt / poweroff / reboot)
ln -sf slinit "${ROOTFS_DIR}/sbin/halt"
ln -sf slinit "${ROOTFS_DIR}/sbin/poweroff"
ln -sf slinit "${ROOTFS_DIR}/sbin/reboot"

# slinit-shutdown symlinks (reboot/halt/soft-reboot via argv[0])
ln -sf slinit-shutdown "${ROOTFS_DIR}/sbin/slinit-reboot"
ln -sf slinit-shutdown "${ROOTFS_DIR}/sbin/slinit-halt"
ln -sf slinit-shutdown "${ROOTFS_DIR}/sbin/slinit-soft-reboot"

# Ensure directories exist
mkdir -p "${ROOTFS_DIR}/run"
mkdir -p "${ROOTFS_DIR}/dev"
mkdir -p "${ROOTFS_DIR}/proc"
mkdir -p "${ROOTFS_DIR}/sys"

# Step 6: Install service files and shell completions
echo "[6/7] Installing service files and shell completions..."
mkdir -p "${ROOTFS_DIR}/etc/slinit.d"
# -R so env-dir subdirectories (e.g. runit-svc.env.d/) are copied intact;
# slinit's loader skips directories, so they don't clash with service files.
cp -R "${SCRIPT_DIR}/services/." "${ROOTFS_DIR}/etc/slinit.d/"

# OpenRC compat: /etc/rc.conf and /etc/conf.d/<svc> are sourced by the
# init.d wrapper before every action, so operators migrating from
# OpenRC keep their tunables.
mkdir -p "${ROOTFS_DIR}/etc/conf.d" "${ROOTFS_DIR}/etc/init.d"
cat > "${ROOTFS_DIR}/etc/rc.conf" <<'EOF'
# /etc/rc.conf — global OpenRC-style config (sourced by init.d wrapper)
rc_interactive="NO"
rc_parallel="YES"
EOF

cat > "${ROOTFS_DIR}/etc/conf.d/hello-initd" <<'EOF'
# /etc/conf.d/hello-initd — per-service OpenRC-style config
HELLO_MESSAGE="hello from /etc/conf.d/hello-initd"
EOF

# Demo init.d script (LSB headers so slinit auto-detects it and
# maps $network → waits-for network, etc.).
cat > "${ROOTFS_DIR}/etc/init.d/hello-initd" <<'INITDEOF'
#!/bin/sh
### BEGIN INIT INFO
# Provides:          hello-initd
# Required-Start:
# Required-Stop:
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: init.d + conf.d demo for slinit
# Description:       Exercises /etc/init.d auto-detection plus
#                    /etc/rc.conf and /etc/conf.d/<name> sourcing.
### END INIT INFO

LOG=/run/hello-initd.log
case "$1" in
    start)
        echo "[hello-initd] start: rc_parallel=${rc_parallel:-unset}, HELLO_MESSAGE=${HELLO_MESSAGE:-unset}" | tee -a "$LOG"
        ;;
    stop)
        echo "[hello-initd] stop" | tee -a "$LOG"
        ;;
    *)
        echo "usage: $0 {start|stop}" >&2
        exit 2
        ;;
esac
INITDEOF
chmod 755 "${ROOTFS_DIR}/etc/init.d/hello-initd"

# Second init.d demo: OpenRC-flavoured. No LSB block; deps come out
# of the depend() shell function, extracted by slinit's sandboxed
# `sh -c` parser. Try `slinitctl start openrc-initd-demo` after boot
# to see it (and its need-target dep) come up.
mkdir -p "${ROOTFS_DIR}/etc/conf.d"
cat > "${ROOTFS_DIR}/etc/conf.d/openrc-initd-demo" <<'EOF'
# /etc/conf.d/openrc-initd-demo — sourced by slinit's init.d wrapper.
GREETING="hello from OpenRC-style depend()"
EOF
cat > "${ROOTFS_DIR}/etc/init.d/openrc-initd-demo" <<'INITDEOF'
#!/sbin/openrc-run
# OpenRC-flavoured init.d script. slinit sees the openrc-run
# shebang, sandboxes the depend() function to extract deps, and
# sources start()/stop() directly (there is no openrc-run
# interpreter shipped).

description="OpenRC-style depend() demo"

depend() {
    # `need` is a hard dep — slinit maps this to depends-on:
    need system-init
    # `after` is order-only — mapped to slinit's after:
    after hello
}

start() {
    echo "[openrc-initd-demo] start: greeting=${GREETING:-unset}" > /run/openrc-initd-demo.log
}

stop() {
    echo "[openrc-initd-demo] stop" >> /run/openrc-initd-demo.log
}
INITDEOF
chmod 755 "${ROOTFS_DIR}/etc/init.d/openrc-initd-demo"

# systemd-flavoured binfmt.d fixture. Wired to demonstrate parse +
# discover. The Alpine virt kernel used by the demo does NOT ship
# binfmt_misc, so slinit-binfmt exits with code 3; try
# `slinit-binfmt -v` inside the VM to see the "not available"
# message. On a kernel with binfmt_misc built-in, the same tool
# would register the format below.
mkdir -p "${ROOTFS_DIR}/etc/binfmt.d"
cat > "${ROOTFS_DIR}/etc/binfmt.d/hello-demo.conf" <<'EOF'
# End-to-end fixture for slinit-binfmt.
# Maps files ending in ".demoext" to /bin/cat as their interpreter.
:demo-hello:E::demoext::/bin/cat:
EOF

# systemd-flavoured sysctl.d fixture. Consumed by sysctl-demo.
mkdir -p "${ROOTFS_DIR}/etc/sysctl.d"
cat > "${ROOTFS_DIR}/etc/sysctl.d/50-slinit-demo.conf" <<'EOF'
# Persisted here for `slinit-sysctl -v /etc/sysctl.d/50-slinit-demo.conf`.
# The `sysctl-demo` service writes its own fixture at boot; this file
# lets the interactive shell run slinit-sysctl by hand without any
# preamble.
kernel.printk = 3 3 1 6
-vm.this.does.not.exist = 42
EOF

# Helper script consumed by the supervise-demo service. Kept as a
# static drop rather than an inline printf inside pre-start-command
# because slinit's config parser pre-expands $VAR at load time; the
# child script's runtime `$n` / `$(...)` references would otherwise
# have to be escaped as `$$n` / `$$(...)` inside the service file.
mkdir -p "${ROOTFS_DIR}/usr/libexec"
cat > "${ROOTFS_DIR}/usr/libexec/supervise-demo-child.sh" <<'CHILDEOF'
#!/bin/sh
# supervise-demo-child.sh -- iteration counter for the supervise-daemon
# respawn loop. Bumps /tmp/supervise-demo-count each time the
# supervisor spawns us, then exits after a short sleep so the
# respawn logic actually fires.
n=0
if [ -f /tmp/supervise-demo-count ]; then
    n=$(cat /tmp/supervise-demo-count)
fi
n=$((n + 1))
echo "$n" >/tmp/supervise-demo-count
exec sleep 1
CHILDEOF
chmod 755 "${ROOTFS_DIR}/usr/libexec/supervise-demo-child.sh"

# fstabinfo/mountinfo need something to query. /etc/fstab exists on
# every Linux distro; give the demo VM a small one so the tools have
# non-empty output.
cat > "${ROOTFS_DIR}/etc/fstab" <<'EOF'
# Demo fstab consumed by slinit-fstabinfo / slinit-mountinfo.
# Try: slinit-fstabinfo -o /
# Try: slinit-fstabinfo --passno =2
# Try: slinit-mountinfo --nonetdev
proc     /proc  proc   defaults          0 0
sysfs    /sys   sysfs  defaults          0 0
tmpfs    /tmp   tmpfs  mode=1777,nosuid  0 0
tmpfs    /run   tmpfs  mode=755          0 0
/dev/vda /      ext4   defaults,noatime  0 1
/dev/vdb /home  ext4   defaults,noatime  0 2
EOF

# Install bash completion (generated by slinitctl itself)
mkdir -p "${ROOTFS_DIR}/etc/bash_completion.d"
"${BUILD_DIR}/slinitctl" completion bash > "${ROOTFS_DIR}/etc/bash_completion.d/slinitctl" 2>/dev/null || true

# Source completion from /etc/profile.d so interactive shells pick it up
mkdir -p "${ROOTFS_DIR}/etc/profile.d"
cat > "${ROOTFS_DIR}/etc/profile.d/slinitctl-completion.sh" <<'EOF'
# slinitctl bash completion
if type complete >/dev/null 2>&1 && [ -f /etc/bash_completion.d/slinitctl ]; then
    . /etc/bash_completion.d/slinitctl
fi
EOF

# Step 7: Create initramfs
echo "[7/7] Creating initramfs..."
cd "${ROOTFS_DIR}"
find . | cpio -o -H newc 2>/dev/null | gzip > "${OUTPUT_DIR}/initramfs.cpio.gz"
cp "${KERNEL}" "${OUTPUT_DIR}/vmlinuz-virt"

echo ""
echo "Build complete!"
echo "  Kernel:    ${OUTPUT_DIR}/vmlinuz-virt"
echo "  Initramfs: ${OUTPUT_DIR}/initramfs.cpio.gz"
echo ""
echo "Run with: ./run.sh"
