#!/bin/sh
# 94-slinit-init-maker — generates a bootable service-description tree.
#
# The tool takes a scratch directory + flags and drops the canonical
# `boot`, `system-init`, `tty`, agetty per configured console, plus a
# README. We exercise the `-dry-run` mode first (must not touch disk)
# and then a real generation into a scratch tree.

WORK="/tmp/acceptance-init-maker"
cleanup() {
    rm -rf "$WORK" /tmp/init-maker.out /tmp/init-maker.err
}
trap cleanup EXIT INT TERM
cleanup

# --- 1. -dry-run must not touch disk --------------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-init-maker -d "$WORK" -dry-run \
        >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    if [ ! -d "$WORK" ]; then
        echo "OK: -dry-run exit 0 without creating $WORK"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: -dry-run created files at $WORK"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -dry-run rc=$? — err: $(cat /tmp/init-maker.err)"
fi

# --- 2. Real generation lays down the expected files ----------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-init-maker -d "$WORK" \
        >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    echo "OK: real generation exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rc=$? — err: $(cat /tmp/init-maker.err)"
    test_summary
    exit 0
fi

# Every generated init-maker tree must include the boot milestone,
# system-init, and per-console getty services.
for _svc in boot system-init README.md; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ -f "$WORK/$_svc" ]; then
        echo "OK: $_svc written"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_svc missing from generated tree"
    fi
done

# The generator drops per-console getty-tty1..6 by default.
_TESTS_RUN=$((_TESTS_RUN + 1))
_getty_count=$(ls "$WORK"/getty-tty[0-9]* 2>/dev/null | wc -l)
if [ "$_getty_count" -ge 1 ]; then
    echo "OK: getty-tty[N] services written ($_getty_count tty entries)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no getty-tty* entries found"
fi

# The generated boot references system-init and tty (or their aliases).
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "system-init" "$WORK/boot" 2>/dev/null; then
    echo "OK: boot references system-init"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: boot does not reference system-init"
fi

# The generated tree passes slinit-check — the whole point of this
# tool is to give operators a graph the linter is happy with.
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-check -d "$WORK" boot \
        >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    echo "OK: slinit-check accepts the generated tree"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check on generated tree failed — $(cat /tmp/init-maker.out)"
fi

# --- 3. -force overwrites without complaint -------------------------------
# A second run without -force would refuse to touch existing files.
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-init-maker -d "$WORK" \
        >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: second run without -force should refuse"
else
    echo "OK: second run without -force refuses to clobber"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-init-maker -d "$WORK" -force \
        >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    echo "OK: -force accepts an already-populated tree"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -force rc=$? — err: $(cat /tmp/init-maker.err)"
fi

test_summary
