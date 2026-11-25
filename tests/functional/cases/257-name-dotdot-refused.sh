#!/bin/sh
# 257-name-dotdot-refused: a service name with a '..' path element is
# refused. A leading '.' always was, but '/' is allowed before the '@' and
# anything after it, and the loader joins the name onto every service
# directory: "a/../../zz257" and "x@/../../zz257" loaded and ran /etc/zz257,
# a file outside all of them.

cat > /etc/zz257 <<'SVC'
type = process
command = /bin/sh -c "touch /run/257.ran; exec sleep 600"
SVC
for name in "a/../../zz257" "x@/../../zz257"; do
    rm -f /run/257.ran
    out=$(slinitctl --system start "$name" 2>&1)
    rc=$?
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$rc" -ne 0 ]; then
        echo "OK: start $name refused (rc=$rc)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: start $name accepted: $out"
    fi
    sleep 1
    assert_eq "$(test -e /run/257.ran && echo ran || echo not-run)" "not-run" "$name: file outside the service directory not run"
    slinitctl --system stop "$name" > /dev/null 2>&1
done
rm -f /etc/zz257

test_summary
