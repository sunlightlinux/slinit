#!/bin/sh
# remote-prelude.sh — sourced by each case on the remote target.
# Provides assertion + slinit service helpers in the same style as
# tests/functional/lib/assert.sh, plus a small svc-deploy helper for
# the lifecycle cases.

_TESTS_RUN=0
_TESTS_FAILED=0

# Namespace under which lifecycle cases drop their service descriptions.
# Kept separate from anything a real install ships so a forgotten teardown
# is trivially identifiable.
ACCEPTANCE_NS_PREFIX="acceptance-test-"
ACCEPTANCE_SVCDIR="/etc/slinit.d"

# ---------- assertions ---------------------------------------------------

assert_eq() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$1" != "$2" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ${3:-assert_eq}: got '$1', expected '$2'"
        return 1
    fi
    echo "OK: ${3:-assert_eq '$1' == '$2'}"
}

assert_contains() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$1" in
        *"$2"*) echo "OK: ${3:-assert_contains '$2'}" ;;
        *) _TESTS_FAILED=$((_TESTS_FAILED + 1))
           echo "FAIL: ${3:-assert_contains}: '$2' not in output" ;;
    esac
}

assert_not_contains() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$1" in
        *"$2"*) _TESTS_FAILED=$((_TESTS_FAILED + 1))
               echo "FAIL: ${3:-assert_not_contains}: '$2' unexpectedly found" ;;
        *) echo "OK: ${3:-assert_not_contains '$2'}" ;;
    esac
}

assert_exit_code() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    eval "$1" >/dev/null 2>&1
    _rc=$?
    if [ "$_rc" != "$2" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ${3:-assert_exit_code}: '$1' exited $_rc, expected $2"
        return 1
    fi
    echo "OK: ${3:-exit $2 for '$1'}"
}

# ---------- slinit helpers ----------------------------------------------

# svc_state SERVICE  -> emits state string (e.g. STARTED, STOPPED, FAILED)
svc_state() {
    slinitctl --system status "$1" 2>/dev/null \
        | awk '/State:/ {print $2; exit}'
}

assert_service_state() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _got=$(svc_state "$1")
    if [ "$_got" != "$2" ]; then
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ${3:-service '$1'}: got '$_got', expected '$2'"
        return 1
    fi
    echo "OK: ${3:-service '$1' is $2}"
}

wait_for_service() {
    _svc="$1"; _want="$2"; _timeout="${3:-10}"; _elapsed=0
    while [ "$_elapsed" -lt "$_timeout" ]; do
        if [ "$(svc_state "$_svc")" = "$_want" ]; then
            return 0
        fi
        sleep 1
        _elapsed=$((_elapsed + 1))
    done
    echo "TIMEOUT: '$_svc' did not reach '$_want' in ${_timeout}s (current: $(svc_state "$_svc"))"
    return 1
}

# svc_deploy NAME <<<CONTENT
# Drops a service description into $ACCEPTANCE_SVCDIR and asks the daemon to
# reload its directory listing. Enforces the acceptance-test- prefix to make
# unexpected leftovers trivial to spot/clean (`ls /etc/slinit.d/acceptance-test-*`).
#
# Unloads the name first so any cached description from a previous case is
# dropped — slinit caches the parsed description at load time and won't
# re-read the file on a fresh start (it only warns about a mtime mismatch).
# Without this, a case re-using a name across runs gets the OLD command.
svc_deploy() {
    _name="$1"
    case "$_name" in
        ${ACCEPTANCE_NS_PREFIX}*) ;;
        *) echo "FATAL: svc_deploy refuses '$_name' (must start with $ACCEPTANCE_NS_PREFIX)" >&2
           exit 2 ;;
    esac
    slinitctl --system unload "$_name" 2>/dev/null || true
    cat > "${ACCEPTANCE_SVCDIR}/${_name}"
}

# svc_remove NAME [NAME...]
# Stops, waits for STOPPED, unloads, removes the service files. Idempotent.
# stop is *blocking* (no --no-wait) because a subsequent unload on a service
# still transitioning is silently rejected by the daemon, leaking the entry
# in the loaded-service list across test cases.
svc_remove() {
    for _name in "$@"; do
        case "$_name" in
            ${ACCEPTANCE_NS_PREFIX}*) ;;
            *) echo "FATAL: svc_remove refuses '$_name' (must start with $ACCEPTANCE_NS_PREFIX)" >&2
               exit 2 ;;
        esac
        # All flags are global in slinitctl — they belong *before* the
        # subcommand. `stop --ignore-unstarted SVC` would parse the flag
        # as the service name.
        slinitctl --system --ignore-unstarted stop "$_name" 2>/dev/null || true
        _e=0
        while [ "$_e" -lt 8 ]; do
            case "$(svc_state "$_name")" in
                STOPPED|"") break ;;
            esac
            sleep 1
            _e=$((_e + 1))
        done
        slinitctl --system unload "$_name" 2>/dev/null || true
        rm -f "${ACCEPTANCE_SVCDIR}/${_name}"
    done
}

# ---------- summary ------------------------------------------------------

test_summary() {
    echo ""
    echo "--- Results: $_TESTS_RUN run, $_TESTS_FAILED failed ---"
    [ "$_TESTS_FAILED" -eq 0 ]
}
