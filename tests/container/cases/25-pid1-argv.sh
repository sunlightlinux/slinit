# As a container's PID 1, slinit reads its own argv with the PID-1 rules
# (the scan a kernel command line gets: lenient, dinit's order), not with
# plain flag parsing. Two things a container's command line relies on:
#
#  - an empty value switches a string option off: `--syslog-socket=`
#    leaves /dev/log to the image (the PID-1 scan once ignored empty
#    values, so this, and slinit-nspawn's `--devtmpfs-path=`, did
#    nothing; 67bb49c, found by review 9);
#  - `-t NAME` picks the boot service, wherever it is on the line.
#
# The default (no flag) serving /dev/log is checked too, so the first
# check cannot pass for want of a socket that never appears anyway.

SVC=$(new_svcdir)
names=""
trap 'for n in $names; do ct_rm $n; done; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
EOF
cat > "$SVC/alt" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
EOF

N=slinit-ct-argv-default-$$; names="$names $N"
ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "default command line: boot reached STARTED"
"$RUNTIME" exec $N test -S /dev/log
check $? "default command line: slinit serves /dev/log"
ct_rm $N

N=slinit-ct-argv-nosyslog-$$; names="$names $N"
ct_start $N "$SVC" -- --syslog-socket=
ct_wait_ready $N 10
check $? "--syslog-socket=: boot reached STARTED"
"$RUNTIME" exec $N test -e /dev/log
[ $? -ne 0 ]
check $? "--syslog-socket= (empty) switches /dev/log off"
ct_rm $N

N=slinit-ct-argv-t-$$; names="$names $N"
ct_start $N "$SVC" -- -t alt
_i=0
until "$RUNTIME" exec $N slinitctl status alt 2>/dev/null | grep -q 'State: *STARTED' ||
      [ "$_i" -ge 50 ]; do sleep 0.2; _i=$((_i + 1)); done
"$RUNTIME" exec $N slinitctl status alt 2>/dev/null | grep -q 'State: *STARTED'
check $? "-t alt: alt is started as the boot service"
ct_rm $N

summary
