#!/bin/sh
# Test: slinit-binfmt end-to-end. Exercises two paths:
#
#   1. Real kernel: register+unregister against /proc/sys/fs/binfmt_misc
#      when the running kernel actually supports binfmt_misc.
#   2. Fixture tree via --root=DIR when it doesn't — matches the
#      test seam the unit tests already use, plus real filesystem
#      operations under the guest.
#
# The Alpine virt kernel used for these functional tests does NOT
# ship binfmt_misc, so path (1) always skips in CI. Path (2) still
# meaningfully exercises the discover/parse/apply pipeline against
# actual on-disk files.

wait_for_service "boot" "STARTED" 10

# ---------------------------------------------------------------
# Path 2: fixture tree via --root
# ---------------------------------------------------------------

# Build a scratch tree that mirrors the paths the tool scans.
FIXTURE=/tmp/binfmt-fixture
rm -rf "$FIXTURE"
mkdir -p "$FIXTURE/etc/binfmt.d"
mkdir -p "$FIXTURE/usr/lib/binfmt.d"
mkdir -p "$FIXTURE/proc/sys/fs/binfmt_misc"
: >"$FIXTURE/proc/sys/fs/binfmt_misc/register"

# Distro-shipped file: should be overridden by the /etc/ copy.
cat >"$FIXTURE/usr/lib/binfmt.d/shared.conf" <<'EOF'
:shared:E::distro::/bin/distro-interp:
EOF

# Operator override with the same basename — /etc/ wins.
cat >"$FIXTURE/etc/binfmt.d/shared.conf" <<'EOF'
:shared:E::operator::/bin/operator-interp:
EOF

# A file with a comment and blank lines; must land the one spec.
cat >"$FIXTURE/etc/binfmt.d/mine.conf" <<'EOF'
# operator note above
;semicolon comment

:mine:M::AAAA:BBBB:/bin/cat:
EOF

# Apply and inspect the fake register file. Its final contents are
# the last spec written (the fixture file has no append semantics,
# unlike the real procfs entry).
output=$(slinit-binfmt --root="$FIXTURE" --verbose 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$output" in
    *registered=2*|*"registered=2 "*)
        echo "OK: --verbose reports registered=2 across the two files"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: --verbose output: $output"
        ;;
esac

# Whichever spec was written last must have reached the register
# entry point. Sort order is deterministic (alphabetical basename),
# so the final write is `shared.conf` from /etc/.
last=$(cat "$FIXTURE/proc/sys/fs/binfmt_misc/register" 2>&1)
assert_contains "$last" "operator-interp" \
    "/etc/ override wins over /usr/lib/ (last write)"
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$last" in
    *distro-interp*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: distro version leaked into final write: $last"
        ;;
    *)
        echo "OK: distro version overridden by /etc/"
        ;;
esac

# Bogus config: wildcard-prefixed key should fail parseName. The
# tool must report a non-zero exit code and mention the file+line.
cat >"$FIXTURE/etc/binfmt.d/bad.conf" <<'EOF'
this line has no delimiter
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-binfmt --root="$FIXTURE" >/dev/null 2>/tmp/binfmt.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: malformed config should have caused non-zero exit"
else
    err=$(cat /tmp/binfmt.err)
    case "$err" in
        *bad.conf:1*)
            echo "OK: parse error includes file+line"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: error missing file+line: $err"
            ;;
    esac
fi

# Cleanup so path (1)'s conditional stays honest.
rm -f "$FIXTURE/etc/binfmt.d/bad.conf"

# ---------------------------------------------------------------
# Path 1: real /proc/sys/fs/binfmt_misc, if the kernel supports it
# ---------------------------------------------------------------

if [ -e /proc/sys/fs/binfmt_misc/register ]; then
    # Kernel exposes it — exercise the register/unregister loop.
    mkdir -p /etc/binfmt.d
    cat >/etc/binfmt.d/slinittest.conf <<'EOF'
:slinittest:E::slinittest::/bin/cat:
EOF
    output=$(slinit-binfmt --verbose 2>&1)
    assert_contains "$output" "registered=1" "real: one spec registered"
    assert_eq \
        "$(test -e /proc/sys/fs/binfmt_misc/slinittest && echo yes || echo no)" \
        "yes" "real: /proc entry created"

    output=$(slinit-binfmt --unregister --verbose 2>&1)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$output" in
        *unregistered=*)
            echo "OK: real: --unregister emitted a summary"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: --unregister output = $output"
            ;;
    esac
else
    # No binfmt_misc kernel support — verify the tool reports
    # exit code 3 with a clear "not available" message. Even that
    # much is real end-to-end coverage.
    slinit-binfmt >/tmp/binfmt.out 2>/tmp/binfmt.err
    rc=$?
    assert_eq "$rc" "3" "real: exit code 3 when binfmt_misc unavailable"
    err=$(cat /tmp/binfmt.err)
    assert_contains "$err" "not available" \
        "real: stderr explains why"
fi

test_summary
