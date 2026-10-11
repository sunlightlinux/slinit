#!/bin/sh
# Test: protect-hostname blocks sethostname/setdomainname via seccomp.
#
# The authoritative reading is the SERVICE's own view of its filter,
# written from inside it, not an external /proc read. An external read
# races the runner's install and, worse, cannot tell a filter that was
# never installed from one that is not visible yet. This case used to
# depend on the external read and carried an assertion that could not
# fail: both branches of its `case` printed OK, so when CI caught the
# filter genuinely missing it printed "OK: seccomp installed" on the
# evidence that it was not — an empty stderr from `hostname`, which
# means the call SUCCEEDED.
#
# Empty stderr cannot distinguish the two outcomes either way: the deny
# filter's action is SIGSYS, so a blocked `hostname` is killed and
# prints nothing, exactly like one that worked. Only the exit status
# and the resulting hostname separate them, so the probe reports both.
#
# The self-report uses `grep | tr` rather than awk, to keep quoting out
# of it. An awk program needs braces, field refs and inner quotes, and
# all three are hazards here: `$1` is eaten by slinit's parse-time $VAR
# expansion, and an escaped `\"` inside `sh -c '...'` reaches awk as a
# literal backslash because single quotes do not process it. The first
# two attempts at this line produced an empty report while the filter
# was working perfectly, which is the failure mode to avoid in a probe
# whose whole job is to be believed.

SVC="test-phn"
OUT="/tmp/functional-phn-out"
SELF="/tmp/functional-phn-self"
_orig_host=$(hostname)
rm -f "$OUT" "$SELF"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
protect-hostname = yes
command = /bin/sh -c 'grep -e Seccomp: -e NoNewPrivs: /proc/self/status | tr "\n" " " > $SELF; if hostname functional-probe 2>$OUT; then echo " sethostname=succeeded" >> $SELF; else echo " sethostname=refused" >> $SELF; fi; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

# The probe writes its self-report before touching the hostname, so
# wait for the file rather than for a /proc field to change.
_i=0
while [ "$_i" -lt 50 ]; do
    [ -s "$SELF" ] && grep -q "sethostname=" "$SELF" 2>/dev/null && break
    sleep 0.2
    _i=$((_i + 1))
done
_self=$(tr -d '\n' < "$SELF" 2>/dev/null)
echo "INFO: probe recorded: ${_self:-<nothing>}"

# What slinit reports externally, for comparison only. A disagreement
# with the probe's own view is itself worth seeing, and is why this is
# INFO and not an assertion.
_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
echo "INFO: external view: pid=${_pid:-none}" \
     "comm=$(cat "/proc/$_pid/comm" 2>/dev/null)" \
     "$(awk '/^Seccomp:|^NoNewPrivs:/ { printf "%s=%s ", $1, $2 }' "/proc/$_pid/status" 2>/dev/null)"

# The service's own view of its filter. Mode 2 is SECCOMP_MODE_FILTER.
_probe_seccomp=$(printf '%s' "$_self" | sed -n 's/.*Seccomp:[[:space:]]*\([0-9]*\).*/\1/p')
assert_eq "$_probe_seccomp" "2" "service sees its own seccomp filter (mode 2)"

# The call itself. A deny filter kills with SIGSYS, so `hostname` must
# not report success — this is the assertion the old one only pretended
# to make.
_probe_call=$(printf '%s' "$_self" | sed -n 's/.*sethostname=\([a-z]*\).*/\1/p')
assert_eq "$_probe_call" "refused" "sethostname did not succeed inside the guarded service"
echo "INFO: probe stderr: '$(cat "$OUT" 2>/dev/null)'"

# Host hostname must remain unchanged. Restore best-effort BEFORE
# asserting so a lost filter does not leak the mutation into the next
# case; the assertion still fires because it compares the value
# observed before the restore.
_after_host=$(hostname)
if [ "$_after_host" != "$_orig_host" ]; then
    hostname "$_orig_host" 2>/dev/null || true
fi
assert_eq "$_after_host" "$_orig_host" "host hostname untouched by guarded service"

test_summary
