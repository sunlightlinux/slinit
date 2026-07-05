#!/bin/sh
# 84-slinit-binfmt — end-to-end for the systemd-binfmt(1) clone against
# a --root=DIR fixture tree, plus the real /proc/sys/fs/binfmt_misc
# path when the running kernel supports it.
#
# The tool ships in the `slinit` main package as /usr/bin/slinit-binfmt.
# discover / parse / apply are exercised against files an operator
# actually drops on disk — no in-process mocks.

FIXTURE="/tmp/acceptance-binfmt"
cleanup() {
    rm -rf "$FIXTURE"
    rm -f /tmp/binfmt.err /tmp/binfmt.out
    # Real /proc/sys entry gets torn down at end of the case if we
    # created one, so a re-run always starts clean.
    if [ -e /proc/sys/fs/binfmt_misc/acceptance-test-slinit ]; then
        echo -1 > /proc/sys/fs/binfmt_misc/acceptance-test-slinit 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM
cleanup

mkdir -p "$FIXTURE/etc/binfmt.d"
mkdir -p "$FIXTURE/usr/lib/binfmt.d"
mkdir -p "$FIXTURE/proc/sys/fs/binfmt_misc"
: >"$FIXTURE/proc/sys/fs/binfmt_misc/register"

# Two configs with the same basename — /etc/ must win over /usr/lib/.
cat >"$FIXTURE/usr/lib/binfmt.d/shared.conf" <<'EOF'
:shared:E::distroext::/bin/distro-interp:
EOF
cat >"$FIXTURE/etc/binfmt.d/shared.conf" <<'EOF'
:shared:E::operatorext::/bin/operator-interp:
EOF
# Second file with a comment + blank line so we exercise both.
cat >"$FIXTURE/etc/binfmt.d/mine.conf" <<'EOF'
# operator note
;semicolon comment

:mine:M::AAAA:BBBB:/bin/cat:
EOF

out=$(/usr/bin/slinit-binfmt --root="$FIXTURE" --verbose 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"registered=2"*)
        echo "OK: --verbose summary reports registered=2 across the two files"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: verbose summary: $out"
        ;;
esac

# /etc/ override wins.
last=$(cat "$FIXTURE/proc/sys/fs/binfmt_misc/register" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$last" in
    *operator-interp*)
        echo "OK: /etc/ override wins (last register write = operator-interp)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: last write was $last"
        ;;
esac
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$last" in
    *distro-interp*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: distro-interp leaked into final write"
        ;;
    *)
        echo "OK: distro version overridden (no distro-interp in final write)"
        ;;
esac

# Bogus config → parse error with file+line.
cat >"$FIXTURE/etc/binfmt.d/bad.conf" <<'EOF'
this line has no delimiter
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-binfmt --root="$FIXTURE" >/dev/null 2>/tmp/binfmt.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: malformed config should exit non-zero"
else
    err=$(cat /tmp/binfmt.err)
    case "$err" in
        *bad.conf:1*)
            echo "OK: parse error names file+line"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: error missing file+line: $err"
            ;;
    esac
fi
rm -f "$FIXTURE/etc/binfmt.d/bad.conf"

# Real /proc/sys path. If the kernel has binfmt_misc, register a
# format that maps *.slinittest to /bin/cat and confirm the entry
# appears. Otherwise assert exit 3 + "not available" message.
if [ -e /proc/sys/fs/binfmt_misc/register ]; then
    # Kernel supports it — try a real registration under our namespace.
    mkdir -p /etc/binfmt.d
    cat >/etc/binfmt.d/acceptance-test-slinit.conf <<'EOF'
:acceptance-test-slinit:E::slinittest::/bin/cat:
EOF
    out=$(/usr/bin/slinit-binfmt --verbose /etc/binfmt.d/acceptance-test-slinit.conf 2>&1)
    rm -f /etc/binfmt.d/acceptance-test-slinit.conf
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$out" in
        *"registered=1"*)
            echo "OK: real kernel accepted the spec"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: real kernel register: $out"
            ;;
    esac
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ -e /proc/sys/fs/binfmt_misc/acceptance-test-slinit ]; then
        echo "OK: /proc/sys/fs/binfmt_misc/acceptance-test-slinit exists"
        # Verify the interpreter is what we asked for.
        status=$(cat /proc/sys/fs/binfmt_misc/acceptance-test-slinit)
        case "$status" in
            *"/bin/cat"*)
                echo "OK: interpreter is /bin/cat"
                _TESTS_RUN=$((_TESTS_RUN + 1))
                ;;
            *)
                _TESTS_FAILED=$((_TESTS_FAILED + 1))
                echo "FAIL: interpreter status = $status"
                _TESTS_RUN=$((_TESTS_RUN + 1))
                ;;
        esac
        echo -1 > /proc/sys/fs/binfmt_misc/acceptance-test-slinit 2>/dev/null || true
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: /proc entry did not appear"
    fi
else
    _TESTS_RUN=$((_TESTS_RUN + 1))
    /usr/bin/slinit-binfmt >/dev/null 2>/tmp/binfmt.err
    rc=$?
    if [ "$rc" = "3" ]; then
        case "$(cat /tmp/binfmt.err)" in
            *"not available"*)
                echo "OK: exit 3 + 'not available' when binfmt_misc kernel module missing"
                ;;
            *)
                _TESTS_FAILED=$((_TESTS_FAILED + 1))
                echo "FAIL: stderr = $(cat /tmp/binfmt.err)"
                ;;
        esac
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: kernel-missing exit rc=$rc"
    fi
fi

test_summary
