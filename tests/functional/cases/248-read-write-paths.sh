#!/bin/sh
# 248-read-write-paths: a path carved out of protect-system = strict is writable,
# probed from inside the service, as nsenter does not see what the service sees.

mkdir -p /var/zzrw
rm -f /run/zzrw.result
slinitctl --system start rwprobe > /dev/null 2>&1
wait_for_service "rwprobe" "STARTED" 5
n=0
while [ ! -s /run/zzrw.result ] && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
out=$(cat /run/zzrw.result 2>/dev/null)
assert_contains "$out" "rw-path-writable" "read-write-paths is writable under strict"
assert_contains "$out" "etc-readonly" "and the rest stays read-only"
slinitctl --system stop rwprobe > /dev/null 2>&1

test_summary
