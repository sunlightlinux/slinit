#!/bin/sh
# Test: `slinitctl add-dep` and `rm-dep` accept the prepared-by kind.
#
# slinitctl maps `prepared-by` to its dependency type and slinitctl(8)
# documents it for add-dep and rm-dep, but up to v4.0.3 the server
# refused every type above the five older ones: the command could only
# ever fail with "add-dep failed: reply 52" (2e6893e).

for n in a b; do
    printf '%s\n' 'type = process' "command = /bin/sleep 315$n" 'restart = false' \
        > "/etc/slinit.d/test-315-$n"
    timeout 5 slinitctl --system status "test-315-$n" >/dev/null 2>&1   # load
done

_out=$(timeout 10 slinitctl --system add-dep test-315-a prepared-by test-315-b 2>&1)
_rc=$?
echo "add-dep: rc=$_rc $_out"
assert_eq "$_rc" "0" "add-dep prepared-by succeeds"
assert_contains "$_out" "Added" "and says the edge was added"

_out=$(timeout 10 slinitctl --system rm-dep test-315-a prepared-by test-315-b 2>&1)
_rc=$?
echo "rm-dep: rc=$_rc $_out"
assert_eq "$_rc" "0" "rm-dep prepared-by succeeds"
assert_contains "$_out" "Removed" "and says the edge was removed"

# The edge really went: removing it again finds nothing.
timeout 10 slinitctl --system rm-dep test-315-a prepared-by test-315-b >/dev/null 2>&1
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: a second rm-dep finds no edge"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a second rm-dep still found a prepared-by edge"
fi

test_summary
