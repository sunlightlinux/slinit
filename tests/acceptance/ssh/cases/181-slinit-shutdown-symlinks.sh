#!/bin/sh
# 181-slinit-shutdown-symlinks — the shutdown utility is invocable via
# multiple names (slinit-reboot / slinit-halt / slinit-soft-reboot)
# through symlinks that resolve to slinit-shutdown. Verify the symlinks
# exist and target the right binary. No actual shutdown fires.

TARGET_BIN="slinit-shutdown"

# All shipped symlinks. /sbin symlinks (halt/poweroff/reboot) are the
# SysV compat set for slinit itself; we check the slinit-* variants
# that ship alongside slinit-shutdown.
for name in slinit-reboot slinit-halt slinit-soft-reboot; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _path=$(command -v "$name" 2>/dev/null)
    if [ -z "$_path" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $name not on PATH"
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

# Also verify slinit-shutdown itself is present.
_TESTS_RUN=$((_TESTS_RUN + 1))
if command -v slinit-shutdown >/dev/null 2>&1; then
    echo "OK: slinit-shutdown present on PATH"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-shutdown missing from PATH"
fi

test_summary
