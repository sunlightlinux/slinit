#!/bin/sh
# run.sh — slinit as a Kubernetes workload, on a local kind cluster.
#
#   ./tests/k8s/run.sh                 # all cases
#   ./tests/k8s/run.sh cases/k02-*.sh  # some cases
#   KEEP_IMAGE=1 ./tests/k8s/run.sh    # skip the image rebuild
#   DELETE_CLUSTER=1 ./tests/k8s/run.sh   # tear the cluster down at the end
#
# The cluster (kind, name `slinit-test`) is created on first run and
# then reused: creating one costs about a minute, running the cases
# costs seconds. Its kubeconfig lives in tests/container/_build, so
# ~/.kube/config is left alone.
set -u

K8S_DIR="$(cd "$(dirname "$0")" && pwd)"
export K8S_DIR
. "$K8S_DIR/lib.sh"

command -v kind >/dev/null 2>&1 || { echo "k8s suite: kind is not installed" >&2; exit 2; }
[ -x "$KUBECTL" ] || command -v "$KUBECTL" >/dev/null 2>&1 || {
    echo "k8s suite: no kubectl found (set KUBECTL, or drop one in $BUILD_DIR/bin)" >&2
    exit 2
}
docker info >/dev/null 2>&1 || { echo "k8s suite: docker is not usable" >&2; exit 2; }

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    echo "creating kind cluster '$CLUSTER' (one-off, ~1 min) ..."
    kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null || {
        echo "cluster creation failed" >&2; exit 2; }
fi

# Wait for the API server, don't assume it. `kind get clusters` lists a
# cluster as soon as its node container exists, which is before the
# kubeconfig has been written — and kubectl pointed at a file that is not
# there yet silently falls back to localhost:8080, so every case fails
# with "connection refused" against a cluster that is merely still
# starting.
i=0
until "$KUBECTL" get --raw=/readyz >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -gt 120 ]; then
        echo "k8s suite: API server of '$CLUSTER' not ready after 120s (KUBECONFIG=$KUBECONFIG)" >&2
        exit 2
    fi
    sleep 1
done

# The suite's own image, built from the working tree and pushed into the
# node. imagePullPolicy: Never in the manifests keeps the kubelet from
# looking for it in a registry.
export CT_DIR
. "$CT_DIR/lib.sh"
if [ "${KEEP_IMAGE:-0}" != "1" ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    echo "building $IMAGE ..."
    build_image || { echo "image build failed" >&2; exit 2; }
fi
echo "loading $IMAGE into the cluster ..."
kind load docker-image "$IMAGE" --name "$CLUSTER" >/dev/null 2>&1 || {
    echo "kind load failed" >&2; exit 2; }

"$KUBECTL" create namespace "$NS" >/dev/null 2>&1
cleanup_all

if [ $# -eq 0 ]; then
    set -- "$K8S_DIR"/cases/*.sh
fi

passed=0 failed=0 failed_names=""
for case in "$@"; do
    name=$(basename "$case" .sh)
    out=$(sh -c '. "$K8S_DIR/lib.sh"; . "$1"' _ "$case" 2>&1)
    if [ $? -eq 0 ]; then
        passed=$((passed + 1))
        echo "  PASS $name"
        [ "${VERBOSE:-0}" = "1" ] && echo "$out" | sed 's/^/    /'
    else
        failed=$((failed + 1))
        failed_names="$failed_names $name"
        echo "  FAIL $name"
        echo "$out" | sed 's/^/    /'
    fi
    cleanup_all
done

echo
echo "Results: $passed passed, $failed failed ($((passed + failed)) total)"
[ -n "$failed_names" ] && echo "Failed:$failed_names"

if [ "${DELETE_CLUSTER:-0}" = "1" ]; then
    echo "deleting cluster '$CLUSTER' ..."
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1
fi

[ "$failed" -eq 0 ]
