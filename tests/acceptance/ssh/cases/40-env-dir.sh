#!/bin/sh
# 40-env-dir — `env-dir = /path/to/dir` injects one env var per file in
# the directory: filename → name, file contents → value. runit's envdir
# convention. pkg/process/envfile.go:106 ReadEnvDir.

SVC="acceptance-test-envdir"
ENVDIR="/run/acceptance-test-envdir.d"
MARK="/run/acceptance-test-envdir.mark"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$ENVDIR"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

rm -rf "$ENVDIR"
mkdir -p "$ENVDIR"
printf 'bar' > "$ENVDIR/FOO"
printf 'qux' > "$ENVDIR/BAZ"
# Hidden file must be ignored (envfile.go:119).
printf 'visible' > "$ENVDIR/.hidden"
chmod 0644 "$ENVDIR"/*

rm -f "$MARK"

# Same $$VAR escape rules as 30/31/32 — runtime env refs need $$.
svc_deploy "$SVC" <<EOF
type = process
env-dir = $ENVDIR
command = /bin/sh -c 'echo "FOO=\$\$FOO BAZ=\$\$BAZ HIDDEN=\$\$hidden" > $MARK; while :; do sleep 60; done'
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

_line=$(cat "$MARK")
# HIDDEN must be empty — hidden files are skipped by ReadEnvDir.
assert_eq "$_line" "FOO=bar BAZ=qux HIDDEN=" "env-dir injected FOO/BAZ; hidden file skipped"

test_summary
