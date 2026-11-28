#!/bin/sh
# Test: a systemd unit's Environment= and EnvironmentFile= reach its
# ExecStart='s variables when it runs (4.1).
#
# $OPTS, from the env-file, is split into words; ${X}, from
# Environment=, is one argument; Y reaches the process's environment.
# Before 4.1 slinit expanded them when it loaded the file, from its own
# environment: both came out empty, and Environment= was dropped.

printf 'OPTS=-a -b\n' > /etc/test-340.env
slinitctl --system start test-340 >/dev/null 2>&1
wait_for_service test-340 "STARTED" 10
sleep 1
assert_eq "$(cat /run/test-340.args 2>/dev/null)" "[-a][-b][two words][inline]" "the command got the service's variables"
slinitctl --system stop test-340 >/dev/null 2>&1

test_summary
