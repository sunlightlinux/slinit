#!/bin/sh
# Test: protect-kernel-tunables = yes ro-remounts /proc/sys in the
# child's mount ns so sysctl writes fail. The probe writes its
# result to /var/tmp/pkt-out/result and we inspect it here.
#
# swapoff is checked as a secondary signal — the seccomp deny list
# includes swapon/swapoff, but if busybox lacks the swapoff applet
# in this VM build, the check gets a soft skip.
#
# swapoff on a target that does not exist fails WITHOUT the filter too
# (ENOENT), and that used to be counted as "denied". Only EPERM
# ("not permitted") counts now; any other error fails the case and
# prints swapoff's stderr. (No mutant for this one: turning the
# directive off also drops the /proc/sys protection, and the seccomp
# half alone cannot be switched off from a service file.)

wait_for_service "pkt-svc" "STARTED" 15

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /var/tmp/pkt-out/result ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: probe did not write its result file"
    test_summary
    return
fi
echo "OK: probe wrote result"

result=$(cat /var/tmp/pkt-out/result 2>/dev/null)
proc=$(echo "$result" | sed -n 's/.*proc=\([^ ]*\).*/\1/p')
swap=$(echo "$result" | sed -n 's/.*swap=\([^ ]*\).*/\1/p')

assert_eq "$proc" "protected" "/proc/sys/net/ipv4/ip_forward write blocked"

case "$swap" in
denied)
    assert_eq "$swap" "denied" "swapoff blocked (seccomp deny list)"
    ;;
no-swapoff)
    echo "  note: busybox swapoff applet not built into this VM — seccomp path not exercised"
    ;;
other)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: swapoff failed with something other than EPERM — filter not hit: $(cat /var/tmp/pkt-out/swap-err 2>/dev/null)"
    ;;
allowed)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: swapoff succeeded on a non-existent target — deny filter missing?"
    ;;
*)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unexpected swap result: '$swap'"
    ;;
esac

test_summary
