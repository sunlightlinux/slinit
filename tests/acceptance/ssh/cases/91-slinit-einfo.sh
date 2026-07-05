#!/bin/sh
# 91-slinit-einfo — argv[0] dispatch across the einfo applet family.
# Applet symlinks live under /usr/bin/ (installed by
# slinit-openrc-shims).

_check_ap() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ -x "/usr/bin/$1" ]; then
        echo "OK: /usr/bin/$1 present"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: /usr/bin/$1 missing"
    fi
}
for _ap in einfo einfon ewarn ewarnn eerror eerrorn ebegin eend ewend \
           veinfo veinfon vewarn vewarnn vebegin veend vewend \
           eindent eoutdent veindent veoutdent \
           esyslog elog ewaitfile eval_ecolors; do
    _check_ap "$_ap"
done

# --- Base info goes to stdout, ends with newline ---
out=$(einfo "hello world" 2>/tmp/einfo.err)
assert_contains() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$1" in
        *"$2"*) echo "OK: $3" ;;
        *) _TESTS_FAILED=$((_TESTS_FAILED + 1))
           echo "FAIL: $3" ;;
    esac
}
assert_contains "$out" "hello world" "einfo prints message"
assert_contains "$out" " * " "einfo prefixes with ' * '"

# --- ewarn → stderr ---
out=$(ewarn "watch out" 2>/tmp/einfo.err >/dev/null)
assert_contains "$(cat /tmp/einfo.err)" "watch out" "ewarn writes to stderr"

# --- eerror rc=1 ---
_TESTS_RUN=$((_TESTS_RUN + 1))
if eerror "kaboom" 2>/tmp/einfo.err >/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eerror should exit 1"
else
    assert_contains "$(cat /tmp/einfo.err)" "kaboom" "eerror stderr contains message"
    echo "OK: eerror rc=1"
fi

# --- veinfo gated on EINFO_VERBOSE ---
out=$(veinfo "silent" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: veinfo silent without EINFO_VERBOSE"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: veinfo leaked"
fi

out=$(EINFO_VERBOSE=yes veinfo "loud" 2>&1)
assert_contains "$out" "loud" "veinfo with EINFO_VERBOSE prints"

# --- EINFO_QUIET blanket suppression ---
out=$(EINFO_QUIET=yes einfo "hidden" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: EINFO_QUIET suppresses einfo"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: quiet leaked: $out"
fi

# --- eend marker + status propagation ---
out=$(COLUMNS=40 eend 0 2>&1)
assert_contains "$out" "[ ok ]" "eend 0 prints [ ok ] marker"

_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(COLUMNS=40 eend 7 "died" 2>&1)
rc=$?
if [ "$rc" = "7" ]; then
    case "$out" in
        *"[ !! ]"*)
            assert_contains "$out" "died" "eend message present"
            echo "OK: eend non-zero rc=7, marker + message"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: eend marker missing"
            ;;
    esac
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eend rc=$rc"
fi

# --- eval_ecolors dumps all 6 vars ---
out=$(eval_ecolors)
for k in GOOD WARN BAD HILITE BRACKET NORMAL; do
    assert_contains "$out" "${k}=" "eval_ecolors emits ${k}="
done

test_summary
