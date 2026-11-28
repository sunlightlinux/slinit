#!/bin/sh
# Test: keyring-mode (4.1, systemd's KeyringMode=) under PID 1.
#
# A service with keyring-mode = private gets a session keyring of its
# own: /proc/keys, read after a service without the setting (which
# inherits slinit's) read it, shows a "_ses" keyring that one did not.

if [ ! -r /proc/keys ]; then
    echo "SKIP: no /proc/keys"
    exit 77
fi

cat > /etc/slinit.d/test-345-private <<'SVC'
type = scripted
command = /bin/sh -c "grep _ses /proc/keys | awk '{print $$1}' | sort > /run/test-345-private; echo done >> /run/test-345-private"
keyring-mode = private
SVC
cat > /etc/slinit.d/test-345-inherit <<'SVC'
type = scripted
command = /bin/sh -c "grep _ses /proc/keys | awk '{print $$1}' | sort > /run/test-345-inherit; echo done >> /run/test-345-inherit"
SVC
# The inheriting one first: /proc/keys lists every keyring root may
# view, not only those a process holds, so the private one shows a
# "_ses" keyring the first did not see.
slinitctl --system start test-345-inherit >/dev/null 2>&1
slinitctl --system start test-345-private >/dev/null 2>&1
p=$(cat /run/test-345-private 2>/dev/null)
i=$(cat /run/test-345-inherit 2>/dev/null)
assert_contains "$p" "done" "the private service ran"
assert_contains "$i" "done" "the inheriting service ran"
new=$(for k in $(echo "$p" | grep -v done); do echo "$i" | grep -qx "$k" || echo "$k"; done)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$new" ]; then
    echo "OK: the private service has a session keyring of its own ($new)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: private [$(echo "$p" | tr '\n' ' ')] inherit [$(echo "$i" | tr '\n' ' ')]: no keyring of its own"
fi

test_summary
