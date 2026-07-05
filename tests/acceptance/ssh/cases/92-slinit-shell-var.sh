#!/bin/sh
# 92-slinit-shell-var — argv sanitiser (non-alnum → '_').

assert_eq "$(/usr/bin/slinit-shell-var 'my-service.d/1')" "my_service_d_1" \
    "single-arg canonical mapping"

assert_eq "$(/usr/bin/slinit-shell-var 'a.b' 'c-d')" "a_b c_d" \
    "multi-arg joined with literal space"

assert_eq "$(/usr/bin/slinit-shell-var 'one with spaces' 'second')" \
    "one_with_spaces second" "inner space becomes _"

assert_eq "$(/usr/bin/slinit-shell-var '127.0.0.1')" "127_0_0_1" \
    "IPv4-style dots become underscores"

_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(/usr/bin/slinit-shell-var '!@#$%^&*()')
case "$out" in
    __________)
        echo "OK: pure punctuation becomes all underscores"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: got '$out', want 10 underscores"
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(/usr/bin/slinit-shell-var)
if [ -z "$out" ]; then
    echo "OK: zero args yields empty output"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: zero args = '$out'"
fi

# Documented use pattern: sanitised name is a valid shell identifier.
svcname=$(/usr/bin/slinit-shell-var "web-server.d/main")
_TESTS_RUN=$((_TESTS_RUN + 1))
if eval "${svcname}_PORT=8080" 2>/dev/null; then
    port_val=$(eval "echo \$${svcname}_PORT")
    if [ "$port_val" = "8080" ]; then
        echo "OK: sanitised name works as shell identifier ('$svcname')"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: eval round-trip failed"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eval refused '$svcname'"
fi

test_summary
