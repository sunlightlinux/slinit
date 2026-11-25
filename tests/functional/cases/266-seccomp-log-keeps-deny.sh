#!/bin/sh
# 266-seccomp-log-keeps-deny: system-call-log only logs; a call that
# system-call-filter refuses stays refused. The log list was compiled into the
# filter program ahead of the deny branch, and SECCOMP_RET_LOG executes the
# call — so logging a denied call let it run.

cat > /run/266-probe.sh <<'PROBE'
#!/bin/sh
rm -rf "/run/zz266-$1"
if mkdir "/run/zz266-$1" 2>/dev/null; then echo made; else echo refused; fi > "/run/266-$1.result"
exec sleep 60
PROBE
rm -f /run/266-*.result
for s in denyonly denylogged; do slinitctl --system start "$s" > /dev/null 2>&1; done
wait_for_service "denyonly" "STARTED" 5
wait_for_service "denylogged" "STARTED" 5
n=0
while { [ ! -s /run/266-plain.result ] || [ ! -s /run/266-logged.result ]; } && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done

assert_eq "$(cat /run/266-plain.result 2>/dev/null)" "refused" "mkdir refused by system-call-filter"
assert_eq "$(cat /run/266-logged.result 2>/dev/null)" "refused" "still refused when system-call-log also lists it"

for s in denyonly denylogged; do slinitctl --system stop "$s" > /dev/null 2>&1; done
rm -rf /run/zz266-*
test_summary
