#!/bin/sh
# 46-protect-home-proc — three sandbox knobs verified in one service:
#
#   protect-home = yes        — /home + /root + /run/user re-bound as
#                                an empty tmpfs in the service's mnt-ns
#                                (pkg/process/exec.go:583 → sandbox wrapper).
#   protect-proc = invisible  — /proc mounted with hidepid=invisible:
#                                processes outside the service's mnt-ns
#                                are hidden from the child.
#   inaccessible-paths = ...  — bind-mounts a tmpfs file with mode 000
#                                over each target.
#
# Probe: list /home (must be empty), stat host PID 1 from inside the
# child's /proc (must fail with ENOENT under hidepid=invisible), stat
# the inaccessible target (must fail with EACCES).

SVC="acceptance-test-sandbox-hp"
MARK="/run/acceptance-test-sandbox-hp.log"
INACC="/etc/acceptance-test-inacc"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK" "$INACC"
}
trap cleanup EXIT INT TERM

rm -f "$MARK" "$INACC"
: > "$MARK"
chmod 0666 "$MARK"

# Pre-create the inaccessible target so the bind-mount source exists.
echo "TARGET_HOST_DATA" > "$INACC"
chmod 0644 "$INACC"

# Multi-probe script in a single command — slinit's parser only
# consumes the first line of a value, so probes are squashed with
# `;` separators and write straight to the marker (no /tmp staging).
# Each probe section is bracketed by '---' so the assertions below can
# slice the output. cat over a 000-mode bind-mount returns "Permission
# denied"; reading /proc/1 with hidepid=invisible from a different
# pid-ns context returns "No such file or directory".
# run-as=nobody is the key here: protect-proc=invisible (hidepid=2)
# only restricts non-root readers; root sees all PIDs and ignores
# 000-mode bind mounts (DAC override). The whole hardening cluster
# is built around dropping privileges first, then layering mounts.
svc_deploy "$SVC" <<EOF
type = process
run-as = nobody
protect-home = yes
protect-proc = invisible
inaccessible-paths = $INACC
command = /bin/sh -c '{ echo "--home--"; ls -A /home; echo "--proc1--"; ls /proc/1/comm 2>&1; echo "--inacc--"; cat $INACC 2>&1; echo "--end--"; } > $MARK; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

sleep 1

# Marker should contain three probes concatenated. Empty /home (just
# newlines), error trying to access /proc/1, error trying to read
# $INACC (Permission denied because the bind-mounted file is 000).
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -r "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $MARK not readable"
    test_summary
    exit 1
fi
_log=$(cat "$MARK")
echo "$_log" | sed 's/^/  log: /'

# Slice each probe section between its '--name--' marker and the next.
_home_section=$(awk '/^--home--/{p=1; next} /^--proc1--/{p=0} p {print}' "$MARK")
_proc1_section=$(awk '/^--proc1--/{p=1; next} /^--inacc--/{p=0} p {print}' "$MARK")
_inacc_section=$(awk '/^--inacc--/{p=1; next} /^--end--/{p=0} p {print}' "$MARK")

# protect-home: bind-mounted tmpfs over /home is empty for the child.
assert_eq "$_home_section" "" "protect-home: /home appears empty inside ns"

# protect-proc=invisible: reading /proc/1/comm from the child must
# fail (the child sits in a different pid-ns scope; hidepid hides
# host PIDs). Accept either ENOENT or EACCES error text.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_proc1_section" in
    *"No such file"*|*"Permission denied"*)
        echo "OK: protect-proc invisible (host PID 1 hidden, got: $_proc1_section)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: protect-proc didn't hide host PID 1: $_proc1_section"
        ;;
esac

# inaccessible-paths: for a regular file slinit binds /dev/null over
# the target (sandbox.go:256), so cat returns empty + exit 0. For a
# directory it'd be tmpfs mode 0000 + EACCES. Either way the original
# content must NOT leak — the test only cares about confidentiality.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_inacc_section" in
    *"TARGET_HOST_DATA"*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: inaccessible-paths leaked content: $_inacc_section"
        ;;
    *)
        echo "OK: inaccessible-paths hid the content (got: '$_inacc_section')"
        ;;
esac

test_summary
