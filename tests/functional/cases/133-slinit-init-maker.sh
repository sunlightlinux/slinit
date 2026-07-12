#!/bin/sh
# Test: slinit-init-maker generates a bootable service tree. Exercises
# -dry-run (no writes), real generation (boot/system-init/getty/
# README all land), and -force overwrites.

WORK="/tmp/functional-init-maker"
rm -rf "$WORK"

_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-init-maker -d "$WORK" -dry-run >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    if [ ! -d "$WORK" ]; then
        echo "OK: -dry-run exit 0 without creating $WORK"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: -dry-run created files at $WORK"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -dry-run rc=$?"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-init-maker -d "$WORK" >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    echo "OK: real generation exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rc=$?"
    test_summary
    return 1
fi

for _svc in boot system-init README.md; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ -f "$WORK/$_svc" ]; then
        echo "OK: $_svc written"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_svc missing"
    fi
done

_TESTS_RUN=$((_TESTS_RUN + 1))
_getty_count=$(ls "$WORK"/getty-tty[0-9]* 2>/dev/null | wc -l)
if [ "$_getty_count" -ge 1 ]; then
    echo "OK: getty-tty[N] services written ($_getty_count entries)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no getty-tty* entries"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "system-init" "$WORK/boot" 2>/dev/null; then
    echo "OK: boot references system-init"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: boot does not reference system-init"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-check -d "$WORK" boot >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    echo "OK: slinit-check accepts the generated tree"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check on generated tree failed"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-init-maker -d "$WORK" >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: second run without -force should refuse"
else
    echo "OK: second run without -force refuses to clobber"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-init-maker -d "$WORK" -force >/tmp/init-maker.out 2>/tmp/init-maker.err; then
    echo "OK: -force accepts an already-populated tree"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -force rc=$?"
fi

test_summary
