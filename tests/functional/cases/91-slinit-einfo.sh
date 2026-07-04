#!/bin/sh
# Test: slinit-einfo argv[0] dispatch to the 20+ applet names,
# stream routing (info→stdout, error→stderr, eerror rc=1), verbose
# gating, indent handling, and ewaitfile with a real file appearing
# after a short delay.

wait_for_service "boot" "STARTED" 10

# Install the applet symlinks. Real deployments hand this to the
# packaging layer; the case wires them at runtime so it stays
# self-contained.
for name in einfo einfon ewarn ewarnn eerror eerrorn ebegin eend \
            ewend veinfo vewarn vebegin veend vewend \
            eindent eoutdent veindent veoutdent \
            esyslog elog ewaitfile eval_ecolors; do
    ln -sf /usr/bin/slinit-einfo "/usr/bin/${name}"
done

# --- Base info goes to stdout, ends with newline, contains msg ---

out=$(einfo "hello world" 2>/tmp/einfo.err)
assert_contains "$out" "hello world" "einfo prints message"
assert_contains "$out" " * " "einfo prefixes with ' * '"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$(cat /tmp/einfo.err)" ]; then
    echo "OK: einfo does not leak to stderr"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stderr got: $(cat /tmp/einfo.err)"
fi

# --- ewarn goes to stderr ---

out=$(ewarn "watch out" 2>/tmp/einfo.err >/dev/null)
err=$(cat /tmp/einfo.err)
assert_contains "$err" "watch out" "ewarn writes to stderr"

# --- eerror returns 1 and goes to stderr ---

_TESTS_RUN=$((_TESTS_RUN + 1))
if eerror "kaboom" 2>/tmp/einfo.err >/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eerror should exit 1"
else
    err=$(cat /tmp/einfo.err)
    case "$err" in
        *kaboom*)
            echo "OK: eerror rc=1, stderr contains message"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: eerror stderr = $err"
            ;;
    esac
fi

# --- The `n` suffix suppresses the trailing newline ---

out=$(einfon "no-nl")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$out" in
    *"no-nl"*)
        # tr -d strips \n; if the output already lacks one, length
        # is unchanged after tr — but we test by looking at the last
        # byte via od.
        last=$(printf "%s" "$out" | od -c | tail -1 | awk '{print $2}')
        if [ "$last" != "\\n" ]; then
            echo "OK: einfon has no trailing newline"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: einfon added a newline"
        fi
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: einfon missing message: $out"
        ;;
esac

# --- veinfo is gated on EINFO_VERBOSE ---

out=$(veinfo "should be silent" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: veinfo silent without EINFO_VERBOSE"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: veinfo leaked: $out"
fi

out=$(EINFO_VERBOSE=yes veinfo "loud now" 2>&1)
assert_contains "$out" "loud now" "veinfo with EINFO_VERBOSE prints"

# --- EINFO_QUIET suppresses even einfo ---

out=$(EINFO_QUIET=yes einfo "hidden" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: EINFO_QUIET suppresses einfo"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: quiet leaked: $out"
fi

# --- eend prints the OK / FAIL marker + propagates status ---

# Force COLUMNS so the marker is visible in a fixed-width window.
out=$(COLUMNS=40 eend 0 2>&1)
assert_contains "$out" "[ ok ]" "eend 0 prints [ ok ] marker"

_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(COLUMNS=40 eend 7 "died" 2>&1)
rc=$?
if [ "$rc" = "7" ]; then
    case "$out" in
        *"[ !! ]"*)
            case "$out" in
                *died*)
                    echo "OK: eend non-zero prints [ !! ] + message, propagates rc"
                    ;;
                *)
                    _TESTS_FAILED=$((_TESTS_FAILED + 1))
                    echo "FAIL: eend message missing: $out"
                    ;;
            esac
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: eend marker missing: $out"
            ;;
    esac
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eend rc=$rc, want 7"
fi

# --- eindent is a no-op stub (subprocess can't mutate parent env) ---

out=$(eindent 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$out" ]; then
    echo "OK: eindent is a silent no-op"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eindent leaked: $out"
fi

# --- eval_ecolors prints shell-var assignments ---

out=$(eval_ecolors)
for k in GOOD WARN BAD HILITE BRACKET NORMAL; do
    assert_contains "$out" "${k}=" "eval_ecolors emits ${k}="
done

# --- ewaitfile fires once the target appears ---

TARGET=/tmp/appear-later
rm -f "$TARGET"
( sleep 0.3; touch "$TARGET" ) &

start=$(date +%s)
_TESTS_RUN=$((_TESTS_RUN + 1))
if EINFO_VERBOSE=yes ewaitfile 3 "$TARGET" >/dev/null 2>&1; then
    elapsed=$(( $(date +%s) - start ))
    if [ "$elapsed" -le 3 ]; then
        echo "OK: ewaitfile fired within 3s"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ewaitfile took $elapsed s"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: ewaitfile did not fire"
fi
rm -f "$TARGET"

# ewaitfile times out on a file that never appears — with a 1s cap.
_TESTS_RUN=$((_TESTS_RUN + 1))
if EINFO_VERBOSE=yes ewaitfile 1 /tmp/nevernever >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: ewaitfile should time out"
else
    echo "OK: ewaitfile times out on missing target"
fi

# Cleanup symlinks.
for name in einfo einfon ewarn ewarnn eerror eerrorn ebegin eend \
            ewend veinfo vewarn vebegin veend vewend \
            eindent eoutdent veindent veoutdent \
            esyslog elog ewaitfile eval_ecolors; do
    rm -f "/usr/bin/${name}"
done

test_summary
