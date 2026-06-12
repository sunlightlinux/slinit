#!/bin/sh
# Test: systemd-style seccomp-bpf filter (#4).
# Validates:
#   - @system-service group is broad enough that an ordinary shell
#     service starts and runs normally;
#   - a denylist on ptrace actually blocks it (strace receives EPERM).

wait_for_service "seccomp-allowed" "STARTED" 15
wait_for_service "seccomp-blocked" "STARTED" 15

# Marker proves the allowed service's command body ran. If seccomp had
# wrongly killed it, no marker would be written.
[ -f /tmp/seccomp-allowed-marker ] && allowed=yes || allowed=no
assert_eq "$allowed" "yes" "@system-service allows ordinary shell to run"

# The blocked service's witness reports whether ptrace was rejected.
# "strace-unavailable" is treated as a soft skip (no probe binary);
# "ptrace-allowed" means our filter failed.
result=$(cat /tmp/seccomp-blocked-result 2>/dev/null)
case "$result" in
ptrace-blocked)
	assert_eq "ptrace-blocked" "ptrace-blocked" "denylist on ptrace returns EPERM"
	;;
strace-unavailable)
	echo "  note: strace not available in test VM — denylist EPERM path not exercised"
	;;
ptrace-allowed)
	assert_eq "$result" "ptrace-blocked" "denylist on ptrace must return EPERM"
	;;
*)
	assert_eq "$result" "ptrace-blocked" "unexpected probe result: $result"
	;;
esac

test_summary
