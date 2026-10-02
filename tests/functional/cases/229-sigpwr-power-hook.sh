#!/bin/sh
# Test: SIGPWR + /run/powerstatus — the sysvinit UPS contract.
#
# Only a real boot proves this. The unit tests cover reading the status
# file and running the hook, but whether PID 1 actually *receives* SIGPWR
# depends on signal.Notify having claimed it before anything else runs,
# and that is exactly what cannot be checked without being PID 1. An
# unclaimed SIGPWR is discarded by the Go runtime in silence, so the
# failure mode here is "nothing happens" with no error anywhere.
#
# Semantics under test: pkg/eventloop claims SIGPWR, pkg/shutdown reads
# one byte of /run/powerstatus, removes it, and runs the power hook with
# failing|ok|low. slinit itself must do nothing else — see the last
# sub-case, which is the one worth having.

HOOK=/etc/slinit/power-hook
SAW=/tmp/229-saw

mkdir -p /etc/slinit
cat > "$HOOK" <<'EOF'
#!/bin/sh
printf '%s\n' "$1" >> /tmp/229-saw
EOF
chmod 755 "$HOOK"

# `kill -s PWR` first; busybox's signal-name table is not guaranteed to
# carry PWR, and 30 is SIGPWR on x86_64/arm (it differs on mips/alpha,
# neither of which this VM is).
send_pwr() {
    kill -s PWR 1 2>/dev/null || kill -30 1
}

# --- Sub-case A: power restored ---------------------------------------
printf 'O' > /run/powerstatus
send_pwr
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(head -1 "$SAW" 2>/dev/null)" = "ok" ]; then
    echo "OK: SIGPWR with 'O' reached the hook as 'ok'"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: hook did not see 'ok' (saw: '$(cat "$SAW" 2>/dev/null)')"
fi

# The status is an event: reading it consumes the file, so a second
# signal cannot replay a power change that was already handled.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /run/powerstatus ]; then
    echo "OK: /run/powerstatus was consumed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /run/powerstatus survived the read"
fi

# --- Sub-case B: missing file means failing ---------------------------
# sysvinit treats anything that is not F/O/L — a missing file included —
# as a power failure, because being wrong in that direction is cheap.
send_pwr
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(sed -n '2p' "$SAW" 2>/dev/null)" = "failing" ]; then
    echo "OK: SIGPWR with no status file reported 'failing'"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected 'failing' on line 2 (saw: '$(cat "$SAW" 2>/dev/null)')"
fi

# --- Sub-case C: a low battery must NOT shut the machine down ---------
# The policy decision, asserted where it can actually be observed. If
# slinit ever grows an automatic poweroff on 'L', this case stops the
# whole VM and the harness reports it as a lost result rather than a
# failed assertion — which is itself the signal.
printf 'L' > /run/powerstatus
send_pwr
sleep 3

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(sed -n '3p' "$SAW" 2>/dev/null)" = "low" ]; then
    echo "OK: SIGPWR with 'L' reached the hook as 'low'"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected 'low' on line 3 (saw: '$(cat "$SAW" 2>/dev/null)')"
fi

# PID 1 is alive and still answering, after three power events including
# a flat battery.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system list >/dev/null 2>&1; then
    echo "OK: slinit still running and responsive after a low-battery SIGPWR"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: PID 1 is gone or unresponsive after SIGPWR"
fi

rm -f "$HOOK" "$SAW"
test_summary
