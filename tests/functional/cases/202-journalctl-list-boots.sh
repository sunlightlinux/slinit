#!/bin/sh
# 202-journalctl-list-boots — v2.1.0: `--list-boots` enumerates
# the boot IDs the ring buffer covers. In Phase 2 the buffer
# holds one boot's worth of events, so the expected output is
# exactly one row with index 0, a 32-hex boot ID, and a start-end
# timestamp range.

wait_for_service "boot" "STARTED" 15

_out=$(slinit-journalctl --list-boots 2>&1)

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '^\s*0 [a-f0-9]{32} '; then
    echo "OK: --list-boots row starts with '0 <32-hex>'"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --list-boots output shape wrong: $_out"
fi

# Boot ID from --list-boots must match the _boot_id field of an
# actual event JSON payload — proves the enumeration is grounded
# in the same buffer state the query path sees.
_id_list=$(echo "$_out" | awk 'NR==1 {print $2}')
_id_event=$(slinit-journalctl -o json -n 1 2>&1 \
              | grep -oE '"_boot_id":"[a-f0-9]+"' \
              | head -1 | cut -d'"' -f4)
assert_eq "$_id_list" "$_id_event" "--list-boots boot ID matches event's _boot_id"

# Systemd shortcut: `-b` bare / `--boot` / `-b0` all point at
# current boot. All three should succeed and return events.
for _flag in '-b' '--boot' '-b0'; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _n=$(slinit-journalctl $_flag -n 3 2>&1 | wc -l)
    if [ "$_n" -ge 1 ]; then
        echo "OK: $_flag returned $_n line(s)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_flag returned empty output"
    fi
done

test_summary
