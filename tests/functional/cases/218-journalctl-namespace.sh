#!/bin/sh
# 218-journalctl-namespace — v2.1.11 Sprint 3: --namespace on
# slinit-journald routes to a per-NS path family + tags every
# incoming event with the namespace. slinit-journalctl
# --list-namespaces enumerates by filesystem scan; --namespace=NS
# filters events by tag.

wait_for_service "boot" "STARTED" 15

NS="funcns"
NS_DIR="/var/log/slinit-journal.$NS"
NS_VOLATILE="/run/slinit-journal.$NS"
NS_PID="/run/slinit-journald.$NS.pid"
NS_SOCK="/run/slinit/events-$NS.sock"
NS_CTL="/run/slinit-journald.$NS.ctl"
LOG=/tmp/ns-daemon.log

# Clean slate — must NOT pre-create the dirs, --list-namespaces
# scans filesystem and pre-mkdir would falsely surface the NS.
rm -rf "$NS_DIR" "$NS_VOLATILE"
rm -f "$NS_PID" "$NS_SOCK" "$NS_CTL"

# --list-namespaces before daemon exists → NS not listed.
_out=$(slinit-journalctl --list-namespaces 2>&1)
assert_not_contains "$_out" "$NS" "--list-namespaces empty before daemon"

# NOW create parent dirs so the daemon's OpenFileSinkWithFallback
# has somewhere to write. The daemon MkdirAll's them anyway; we
# just make sure /var/log exists in the slim guest rootfs.
mkdir -p /var/log /run

# Spawn namespaced daemon. --namespace auto-suffixes every
# default path (dir/volatile/socket/pid/admin).
slinit-journald --namespace="$NS" --format=jsonl >/dev/null 2>"$LOG" &
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$NS_PID" ] && kill -0 "$(cat "$NS_PID")" 2>/dev/null; then
    echo "OK: namespaced daemon started (pid $(cat "$NS_PID"))"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: namespaced daemon did not start"
    tail "$LOG"
    test_summary; return
fi

# --list-namespaces after daemon exists → NS listed.
_out=$(slinit-journalctl --list-namespaces 2>&1)
assert_contains "$_out" "$NS" "--list-namespaces surfaces $NS"

# A namespaced daemon in the guest has no upstream producer
# emitting into its custom /run/slinit/events-<NS>.sock (the
# guest's slinit publishes to the DEFAULT socket, not this
# namespaced one). Backlog replay via --control-socket can
# populate the persistent store on ceres because the ring
# buffer has live events; in this minimal VM the daemon may
# have started too fast for the replay to land bytes on disk.
# Test what IS deterministic: the daemon banner names the
# --namespace value AND the enumeration surfaces the NS.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "$NS_SOCK" "$LOG"; then
    echo "OK: daemon banner references the per-NS events socket ($NS_SOCK)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: daemon didn't announce per-NS socket path"
    cat "$LOG"
fi

# Simulate an event with the namespace tag directly and verify
# the client-side --namespace filter drops non-matching events.
# Uses --file with a synthetic JSONL — this exercises the same
# filter code path the on-daemon test would, without depending
# on the guest's event-bus wiring.
SYNTH=/tmp/ns-synth.jsonl
cat > "$SYNTH" <<EOF
{"ts":100,"msg":"in-namespace event","prio":6,"unit":"probe","namespace":"$NS"}
{"ts":200,"msg":"default namespace event","prio":6,"unit":"probe"}
EOF

_n_match=$(slinit-journalctl --file="$SYNTH" --namespace="$NS" -o cat 2>&1 | wc -l)
assert_eq "$_n_match" "1" "--namespace=$NS matches exactly the tagged event"

_n_other=$(slinit-journalctl --file="$SYNTH" --namespace=nope-$$ -o cat 2>&1 | wc -l)
assert_eq "$_n_other" "0" "--namespace=<unknown> drops all events"

kill "$(cat "$NS_PID")" 2>/dev/null

test_summary
