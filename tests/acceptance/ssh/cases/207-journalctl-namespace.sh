#!/bin/sh
# 207-journalctl-namespace — v2.1.11: --namespace tags events at
# the daemon (guardedSink.Handle stamps evt.Namespace when the
# sink was constructed with a namespace), --list-namespaces
# enumerates from the filesystem-suffix convention.

NS="acc-ns-$$"
NS_DIR_PERSIST="/var/log/slinit-journal.$NS"
NS_DIR_VOLATILE="/run/slinit-journal.$NS"
NS_PID="/run/slinit-journald.$NS.pid"
NS_SOCK="/run/slinit/events-$NS.sock"
NS_CTL="/run/slinit-journald.$NS.ctl"

cleanup() {
    [ -f "$NS_PID" ] && kill "$(cat "$NS_PID")" 2>/dev/null
    rm -f "$NS_PID" "$NS_SOCK" "$NS_CTL"
    rm -rf "$NS_DIR_PERSIST" "$NS_DIR_VOLATILE"
}
trap cleanup EXIT INT TERM

# --list-namespaces BEFORE our daemon exists — must not list NS.
# Note: we intentionally DO NOT pre-create the dirs — --list-
# namespaces scans filesystem, and pre-mkdir would falsely surface
# the NS before any daemon touched it. The daemon's
# OpenFileSinkWithFallback creates the dirs itself on first write.
_out=$(slinit-journalctl --list-namespaces 2>&1)
assert_not_contains "$_out" "$NS" "--list-namespaces empty before daemon starts"

# Spawn the namespaced daemon. --namespace suffixes the default
# paths automatically; we only supply --namespace and the format.
slinit-journald --namespace="$NS" --format=jsonl >/dev/null 2>&1 &
sleep 0.4

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$NS_PID" ]; then
    echo "OK: namespaced daemon wrote per-NS pid-file"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: daemon did not write $NS_PID"
    test_summary; return
fi

# --list-namespaces AFTER daemon exists.
_out=$(slinit-journalctl --list-namespaces 2>&1)
assert_contains "$_out" "$NS" "--list-namespaces surfaces namespace after daemon starts"

# Persistent dir now has journal files (daemon replayed backlog
# from slinit's ring buffer at startup).
_TESTS_RUN=$((_TESTS_RUN + 1))
_jf=$(ls "$NS_DIR_PERSIST"/*.jsonl 2>/dev/null | head -1)
if [ -n "$_jf" ]; then
    echo "OK: namespaced daemon produced journal file"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no journal file in $NS_DIR_PERSIST"
    test_summary; return
fi

# Every event in the persistent store carries the namespace tag.
_out=$(slinit-journalctl --file="$_jf" -o json -n 1 2>&1)
assert_contains "$_out" "\"namespace\":\"$NS\"" "event carries namespace tag"

test_summary
