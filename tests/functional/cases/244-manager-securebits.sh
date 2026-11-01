#!/bin/sh
# 244-manager-securebits — slinit --securebits reaches every service.
#
# systemd's manager SecureBits= (v263): bits set for the service manager
# are inherited by everything it starts. slinit applies them through
# slinit-runner, so a service that sets no securebits of its own must
# still get them. The effect is visible from a shell: with noroot, a
# service running as root gains no capabilities when it execs, so its
# CapEff is 0. A second instance without --securebits is the control,
# and must show the capabilities, so the check can tell the two apart.
#
# Runs a separate slinit in user mode, as root, on its own socket: the
# test cannot change PID 1's flags, and a user-mode instance does no
# system setup.

wait_for_service "boot" "STARTED" 15

_dir=/tmp/sb244.d
mkdir -p "$_dir"
# A script, not the command line: `command =` expands $VAR itself.
cat > /tmp/sb244.sh <<'EOF'
#!/bin/sh
awk '/^CapEff:/ { print $2 }' /proc/self/status > "/tmp/sb244.$1"
# exec, so the only process left to stop is sleep.
exec sleep 3600
EOF

run_instance() { # $1 = tag, rest = extra slinit flags
    _tag=$1
    shift
    cat > "$_dir/probe" <<EOF
type = process
command = /bin/sh /tmp/sb244.sh $_tag
restart = no
stop-timeout = 2
EOF
    rm -f "/tmp/sb244.$_tag"
    slinit --user -p "/tmp/sb244-$_tag.sock" -d "$_dir" -B "$@" -t probe \
        >"/tmp/sb244-$_tag.log" 2>&1 &
    _pid=$!
    for _ in $(seq 1 50); do
        [ -s "/tmp/sb244.$_tag" ] && break
        sleep 0.1
    done
    kill "$_pid" 2>/dev/null
    # Poll rather than `wait`: in this harness busybox ash's wait never
    # returned after the instance had exited, and hung the case.
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        [ -d "/proc/$_pid" ] || break
        sleep 0.5
    done
}

run_instance locked --securebits=noroot
run_instance control

_locked=$(cat /tmp/sb244.locked 2>/dev/null)
_control=$(cat /tmp/sb244.control 2>/dev/null)
echo "MEASURE: CapEff with --securebits=noroot: '$_locked', without: '$_control'"

assert_eq "$_locked" "0000000000000000" "service under --securebits=noroot has no effective caps"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_control" ] && [ "$_control" != "0000000000000000" ]; then
    echo "OK: control instance's service keeps its caps"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: control CapEff '$_control'; the check cannot tell noroot from its absence"
    tail -5 /tmp/sb244-control.log
fi

[ -n "$_locked" ] || tail -5 /tmp/sb244-locked.log

test_summary
