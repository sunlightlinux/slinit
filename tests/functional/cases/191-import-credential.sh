#!/bin/sh
# Test: import-credential copies the matching credentials from the system
# credential store into $CREDENTIALS_DIRECTORY, and only those.
#
# rootfs/ seeds /etc/credstore with my-secret-a, my-secret-b and
# other-x; the service imports my-secret-*. (The case used to have an
# empty store and check only the set-credential entry, which is there
# with or without import-credential.)

wait_for_service "importcred-svc" "STARTED" 10
sleep 1

has() { grep -qx "$1" /tmp/191-creds 2>/dev/null && echo yes || echo no; }

echo "INFO: credentials directory: $(tr '\n' ' ' < /tmp/191-creds 2>/dev/null)"
assert_eq "$(has my-secret-a)" "yes" "my-secret-a imported"
assert_eq "$(has my-secret-b)" "yes" "my-secret-b imported"
assert_eq "$(has other-x)" "no" "other-x (no match) not imported"
assert_eq "$(has fallback)" "yes" "set-credential entry alongside the imports"
assert_eq "$(cat /tmp/191-secret-a 2>/dev/null)" "alpha" "imported credential has the store's content"

test_summary
