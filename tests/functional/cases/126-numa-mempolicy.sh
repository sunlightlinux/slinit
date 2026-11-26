#!/bin/sh
# Test: numa-mempolicy = bind on node 0 is the service's memory policy.
# Requires CONFIG_NUMA, which Alpine's virt kernel lacks: 126.d/kernel
# boots linux-lts for this case and 126.d/qemu-args gives it two nodes.
# Still skips when /sys/devices/system/node isn't there (no lts kernel).

SVC="test-numa"

if [ ! -d /sys/devices/system/node ]; then
    skip_case "kernel does not expose /sys/devices/system/node (CONFIG_NUMA off)"
fi
_nodes=$(ls -d /sys/devices/system/node/node[0-9]* 2>/dev/null | wc -l)
echo "INFO: $(uname -r), $_nodes NUMA node(s)"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
numa-mempolicy = bind
numa-nodes = 0
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

# Sample the service, not the wrapper: the PID slinit reports is
# slinit-runner until it execs (see 125-mlockall for how sampling the
# runner made a case pass for a broken feature).
_pid=""
_deadline=$(( $(date +%s) + 15 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
    if [ -n "$_pid" ] && [ "$_pid" != "0" ]; then
        case "$(cat "/proc/$_pid/comm" 2>/dev/null)" in
            slinit-runner|"") ;;
            *) break ;;
        esac
    fi
    sleep 0.5
done

# The policy itself. numa_maps prints, per mapping, the VMA's own policy
# or else the task's — and the service set none of its own, so every
# line carries what slinit asked for. A service that ran with no policy
# at all shows "default", so this is what tells the setting apart from
# its absence; "numa_maps is readable" (all this case used to check)
# holds either way. Two nodes (126.d/qemu-args) make node 0 a choice.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r "/proc/$_pid/numa_maps" ]; then
    _maps=$(cat "/proc/$_pid/numa_maps" 2>/dev/null)
    _n=$(printf '%s\n' "$_maps" | grep -c .)
    _bound=$(printf '%s\n' "$_maps" | awk '$2 == "bind:0"' | grep -c .)
    if [ "$_n" -gt 0 ] && [ "$_bound" -eq "$_n" ]; then
        echo "OK: all $_n mappings of PID $_pid ($(cat /proc/$_pid/comm)) carry bind:0"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_bound of $_n mappings of PID $_pid carry bind:0; first: '$(printf '%s\n' "$_maps" | head -1)'"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /proc/$_pid/numa_maps unreadable"
fi

test_summary
