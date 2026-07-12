#!/bin/sh
# Test: slinit-seedrng persists an entropy pool across runs into
# -seed-dir. Fresh run writes seed.credit or seed.no-credit; a
# second run rotates the seed (sha256 changes).

SEEDDIR="/tmp/functional-seedrng"
rm -rf "$SEEDDIR"

_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-seedrng -seed-dir "$SEEDDIR" -quiet >/tmp/seedrng.out 2>/tmp/seedrng.err; then
    echo "OK: fresh run exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: fresh run rc=$?"
    test_summary
    return 1
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$SEEDDIR/seed.credit" ] || [ -f "$SEEDDIR/seed.no-credit" ]; then
    echo "OK: fresh seed file dropped in $SEEDDIR"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no seed.credit / seed.no-credit under $SEEDDIR"
fi

SEED_FILE=$(ls "$SEEDDIR"/seed.credit "$SEEDDIR"/seed.no-credit 2>/dev/null | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$SEED_FILE" ]; then
    _sz=$(stat -c '%s' "$SEED_FILE" 2>/dev/null || echo 0)
    if [ "$_sz" -ge 16 ]; then
        echo "OK: seed file has ${_sz} bytes (non-trivial payload)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: seed file is only ${_sz} bytes"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no seed file to size-check"
fi

rm -rf "$SEEDDIR"
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-seedrng -seed-dir "$SEEDDIR" -skip-credit -quiet \
        >/tmp/seedrng.out 2>/tmp/seedrng.err; then
    if ls "$SEEDDIR"/seed.* >/dev/null 2>&1; then
        echo "OK: -skip-credit run exits 0 and writes a seed file"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: -skip-credit did not write any seed file"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -skip-credit rc=$?"
fi

SEED_FILE=$(ls "$SEEDDIR"/seed.credit "$SEEDDIR"/seed.no-credit 2>/dev/null | head -1)
if [ -n "$SEED_FILE" ]; then
    before_sha=$(sha256sum "$SEED_FILE" | cut -d' ' -f1)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if slinit-seedrng -seed-dir "$SEEDDIR" -quiet \
            >/tmp/seedrng.out 2>/tmp/seedrng.err; then
        SEED_FILE_AFTER=$(ls "$SEEDDIR"/seed.credit "$SEEDDIR"/seed.no-credit 2>/dev/null | head -1)
        after_sha=$(sha256sum "$SEED_FILE_AFTER" | cut -d' ' -f1)
        if [ "$before_sha" != "$after_sha" ]; then
            echo "OK: fresh seed rotated across runs (sha256 changed)"
        else
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: seed did not change across runs"
        fi
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: second run rc=$?"
    fi
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(slinit-seedrng -version 2>&1)
case "$out" in
    *seedrng*|*version*|*[0-9]*) echo "OK: -version prints something and exits" ;;
    *) _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: -version output: $out" ;;
esac

test_summary
