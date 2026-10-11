#!/bin/sh
# 235-condition-network-online — the predicate agrees with the machine,
# in both directions.
#
# HOW A SKIP IS OBSERVED, because two obvious ways do not work. An unmet
# `condition-*` is a SKIP, not a failure — systemd's ConditionXXX
# semantics, where the unit counts as successfully "nothing to do". So:
#
#   * the service still reports State: STARTED, which is why asserting
#     on the state found nothing;
#   * and `slinit-journalctl -u <svc>` does not carry the "skipped"
#     line either.
#
# What distinguishes them is whether the command ran. A marker file
# answers that regardless of how skipping is reported, so that is what
# this case checks.
#
# The expected answer is computed from /sys/class/net rather than
# assumed: the VM as built has only `lo`, so the predicate should skip,
# but the comparison stays correct if the image ever gains an interface.

_expect=no
for _d in /sys/class/net/*; do
    _n=$(basename "$_d")
    [ "$_n" = "lo" ] && continue
    [ "$(cat "$_d/operstate" 2>/dev/null)" = "up" ] || continue
    if ip -o addr show dev "$_n" 2>/dev/null | grep -q "inet"; then
        _expect=yes
    fi
done
echo "INFO: machine has a non-loopback interface with an address: $_expect"

# ran_with CONDITION -> yes|no, whether the command executed
ran_with() {
    rm -f /run/net-on-marker
    cat > /etc/slinit.d/test-net-cond <<EOF
type = scripted
command = /bin/sh -c 'touch /run/net-on-marker'
$1
EOF
    slinitctl --system start test-net-cond > /dev/null 2>&1
    _w=0
    while [ "$_w" -lt 12 ]; do
        [ -f /run/net-on-marker ] && break
        sleep 0.25
        _w=$((_w + 1))
    done
    [ -f /run/net-on-marker ] && _r=yes || _r=no
    slinitctl --system --ignore-unstarted stop test-net-cond > /dev/null 2>&1
    slinitctl --system unload test-net-cond > /dev/null 2>&1
    rm -f /etc/slinit.d/test-net-cond /run/net-on-marker
    echo "$_r"
}

_yes_ran=$(ran_with "condition-network-online = yes")
_no_ran=$(ran_with "condition-network-online = no")
echo "INFO: = yes → ran=$_yes_ran   = no → ran=$_no_ran"

assert_eq "$_yes_ran" "$_expect" \
    "condition-network-online = yes matches the machine's actual state"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_yes_ran" = "$_no_ran" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: yes and no both ran=$_yes_ran — the predicate is constant"
else
    echo "OK: the negated form is the exact inverse"
fi

test_summary
