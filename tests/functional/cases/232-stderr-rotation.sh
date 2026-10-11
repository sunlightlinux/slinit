#!/bin/sh
# 232-stderr-rotation — stderr-logfile-max-* rotates the stderr file on
# its own policy, independently of the stdout one.
#
# stderr-logfile (3.0.5) gave stderr its own file but not its own
# policy: the rotator configuration varied only the path, so a service
# quiet on stdout and noisy on stderr had to accept one compromise for
# both. The check below is that the two streams really diverge — a
# small stderr budget rotates while a large stdout budget does not.

SVC="test-errot"
OUT=/run/errot-out.log
ERR=/run/errot-err.log
rm -f "$OUT"* "$ERR"*

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
log-type = file
logfile = $OUT
logfile-max-size = 10485760
logfile-max-files = 4
stderr-logfile = $ERR
stderr-logfile-max-size = 4096
stderr-logfile-max-files = 3
command = /bin/sh -c 'i=0; while [ \$\$i -lt 400 ]; do echo "out-\$\$i"; echo "err-\$\$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" >&2; i=\$\$((i+1)); done; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service with two rotation policies started"

# Give the writer time to produce enough stderr to cross 4096 bytes.
_i=0
while [ "$_i" -lt 40 ]; do
    ls "$ERR".* >/dev/null 2>&1 && break
    sleep 0.25
    _i=$((_i + 1))
done

echo "INFO: stdout files: $(ls -1 "$OUT"* 2>/dev/null | tr '\n' ' ')"
echo "INFO: stderr files: $(ls -1 "$ERR"* 2>/dev/null | tr '\n' ' ')"

# The small stderr budget must have rotated: more than the live file.
_err_n=$(ls -1 "$ERR"* 2>/dev/null | wc -l)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_err_n" -gt 1 ]; then
    echo "OK: stderr rotated under its own 4096-byte budget ($_err_n files)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stderr did not rotate — $_err_n file(s), so the override did not apply"
fi

# The 10 MB stdout budget must NOT have rotated on this volume. That is
# the half that proves the policies are separate rather than both small.
_out_n=$(ls -1 "$OUT"* 2>/dev/null | wc -l)
assert_eq "$_out_n" "1" "stdout kept its own 10 MB budget and did not rotate"

slinitctl --system stop "$SVC" 2>/dev/null
rm -f "/etc/slinit.d/$SVC" "$OUT"* "$ERR"*

test_summary
