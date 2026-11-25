#!/bin/sh
# Test: protect-kernel-modules refuses init_module/finit_module/
# delete_module via seccomp.
#
# The probe has to reach the syscall. It used `modprobe -n dummy`, a dry
# run that loads nothing, and accepted any stderr at all as proof — so the
# case passed whether or not anything was filtered. Now insmod hands a file
# that is not a module to the kernel: unfiltered (the control, run here as
# root) the kernel itself rejects it with "Exec format error" / "Invalid
# module format"; under the filter the call never reaches it and fails with
# EPERM.

SVC="test-pkm"
OUT="/tmp/functional-pkm-out"
JUNK="/tmp/functional-pkm-junk.ko"
printf 'not a kernel module\n' > "$JUNK"

_ctl=$(insmod "$JUNK" 2>&1)
case "$_ctl" in
    *"not permitted"*) skip_case "module loading refused here even unfiltered ($_ctl)" ;;
esac
echo "control (unfiltered): $_ctl"

rm -f "$OUT"
cat > "/etc/slinit.d/$SVC" <<EOF
type = process
protect-kernel-modules = yes
command = /bin/sh -c 'insmod $JUNK 2>$OUT; echo rc=\$\$? >> $OUT; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"
n=0
while ! grep -q '^rc=' "$OUT" 2>/dev/null && [ "$n" -lt 30 ]; do sleep 0.1; n=$((n + 1)); done
_err=$(cat "$OUT" 2>/dev/null)
echo "in the service: $_err"
assert_contains "$_err" "not permitted" "init_module refused by the filter (EPERM), not by the kernel"

slinitctl --system stop "$SVC" > /dev/null 2>&1
rm -f "$JUNK"
test_summary
