#!/bin/sh
# 32-credentials — `set-credential = NAME:VALUE` (literal) and
# `load-credential = NAME:PATH` (copy from disk) populate the per-service
# tmpfs at /run/credentials/<svc>/, mode 0700, with $CREDENTIALS_DIRECTORY
# exposed to the child. Both forms write files named NAME containing the
# value.

SVC="acceptance-test-creds"
SRC_FILE="/run/acceptance-test-creds.src"
SECRET_LITERAL="secret-literal-$(date +%s)"
SECRET_FROM_FILE="secret-from-file-$(date +%s)-$$"
MARK="/run/acceptance-test-creds.mark"

cleanup() {
    svc_remove "$SVC"
    rm -f "$SRC_FILE" "$MARK"
}
trap cleanup EXIT INT TERM

printf '%s' "$SECRET_FROM_FILE" > "$SRC_FILE"
chmod 0600 "$SRC_FILE"
rm -f "$MARK"

# slinit's config parser treats each setting as a single line — a
# multi-line `command =` block silently parses as only the first line.
#
# Two layers of escape for $CREDENTIALS_DIRECTORY: `\$\$` in the heredoc
# becomes `$$` in the service file; slinit's parser
# (expandEnvVarsForCommand, parser.go:1000) collapses `$$VAR` to literal
# `$VAR` since CREDENTIALS_DIRECTORY isn't in slinit's env at parse time;
# only at runtime does the child sh see and expand it.
svc_deploy "$SVC" <<EOF
type = process
set-credential = lit:$SECRET_LITERAL
load-credential = file:$SRC_FILE
command = /bin/sh -c 'echo "dir=\$\$CREDENTIALS_DIRECTORY" > $MARK; echo "lit=\$\$(cat \$\$CREDENTIALS_DIRECTORY/lit)" >> $MARK; echo "file=\$\$(cat \$\$CREDENTIALS_DIRECTORY/file)" >> $MARK; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -r "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $MARK not written"
    test_summary
    exit 1
fi
echo "OK: marker written"

_dir=$(awk -F= '/^dir=/ {print $2; exit}' "$MARK")
_lit=$(awk -F= '/^lit=/ {print $2; exit}' "$MARK")
_file=$(awk -F= '/^file=/ {print $2; exit}' "$MARK")

# CREDENTIALS_DIRECTORY must be set and point under /run/credentials/<svc>.
assert_eq "$_dir" "/run/credentials/$SVC" "CREDENTIALS_DIRECTORY path"
assert_eq "$_lit" "$SECRET_LITERAL" "set-credential literal contents"
assert_eq "$_file" "$SECRET_FROM_FILE" "load-credential file contents"

# Mount-side: /run/credentials/<svc> should be a tmpfs mount.
_TESTS_RUN=$((_TESTS_RUN + 1))
if mount | grep -qE "tmpfs on /run/credentials/$SVC( |$)"; then
    echo "OK: credentials dir is a tmpfs mount"
else
    # Older slinit may not advertise the mount in /proc/mounts via mount(8);
    # fall back to /proc/self/mountinfo for a stricter probe.
    if awk -v p="/run/credentials/$SVC" '$5==p {found=1} END{exit !found}' /proc/self/mountinfo; then
        echo "OK: credentials dir is a tmpfs mount (mountinfo)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: /run/credentials/$SVC is not a tmpfs mount"
    fi
fi

test_summary
