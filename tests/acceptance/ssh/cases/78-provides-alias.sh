#!/bin/sh
# 78-provides-alias — `provides = <alias>` makes the service queryable
# under a second name.
#
# pkg/service/set.go: AddService registers the canonical name in records
# AND the alias in a separate aliases map. FindService falls back to
# aliases when the canonical lookup misses, so every CLI verb that
# accepts a name (status, start, stop) works with either spelling.
#
# Dinit-parity use case: a logger service named "socklog" provides the
# "syslog" capability — consumer services depend-on/waits-for "syslog"
# without caring which logger is running today.

CANON="acceptance-test-prov-real"
ALIAS="acceptance-test-prov-alias"
CONSUMER="acceptance-test-prov-consumer"
CFILE="/etc/slinit.d/$CANON"
CONFILE="/etc/slinit.d/$CONSUMER"
MARKER="/tmp/acceptance-prov.mark"

cleanup() {
    slinitctl --system stop "$CONSUMER" 2>/dev/null
    slinitctl --system stop "$CANON" 2>/dev/null
    slinitctl --system unload "$CONSUMER" 2>/dev/null
    slinitctl --system unload "$CANON" 2>/dev/null
    rm -f "$CFILE" "$CONFILE" "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup

cat > "$CFILE" <<EOF
type = process
command = /bin/sh -c 'touch $MARKER; exec sleep 600'
provides = $ALIAS
restart = false
EOF

# Consumer depends on the ALIAS name, not the canonical one. If the
# alias machinery works, the consumer pulls the real service up.
cat > "$CONFILE" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
depends-on: $ALIAS
restart = false
EOF

# --- Probe 1: slinit-check accepts `provides = <name>` --------------
_chk=$(slinit-check -d /etc/slinit.d "$CANON" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinit-check accepts the provides directive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: parser rejected provides:"; echo "$_chk" | sed 's/^/  | /'
fi

# --- Probe 2: status queries the canonical service via its alias ----
slinitctl --system start "$CANON" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _st=$(slinitctl --system status "$CANON" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: $CANON STARTED (canonical name lookup)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $CANON stuck at '$_st'"
    test_summary
    exit 1
fi

# Query via the alias.
_alias_st=$(slinitctl --system status "$ALIAS" 2>/dev/null | awk '/State:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_alias_st" = "STARTED" ]; then
    echo "OK: status via alias name resolves to the canonical service"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: alias status returned '$_alias_st' (expected STARTED)"
fi

# --- Probe 3: depends-on by alias actually pulls the real one up ----
slinitctl --system start "$CONSUMER" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _cst=$(slinitctl --system status "$CONSUMER" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_cst" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_cst" = "STARTED" ]; then
    echo "OK: consumer STARTED via depends-on: <alias>"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: consumer stuck at '$_cst' (alias dep didn't resolve?)"
fi

# Stopping the canonical service via the alias name brings it down too.
slinitctl --system stop "$CONSUMER" >/dev/null 2>&1
sleep 1
slinitctl --system stop "$ALIAS" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _st=$(slinitctl --system status "$CANON" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STOPPED" ] && break
    sleep 1
    _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STOPPED" ]; then
    echo "OK: stop via alias brought the canonical service down"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stop via alias didn't reach STOPPED (state='$_st')"
fi

test_summary
