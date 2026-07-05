#!/bin/sh
# 93-slinit-check — offline config linter.
#
# Drops a fixture service-dir tree, points slinit-check at it via
# `-d`, and asserts the tool catches every category of defect the
# offline lint pass is documented to detect:
#   * unknown setting keys
#   * missing dependencies
#   * simple two-service cycles
#   * a clean tree returns success

WORK="/tmp/acceptance-check-svcdir"
cleanup() {
    rm -rf "$WORK" /tmp/check.out /tmp/check.err
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"

# --- 1. Clean tree passes -------------------------------------------------
cat >"$WORK/acceptance-test-clean-a" <<'EOF'
type = internal
EOF
cat >"$WORK/acceptance-test-clean-b" <<'EOF'
type = internal
depends-on: acceptance-test-clean-a
EOF

_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-check -d "$WORK" acceptance-test-clean-b \
        >/tmp/check.out 2>/tmp/check.err; then
    echo "OK: clean tree passes"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: clean tree returned non-zero — out: $(cat /tmp/check.out) err: $(cat /tmp/check.err)"
fi

# --- 2. Unknown setting is flagged ----------------------------------------
cat >"$WORK/acceptance-test-bad-setting" <<'EOF'
type = internal
totally-not-a-real-setting = 42
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-check -d "$WORK" acceptance-test-bad-setting \
        >/tmp/check.out 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unknown setting should have failed the lint"
else
    out=$(cat /tmp/check.out)
    case "$out" in
        *unknown*|*"not"*)
            echo "OK: unknown setting flagged"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: expected 'unknown' in output — got: $out"
            ;;
    esac
fi

# --- 3. Missing dependency is flagged -------------------------------------
cat >"$WORK/acceptance-test-missing-dep" <<'EOF'
type = internal
depends-on: acceptance-test-does-not-exist
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-check -d "$WORK" acceptance-test-missing-dep \
        >/tmp/check.out 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing dep should have failed the lint"
else
    out=$(cat /tmp/check.out)
    case "$out" in
        *"does-not-exist"*)
            echo "OK: missing dep names the unresolved target"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: missing dep output: $out"
            ;;
    esac
fi

# --- 4. Two-service dep cycle is caught -----------------------------------
cat >"$WORK/acceptance-test-cycle-a" <<'EOF'
type = internal
depends-on: acceptance-test-cycle-b
EOF
cat >"$WORK/acceptance-test-cycle-b" <<'EOF'
type = internal
depends-on: acceptance-test-cycle-a
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-check -d "$WORK" acceptance-test-cycle-a \
        >/tmp/check.out 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: dep cycle should have failed the lint"
else
    out=$(cat /tmp/check.out)
    case "$out" in
        *cycle*|*Cycle*)
            echo "OK: dependency cycle detected"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: cycle detection missing: $out"
            ;;
    esac
fi

# --- 5. Online mode probes the running daemon ----------------------------
# The daemon on this target already boots successfully (case 03 covers
# essential-services), so an online lint of `boot` must exit clean.
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-check --online >/tmp/check.out 2>/tmp/check.err; then
    echo "OK: online lint against running daemon passes"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: online lint rc=$? — err: $(cat /tmp/check.err)"
fi

test_summary
