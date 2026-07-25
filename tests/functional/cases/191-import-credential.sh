#!/bin/sh
# Test: import-credential parses + service starts.
# Validates: the directive is accepted by the loader; the credentials
# tmpfs is populated with whatever slinit could resolve (fallback here
# is set-credential which we know lands). Behavioral verification of
# a daemon that actually has credentials to pass through belongs in
# an acceptance test with a real credential source.

wait_for_service "importcred-svc" "STARTED" 10
assert_service_state "importcred-svc" "STARTED" "import-credential parses + svc starts"

sleep 1

# Verify the credentials dir was set up with at least the fallback
# set-credential entry; import-credential should be an additive no-op
# when the source has no matches.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q '^fallback$' /tmp/191-creds 2>/dev/null; then
    echo "OK: credentials tmpfs contains the set-credential entry"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: credentials tmpfs does not contain 'fallback' — set-credential missing?"
    cat /tmp/191-creds 2>/dev/null | sed 's/^/    /'
fi

test_summary
