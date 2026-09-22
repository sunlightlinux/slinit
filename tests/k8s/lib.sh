#!/bin/sh
# lib.sh — shared helpers for the Kubernetes suite (kind).
#
# Same subject as tests/container, one layer up: there the runtime talks
# to slinit directly, here a kubelet does. That changes who sends the
# signals, who reads the exit code, and what an operator sees — a Job's
# exit status, `kubectl logs`, restartCount, probes.
#
# Service files arrive as a ConfigMap mounted at /etc/slinit.d instead of
# a bind mount, which is how they would ship in a real deployment.

K8S_DIR="${K8S_DIR:-$(cd "$(dirname "$0")" && pwd)}"
REPO_DIR="$(cd "$K8S_DIR/../.." && pwd)"
CT_DIR="$REPO_DIR/tests/container"
BUILD_DIR="$CT_DIR/_build"

CLUSTER="${KIND_CLUSTER:-slinit-test}"
IMAGE="${SLINIT_TEST_IMAGE:-slinit-container-test:local}"
NS="${K8S_NAMESPACE:-slinit-test}"

# kubectl: the one on PATH, or the copy the suite downloaded.
KUBECTL="${KUBECTL:-$(command -v kubectl || echo "$BUILD_DIR/bin/kubectl")}"
KUBECONFIG="${KUBECONFIG:-$BUILD_DIR/kubeconfig}"
export KUBECONFIG

k() { "$KUBECTL" -n "$NS" "$@"; }

_TESTS_RUN=0
_TESTS_FAILED=0

pass() { _TESTS_RUN=$((_TESTS_RUN + 1)); echo "OK: $*"; }
fail() { _TESTS_RUN=$((_TESTS_RUN + 1)); _TESTS_FAILED=$((_TESTS_FAILED + 1)); echo "FAIL: $*"; }
check() { if [ "$1" -eq 0 ]; then pass "$2"; else fail "$2"; fi; }

summary() {
    echo "--- $_TESTS_RUN checks, $_TESTS_FAILED failed ---"
    [ "$_TESTS_FAILED" -eq 0 ]
}

# svc_configmap NAME FILE=CONTENT... — writes a ConfigMap whose keys are
# service file names. Mount it at /etc/slinit.d and slinit loads them.
# Each argument after the name is "filename:::content".
svc_configmap() {
    _name=$1
    shift
    _args=""
    for pair in "$@"; do
        _file=${pair%%:::*}
        _body=${pair#*:::}
        _tmp="${TMPDIR:-/tmp}/k8s-svc-$$-$_file"
        printf '%s' "$_body" > "$_tmp"
        _args="$_args --from-file=$_file=$_tmp"
    done
    # shellcheck disable=SC2086
    k create configmap "$_name" $_args --dry-run=client -o yaml | k apply -f - >/dev/null
    rm -f "${TMPDIR:-/tmp}"/k8s-svc-$$-*
}

# wait_pod_phase NAME PHASE SECONDS
wait_pod_phase() {
    _i=0
    while [ "$_i" -lt "$(($3 * 2))" ]; do
        [ "$(k get pod "$1" -o jsonpath='{.status.phase}' 2>/dev/null)" = "$2" ] && return 0
        sleep 0.5
        _i=$((_i + 1))
    done
    return 1
}

# wait_pod_ready NAME SECONDS — the Ready condition, not just Running.
wait_pod_ready() {
    _i=0
    while [ "$_i" -lt "$(($2 * 2))" ]; do
        [ "$(k get pod "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = "True" ] && return 0
        sleep 0.5
        _i=$((_i + 1))
    done
    return 1
}

# wait_gone NAME SECONDS — the pod object is deleted.
#
# An unreachable API server also makes `get pod` fail, which would look
# exactly like a deleted pod: the first run of this suite reported a pod
# "terminated in 178ms" while nothing had been created at all. So a
# failed lookup is only believed when the API itself still answers.
wait_gone() {
    _i=0
    while [ "$_i" -lt "$(($2 * 2))" ]; do
        if ! k get pod "$1" >/dev/null 2>&1; then
            k get --raw=/readyz >/dev/null 2>&1 || return 1
            return 0
        fi
        sleep 0.5
        _i=$((_i + 1))
    done
    return 1
}

term_exit_code() {
    k get pod "$1" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null
}

term_reason() {
    k get pod "$1" -o jsonpath='{.status.containerStatuses[0].state.terminated.reason}' 2>/dev/null
}

restart_count() {
    k get pod "$1" -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null
}

cleanup_all() {
    k delete pod,job,configmap --all --now >/dev/null 2>&1
}
