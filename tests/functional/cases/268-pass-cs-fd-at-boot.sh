#!/bin/sh
# 268-pass-cs-fd-at-boot: options = pass-cs-fd actually works for a service
# started at boot — slinitctl --use-passed-cfd over the handed-down fd.
# 194 only checks the directive parses. The bug fixed tonight (the server-end
# hook wired after the boot services were started) is a race this VM's boot
# does not lose: this case passes on v4.0.2 too, and the deterministic
# reproduction is cmd/slinit's TestPassCSFDAtBoot.

n=0
while [ ! -s /run/268.out ] && [ "$n" -lt 100 ]; do sleep 0.1; n=$((n + 1)); done
out=$(cat /run/268.out 2>/dev/null)
echo "$out" | tail -3
assert_contains "$out" "rc=0" "slinitctl --use-passed-cfd works from a boot-started service"
assert_contains "$out" "csuser" "and lists the services"

test_summary
