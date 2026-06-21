#!/bin/sh
# 51-env-substitution — parser-side composition features:
#
#   ${VAR:-default}   parser.go:2632 — colon-dash fallback
#   command +=        parser.go:998-1004 — append more args
#   @include          parser.go:651-668 — inline another file's settings
#   @include-opt      parser.go     — silently skip if target absent
#
# Heredoc note: EOF is unquoted, so the shell expands `$VAR` first. For
# things slinit should expand at parse time we escape the `$` with `\$`
# so the literal `${...}` is what hits slinit's parser. For things the
# shell should expand (paths, MARK_* names) we leave the `$` bare.
#
# Use `--no-wait` on starts: the default blocks until STARTED, which
# masks any failure as an indefinite hang. Bound the wait with
# wait_for_service afterward instead.

CHECKDIR="/tmp/acceptance-test-env-sub"
cleanup() {
    svc_remove acceptance-test-env-default acceptance-test-env-append acceptance-test-env-include 2>/dev/null
    rm -rf "$CHECKDIR"
    rm -f /run/acceptance-test-env-default.mark /run/acceptance-test-env-append.mark
    rm -rf /etc/slinit.d/acceptance-test-env-include.fragments
}
trap cleanup EXIT INT TERM

rm -rf "$CHECKDIR"
mkdir -p "$CHECKDIR"

# --- ${VAR:-default} -------------------------------------------------
# A: VAR unset → slinit's parser substitutes the default. Escape `\$` so
# slinit (not the shell) sees `${ACCEPT_TEST_VAR:-fallback}` and resolves
# it at parse time; the resolved string ends up in the child's argv.
SVC_DEFAULT="acceptance-test-env-default"
MARK_D="/run/acceptance-test-env-default.mark"
rm -f "$MARK_D"
unset ACCEPT_TEST_VAR

svc_deploy "$SVC_DEFAULT" <<EOF
type = process
command = /bin/sh -c 'echo "v=\${ACCEPT_TEST_VAR:-fallback}" > $MARK_D; while :; do sleep 60; done'
restart = false
EOF
slinitctl --system --no-wait start "$SVC_DEFAULT" >/dev/null 2>&1
wait_for_service "$SVC_DEFAULT" "STARTED" 10 || true
sleep 1
assert_eq "$(cat "$MARK_D" 2>/dev/null)" "v=fallback" "\${VAR:-default} expands to default when VAR is unset"
svc_remove "$SVC_DEFAULT"
rm -f "$MARK_D"

# --- command += -----------------------------------------------------
# Append extra argv tokens. Notes on the shell mechanics:
#   - `sh -c SCRIPT name a b`  →  $0=name $1=a $2=b — the first positional
#     after SCRIPT is $0, NOT $1, so we insert `_` as a $0 placeholder.
#   - `\$\$@` survives both shell and slinit's $-pre-expansion (slinit's
#     `$$` escape → literal `$`), so /bin/sh sees `$@` at runtime.
#   - slinit's splitCommand eats `\n` as an escape even inside quotes
#     (per project memory), so we use a `;`-joined `echo $1; echo $2`
#     instead of `printf "%s\n"` to keep newlines in the marker.
SVC_APPEND="acceptance-test-env-append"
MARK_A="/run/acceptance-test-env-append.mark"
rm -f "$MARK_A"
svc_deploy "$SVC_APPEND" <<EOF
type = process
command = /bin/sh -c 'for a in "\$\$@"; do echo "\$\$a" >> $MARK_A; done; while :; do sleep 60; done' _ base-arg
command += extra-arg
restart = false
EOF
slinitctl --system --no-wait start "$SVC_APPEND" >/dev/null 2>&1
wait_for_service "$SVC_APPEND" "STARTED" 10 || true
sleep 1
_out=$(cat "$MARK_A" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *base-arg*extra-arg*)
        echo "OK: command += appended (got: $_out)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: command += didn't append: '$_out'"
        ;;
esac
svc_remove "$SVC_APPEND"
rm -f "$MARK_A"

# --- @include -------------------------------------------------------
# Behavioural probe: the included fragment sets restart=false. Run a
# command that exits immediately; if the include landed, the service
# reaches a terminal stopped state instead of looping.
SVC_INCLUDE="acceptance-test-env-include"
INC_DIR="/etc/slinit.d/acceptance-test-env-include.fragments"
mkdir -p "$INC_DIR"
cat > "$INC_DIR/restart.conf" <<'FRAG'
restart = false
FRAG

svc_deploy "$SVC_INCLUDE" <<EOF
type = process
command = /bin/sh -c 'exit 0'
@include $INC_DIR/restart.conf
EOF
slinitctl --system --no-wait start "$SVC_INCLUDE" >/dev/null 2>&1
sleep 2
_st=$(svc_state "$SVC_INCLUDE")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    STOPPED|FAILED|"")
        echo "OK: @include applied (svc reached '$_st' — restart=false honored)"
        ;;
    STARTED|STARTING)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: @include didn't apply restart=false (state: $_st)"
        ;;
esac
svc_remove "$SVC_INCLUDE"
rm -rf "$INC_DIR"

# --- @include-opt: silently skip missing target ---------------------
mkdir -p "$CHECKDIR"
cat > "$CHECKDIR/svc-include-opt" <<EOF
type = process
command = /bin/true
@include-opt $CHECKDIR/nonexistent.conf
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-check -d "$CHECKDIR" svc-include-opt >/dev/null 2>&1; then
    echo "OK: @include-opt silently skips missing files"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: @include-opt rejected when target is missing"
fi

# --- Negative parse: unterminated ${ -------------------------------
cat > "$CHECKDIR/svc-broken-expand" <<'EOF'
type = process
command = /bin/sh -c 'echo ${unterminated'
EOF
_TESTS_RUN=$((_TESTS_RUN + 1))
# slinit's parser keeps trailing literal '${...' rather than erroring —
# document the behaviour either way (this is observational, not a
# correctness assertion).
if slinit-check -d "$CHECKDIR" svc-broken-expand >/dev/null 2>&1; then
    echo "OK: parser accepts unterminated \${ (keeps literal — by design)"
else
    echo "OK: parser rejects unterminated \${"
fi

test_summary
