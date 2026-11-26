#!/bin/sh
# Test: socket-reuseport and stderr-logfile-rotate-time, by what they do.
#
# socket-reuseport: two instances of one template listen on the same TCP
# port. With the option both bind; without it the second fails with
# EADDRINUSE, which is the control showing the port is really shared.
#
# stderr-logfile-rotate-time: the stderr file is rotated on a timer, so a
# service writing to stderr leaves timestamped rotated files behind.

listeners() { # port in hex
    awk -v p=":$1" '$2 ~ p"$" && $4 == "0A"' /proc/net/tcp | wc -l
}

printf '%s\n' 'type = process' 'command = /bin/sleep 2841' 'restart = false' \
    'socket-listen = tcp:127.0.0.1:28401' 'socket-reuseport = yes' \
    > /etc/slinit.d/test-rp
printf '%s\n' 'type = process' 'command = /bin/sleep 2842' 'restart = false' \
    'socket-listen = tcp:127.0.0.1:28402' > /etc/slinit.d/test-norp

slinitctl --system start test-rp@1 >/dev/null 2>&1
slinitctl --system start test-rp@2 >/dev/null 2>&1
assert_service_state test-rp@1 STARTED "socket-reuseport: first instance started"
assert_service_state test-rp@2 STARTED "socket-reuseport: second instance on the same port started"
assert_eq "$(listeners 6EF1)" "2" "socket-reuseport: two listeners on port 28401"

slinitctl --system start test-norp@1 >/dev/null 2>&1
slinitctl --system start test-norp@2 >/dev/null 2>&1
assert_service_state test-norp@1 STARTED "without reuseport: first instance started"
assert_service_state test-norp@2 STOPPED "without reuseport: second instance cannot bind"
assert_eq "$(listeners 6EF2)" "1" "without reuseport: one listener on port 28402"

for s in test-rp@1 test-rp@2 test-norp@1; do slinitctl --system stop "$s" >/dev/null 2>&1; done

# --- stderr-logfile-rotate-time ---
printf '%s\n' '#!/bin/sh' 'while :; do echo tick >&2; sleep 0.2; done' > /tmp/284-ticker.sh
chmod +x /tmp/284-ticker.sh
mkdir -p /tmp/284-log
printf '%s\n' 'type = process' 'command = /tmp/284-ticker.sh' 'restart = false' \
    'stderr-logfile = /tmp/284-log/err.log' 'stderr-logfile-rotate-time = 2' \
    > /etc/slinit.d/test-errrot
slinitctl --system start test-errrot >/dev/null 2>&1
sleep 5
slinitctl --system stop test-errrot >/dev/null 2>&1
ls -l /tmp/284-log
_rot=$(ls /tmp/284-log | grep -c '^err\.log\.')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rot" -ge 1 ]; then
    echo "OK: stderr-logfile-rotate-time: $_rot rotated file(s) after 5s"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stderr-logfile-rotate-time: no rotated file after 5s"
fi
_first=$(ls /tmp/284-log | grep '^err\.log\.' | head -1)
assert_contains "$(cat "/tmp/284-log/$_first" 2>/dev/null)" "tick" \
    "the rotated file holds the service's stderr"

test_summary
