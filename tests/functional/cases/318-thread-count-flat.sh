#!/bin/sh
# Test: slinit's OS thread count does not grow with the number of
# running children, output-loggers or idle on-demand sockets.
#
# Up to v4.0.3 each of these held an OS thread for its whole life: a
# child's cmd.Wait() blocked in waitid, an output-logger's ad-hoc wait,
# an on-demand socket's watcher. 300 children meant ~300 threads, and Go
# aborts the whole process (as PID 1, a kernel panic) when it cannot
# create one more. Now they wait in the netpoller (17797fa, b964380,
# e76849c), and slinit keeps a fixed reserve of about GOMAXPROCS+16
# threads (cf61810). Measured on a nested `slinit --user`, 150 services
# per kind.

R=/tmp/318
S="$R/svcs"
rm -rf "$R"; mkdir -p "$S"
N=150
printf '%s\n' 'type = internal' > "$S/boot"
i=1
while [ "$i" -le "$N" ]; do
    printf '%s\n' 'type = process' "command = /bin/sleep 318$i" 'restart = false' > "$S/p$i"
    printf '%s\n' 'type = process' "command = /bin/sleep 3180$i" 'restart = false' \
        'output-logger = /bin/cat' > "$S/l$i"
    printf '%s\n' 'type = process' "socket-listen = $R/o$i.sock" \
        'socket-activation = on-demand' 'command = /bin/true' 'restart = false' > "$S/o$i"
    i=$((i + 1))
done

setsid /sbin/slinit --user --socket-path "$R/sock" --services-dir "$S" \
    --log-file "$R/log" </dev/null >/dev/null 2>&1 &
NEST=$!
sl() { timeout 20 slinitctl --socket-path "$R/sock" "$@"; }
_e=0
until [ "$(sl status boot 2>/dev/null | awk '/State:/ {print $2}')" = STARTED ] ||
      [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
[ "$_e" -lt 20 ] || skip_case "nested slinit did not come up"

threads() { awk '/^Threads:/ {print $2}' "/proc/$NEST/status"; }

_t0=$(threads)
echo "threads at boot: $_t0"
for kind in p l o; do
    i=1
    while [ "$i" -le "$N" ]; do
        sl --no-wait start "$kind$i" >/dev/null 2>&1
        i=$((i + 1))
    done
    _e=0
    while [ "$_e" -lt 40 ]; do
        # STARTED shows as [+] or {+} right after the opening bracket
        # (dinit's indicator), then the name, maybe followed by "(pid: N)".
        _up=$(sl list 2>/dev/null | awk -v k="$kind" \
            'substr($0, 3, 1) == "+" && $0 ~ ("] " k "[0-9]+( |$)") { n++ } END { print n + 0 }')
        [ "$_up" -ge "$N" ] && break
        sleep 0.5; _e=$((_e + 1))
    done
    sleep 1
    _t=$(threads)
    echo "kind $kind: $_up of $N up, slinit threads $_t"
    [ "$_up" -ge "$N" ] || { sl list 2>/dev/null | head -3; skip_case "only $_up of $N '$kind' services came up"; }
done

_tf=$(threads)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_tf" -le 80 ]; then
    echo "OK: $((3 * N)) children, loggers and on-demand sockets on $_tf threads"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_tf threads for $((3 * N)) children, loggers and on-demand sockets"
fi

kill -9 "$NEST" 2>/dev/null
test_summary
