#!/bin/sh
# Test: slinit-shell-var — pure argv sanitiser (non-alnum → '_').
# The tool exists purely to make init.d scripts able to build
# shell-variable-safe names from arbitrary user-facing identifiers.

wait_for_service "boot" "STARTED" 10

# --- Single arg: canonical use case ---

out=$(slinit-shell-var "my-service.d/1")
assert_eq "$out" "my_service_d_1" "single-arg canonical mapping"

# --- Multi-arg: joined with a literal space, each sanitised ---

out=$(slinit-shell-var "a.b" "c-d")
assert_eq "$out" "a_b c_d" "multi-arg keeps literal space between args"

# --- Space INSIDE an arg becomes '_'; the arg separator space
#     stays literal. Quoted to keep the shell from splitting. ---

out=$(slinit-shell-var "one with spaces" "second")
assert_eq "$out" "one_with_spaces second" "inner space becomes _"

# --- Numeric-heavy: dots+colons in IPv4 style ---

out=$(slinit-shell-var "127.0.0.1")
assert_eq "$out" "127_0_0_1" "IPv4-style dots become underscores"

# --- Pure punctuation → all underscores ---

out=$(slinit-shell-var '!@#$%^&*()')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    __________)
        echo "OK: pure punctuation becomes all underscores"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: got '$out', want 10 underscores"
        ;;
esac

# --- Zero args: just prints a newline (per the C original) ---

out=$(slinit-shell-var)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: zero args yields empty output"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: zero-args output = '$out'"
fi

# --- Documented use pattern: derive a shell-var name for eval. ---

svcname=$(slinit-shell-var "web-server.d/main")
_TESTS_RUN=$((_TESTS_RUN + 1))
if eval "${svcname}_PORT=8080" 2>/dev/null; then
    port_var="${svcname}_PORT"
    port_val=$(eval "echo \$${port_var}")
    if [ "$port_val" = "8080" ]; then
        echo "OK: sanitised name works as a shell-var identifier"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: eval loop lost the value"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eval refused the sanitised name '$svcname'"
fi

test_summary
