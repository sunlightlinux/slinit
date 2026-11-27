#!/bin/sh
# Test: starting a shared-logger producer again and again does not pile
# up pipe descriptors in slinit.
#
# Each start of a producer gets a fresh pipe from the shared logger's
# mux, and its write end goes to the child. Up to v4.0.3 slinit never
# closed its own copy after the fork, so the mux never saw EOF when the
# producer exited and every start left one more descriptor open until a
# garbage collection happened to run (93cd0da; 30 cycles left 33 open).
# The nested `slinit --user` runs with GOGC=off, so the count shows the
# leak instead of whenever the collector last ran.

R=/tmp/324
S="$R/svcs"
rm -rf "$R"; mkdir -p "$S"
printf '%s\n' 'type = internal' 'waits-for: lg' > "$S/boot"
# The logger stays up whatever its stdin is (v4.0.3 gives it EOF at once);
# the leak is counted in slinit's descriptors, not the logger's.
printf '%s\n' 'type = process' 'command = /bin/sh -c "cat > /dev/null; exec sleep 100000"' \
    'restart = false' > "$S/lg"
for t in process scripted; do
    case $t in
        process)  printf '%s\n' 'type = process' 'command = /bin/sh -c "echo hi; exec sleep 30"' > "$S/p-$t" ;;
        scripted) printf '%s\n' 'type = scripted' 'command = /bin/echo hi' 'stop-command = /bin/true' > "$S/p-$t" ;;
    esac
    printf '%s\n' 'restart = false' 'shared-logger = lg' 'depends-on: lg' >> "$S/p-$t"
done

GOGC=off setsid /sbin/slinit --user --socket-path "$R/sock" --services-dir "$S" \
    --log-file "$R/log" </dev/null >/dev/null 2>&1 &
NEST=$!
sl() { timeout 10 slinitctl --socket-path "$R/sock" "$@"; }
_e=0
until [ "$(sl status lg 2>/dev/null | awk '/State:/ {print $2}')" = STARTED ] ||
      [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
[ "$_e" -lt 20 ] || skip_case "nested slinit / shared logger did not come up: lg '$(sl status lg 2>&1 | awk '/State:/ {print $2}')', log: $(tail -3 "$R/log" | tr '\n' ' ')"
grep -q 'GOGC=off' "/proc/$NEST/environ" || skip_case "nested slinit lacks GOGC=off"

for t in process scripted; do
    sl start "p-$t" >/dev/null 2>&1; sl stop "p-$t" >/dev/null 2>&1   # warm up
    _f0=$(ls "/proc/$NEST/fd" | wc -l)
    i=0
    while [ "$i" -lt 30 ]; do
        sl start "p-$t" >/dev/null 2>&1
        sl stop "p-$t" >/dev/null 2>&1
        i=$((i + 1))
    done
    sleep 1
    _f1=$(ls "/proc/$NEST/fd" | wc -l)
    echo "$t producer: slinit fds $_f0 -> $_f1 over 30 start/stop cycles"
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$_f1" -le $((_f0 + 5)) ]; then
        echo "OK: $t producer: descriptors flat"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $t producer: slinit kept $((_f1 - _f0)) descriptors"
    fi
done

kill -9 "$NEST" 2>/dev/null
test_summary
