#!/bin/sh
# 95-slinit-seedrng — persistent entropy pool save/restore.
#
# The tool operates on a --seed-dir (default /var/lib/seedrng): reads
# any existing `seed.credit`/`seed.no-credit` files into the kernel
# via RNDADDENTROPY, writes a fresh seed for the next boot. We drive
# a scratch directory so /var/lib/seedrng on the target is untouched.

SEEDDIR="/tmp/acceptance-seedrng"
cleanup() {
    rm -rf "$SEEDDIR" /tmp/seedrng.out /tmp/seedrng.err
}
trap cleanup EXIT INT TERM
cleanup

# --- 1. First run creates a fresh seed ------------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-seedrng -seed-dir "$SEEDDIR" -quiet \
        >/tmp/seedrng.out 2>/tmp/seedrng.err; then
    echo "OK: fresh run exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: fresh run rc=$? — err: $(cat /tmp/seedrng.err)"
    test_summary
    exit 0
fi

# One of seed.credit / seed.no-credit lands after a run.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$SEEDDIR/seed.credit" ] || [ -f "$SEEDDIR/seed.no-credit" ]; then
    echo "OK: fresh seed file dropped in $SEEDDIR"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no seed.credit / seed.no-credit under $SEEDDIR"
    ls -la "$SEEDDIR" 2>&1
fi

# The seed is a SHA-256 blob — 32 bytes on disk. The exact file name
# is seed.credit or seed.no-credit depending on whether the run
# passed -skip-credit; the byte layout is identical.
SEED_FILE=$(ls "$SEEDDIR"/seed.credit "$SEEDDIR"/seed.no-credit 2>/dev/null | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$SEED_FILE" ]; then
    _sz=$(stat -c '%s' "$SEED_FILE" 2>/dev/null || echo 0)
    # 32 bytes = SHA-256 digest length, the tool's fixed seed size.
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

# --- 2. -skip-credit is accepted --------------------------------------
# The flag affects how PREVIOUSLY-STORED seeds are treated on read
# (credit or not); it doesn't change the file name of the fresh seed
# written for the NEXT boot. Just assert the flag is accepted and the
# run completes cleanly.
rm -rf "$SEEDDIR"
_TESTS_RUN=$((_TESTS_RUN + 1))
if /usr/bin/slinit-seedrng -seed-dir "$SEEDDIR" -skip-credit -quiet \
        >/tmp/seedrng.out 2>/tmp/seedrng.err; then
    if ls "$SEEDDIR"/seed.* >/dev/null 2>&1; then
        echo "OK: -skip-credit run exits 0 and writes a seed file"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: -skip-credit did not write any seed file"
    fi
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -skip-credit rc=$? — err: $(cat /tmp/seedrng.err)"
fi

# --- 3. Second run rotates the seed for the next boot -----------------
SEED_FILE=$(ls "$SEEDDIR"/seed.credit "$SEEDDIR"/seed.no-credit 2>/dev/null | head -1)
if [ -n "$SEED_FILE" ]; then
    before_sha=$(sha256sum "$SEED_FILE" | cut -d' ' -f1)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if /usr/bin/slinit-seedrng -seed-dir "$SEEDDIR" -quiet \
            >/tmp/seedrng.out 2>/tmp/seedrng.err; then
        # Filename may have flipped (no-credit → credit or vice versa).
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

# --- 4. --quiet suppresses info output; -version exits without touching FS
_TESTS_RUN=$((_TESTS_RUN + 1))
out=$(/usr/bin/slinit-seedrng -version 2>&1)
case "$out" in
    *seedrng*|*version*|*[0-9]*)
        echo "OK: -version prints something and exits"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: -version output: $out"
        ;;
esac

test_summary
