#!/bin/sh
# Test: three more ways a pin used to leave a service the shutdown had to
# wait out (each an emergency timeout with nothing to show for it).
#
#  A  c83cad8: a dependent started while its dependency is stopping
#     wants it back; with the dependency pinned stopped, the restart was
#     refused but nobody told the dependent, which waited for good.
#  B  54dd6e5: a service pinned while STARTING, with a dependency still
#     starting, was unpinned by the shutdown with the release of that
#     dependency still pending; the dependency then stayed up.
#  C  9bf0acc: a failed start dropped the failing service's own start
#     pin but not the copy its hard dependencies received, so the leaf
#     stayed pinned started.
#
# Each runs against its own nested `slinit --user`, and ends with a
# poweroff that must finish within 15 s.

nest_up() { # dir -> NEST, sl(), st()
    R="$1"; S="$R/svcs"
    setsid /sbin/slinit --user --socket-path "$R/sock" --services-dir "$S" \
        --log-file "$R/log" </dev/null >/dev/null 2>&1 &
    NEST=$!
    _e=0
    until [ "$(st boot)" = STARTED ] || [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
    [ "$(st boot)" = STARTED ] || skip_case "$R: nested slinit did not come up"
}
sl() { timeout 10 slinitctl --socket-path "$R/sock" "$@"; }
st() { sl status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }
nest_poweroff() { # tag
    _t0=$(date +%s)
    sl poweroff >/dev/null 2>&1
    _e=0
    while [ "$_e" -lt 30 ] && kill -0 "$NEST" 2>/dev/null; do sleep 0.5; _e=$((_e + 1)); done
    _alive=0; kill -0 "$NEST" 2>/dev/null && _alive=1
    echo "$1: shutdown $(( $(date +%s) - _t0 ))s, still running: $_alive"
    grep -i 'waiting for' "$R/log" 2>/dev/null | tail -1
    kill -9 "$NEST" 2>/dev/null
    assert_eq "$_alive" "0" "$1: the shutdown finishes within 15 s"
}
mkroot() { rm -rf "$1"; mkdir -p "$1/svcs"; printf '%s\n' 'type = internal' > "$1/svcs/boot"; }

# --- A: c83cad8 ---
A=/tmp/319a; mkroot $A
printf '%s\n' 'type = scripted' 'command = /bin/true' 'stop-command = /bin/sleep 3' \
    'restart = false' > $A/svcs/d
printf '%s\n' 'type = process' 'command = /bin/sleep 3191' 'restart = false' \
    'depends-on: d' > $A/svcs/t
nest_up $A
sl start d >/dev/null 2>&1
sl --no-wait --pin stop d >/dev/null 2>&1
sleep 0.5
[ "$(st d)" = STOPPING ] || skip_case "A: d is '$(st d)', not STOPPING"
sl --no-wait start t >/dev/null 2>&1
sleep 6
echo "A: d $(st d), t $(st t)"
_t=$(st t)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_t" != STARTING ]; then
    echo "OK: A: the dependent whose dependency's stop pin refused the start is not left waiting ($_t)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: A: t still STARTING after its dependency's stop pin refused to start it"
fi
sl stop t >/dev/null 2>&1
sl unpin d >/dev/null 2>&1
nest_poweroff A

# --- B: 54dd6e5 ---
B=/tmp/319b; mkroot $B
printf '%s\n' 'type = scripted' 'command = /bin/sleep 3' 'stop-command = /bin/true' \
    'restart = false' > $B/svcs/sc
printf '%s\n' 'type = process' 'command = /bin/sleep 3192' 'restart = false' \
    'depends-on: sc' > $B/svcs/in
nest_up $B
sl --no-wait --pin start in >/dev/null 2>&1
sleep 0.5
[ "$(st sc)" = STARTING ] && [ "$(st in)" = STARTING ] ||
    skip_case "B: sc '$(st sc)', in '$(st in)': both should be STARTING"
nest_poweroff B

# --- C: 9bf0acc ---
C=/tmp/319c; mkroot $C
printf '%s\n' 'type = internal' 'depends-on: mid' > $C/svcs/top
printf '%s\n' 'type = internal' 'depends-on: leaf' > $C/svcs/mid
printf '%s\n' 'type = internal' > $C/svcs/leaf
nest_up $C
sl start leaf >/dev/null 2>&1
sl --pin stop mid >/dev/null 2>&1
_o1=$(sl --pin start top 2>&1); _o2=$(sl start top 2>&1)
echo "C: start --pin top: $_o1 / start top: $_o2"
sl stop leaf >/dev/null 2>&1
sleep 1
assert_eq "$(st leaf)" "STOPPED" "C: two failed starts of top leave no pin on leaf"
nest_poweroff C

test_summary
