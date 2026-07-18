#!/bin/sh
# Test: slinit-tmpfiles applies d/f/w/L directives + reads the
# expected dir chain. We ship a tiny fixture at /tmp/tmpfiles.d
# and point the binary at it.
mkdir -p /tmp/tmpfiles-fixture
cat > /tmp/tmpfiles-fixture/probe.conf <<'CONF'
d /tmp/slinit-tf-dir 0755 - - -
f /tmp/slinit-tf-file 0644 - - -
w /tmp/slinit-tf-write - - - - hello-tmpfiles
L /tmp/slinit-tf-link - - - - /tmp/slinit-tf-file
CONF

rm -rf /tmp/slinit-tf-dir /tmp/slinit-tf-file /tmp/slinit-tf-write /tmp/slinit-tf-link
slinit-tmpfiles --dirs=/tmp/tmpfiles-fixture 2>/tmp/tf-err
_rc=$?

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: slinit-tmpfiles exited 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-tmpfiles rc=$_rc"; sed 's/^/  err: /' /tmp/tf-err
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
[ -d /tmp/slinit-tf-dir ] && echo "OK: d created dir" || {
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: d did not create dir"
}
_TESTS_RUN=$((_TESTS_RUN + 1))
[ -f /tmp/slinit-tf-file ] && echo "OK: f created file" || {
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: f did not create file"
}
_TESTS_RUN=$((_TESTS_RUN + 1))
_val=$(cat /tmp/slinit-tf-write 2>/dev/null)
if [ "$_val" = "hello-tmpfiles" ]; then
    echo "OK: w wrote payload"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: w payload='$_val' expected 'hello-tmpfiles'"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -L /tmp/slinit-tf-link ] && [ "$(readlink /tmp/slinit-tf-link)" = "/tmp/slinit-tf-file" ]; then
    echo "OK: L created symlink"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: L symlink missing or wrong target"
fi

test_summary
