#!/bin/sh
# 181-slinit-shutdown-symlinks — slinit-shutdown ships as a standalone
# binary; the slinit-halt / slinit-reboot / slinit-soft-reboot symlinks
# are optional (installers may or may not create them). Verify what's
# present and skip cleanly when the optional aliases aren't installed.

TARGET_BIN="slinit-shutdown"

# slinit-shutdown itself must be present.
_TESTS_RUN=$((_TESTS_RUN + 1))
if command -v slinit-shutdown >/dev/null 2>&1; then
    echo "OK: slinit-shutdown present on PATH"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-shutdown missing from PATH"
    test_summary
    return
fi

# Optional aliases — skip individually when absent; when present,
# check they resolve to slinit-shutdown.
for name in slinit-reboot slinit-halt slinit-soft-reboot; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _path=$(command -v "$name" 2>/dev/null)
    if [ -z "$_path" ]; then
        echo "  note: $name symlink not installed (optional)"
        continue
    fi
    _tgt=$(readlink -f "$_path")
    case "$_tgt" in
        */${TARGET_BIN})
            echo "OK: $name → $_tgt"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: $name resolved to $_tgt (expected $TARGET_BIN)"
            ;;
    esac
done

test_summary
