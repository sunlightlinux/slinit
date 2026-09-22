# Kubernetes tests

slinit as the container of a Pod, on a local [kind](https://kind.sigs.k8s.io/)
cluster.

`tests/container` drives slinit through a container runtime directly.
Here a kubelet is in the middle, which changes who sends the signals,
who reads the exit code, and what an operator sees: a pod phase,
`kubectl logs`, `restartCount`, probe results.

## Usage

```bash
./tests/k8s/run.sh                    # all cases
./tests/k8s/run.sh cases/k02-*.sh     # some cases
VERBOSE=1 ./tests/k8s/run.sh          # print every check
KEEP_IMAGE=1 ./tests/k8s/run.sh       # reuse the image from the last run
DELETE_CLUSTER=1 ./tests/k8s/run.sh   # tear the cluster down afterwards
```

Requirements: Go, Docker, `kind`, and `kubectl` — on PATH, in `$KUBECTL`,
or at `tests/container/_build/bin/kubectl`.

The cluster is named `slinit-test` and is created on first use, then
kept: creating one takes about a minute, running the cases takes
seconds. Its kubeconfig is written to `tests/container/_build/kubeconfig`,
so `~/.kube/config` is untouched. The image is the same one
`tests/container` builds, pushed into the node with `kind load` and
referenced with `imagePullPolicy: Never`.

Service files reach slinit as a ConfigMap mounted at `/etc/slinit.d`,
which is how they would ship in a real deployment.

## What the cases pin

| Case | Contract |
|------|----------|
| k01-pod-lifecycle | Pod becomes Ready, `kubectl exec` reaches slinitctl, slinit is PID 1, deletion ends it well inside the grace period |
| k02-job-exit-code | a batch workload's exit code is what Kubernetes records, so Succeeded/Failed is decided by the workload, not by slinit |
| k03-crashloop-logs | `kubectl logs` carries the service failure and the boot failure that ended the pod |
| k04-restart-policy | with `restartPolicy: Always` the kubelet restarts the container and records the workload's exit code |
| k05-probes | a readiness probe driven by `slinitctl status` holds the pod un-Ready until the service is up; liveness keeps passing |
| k06-grace-period | a SIGTERM-ignoring service is killed by slinit after its stop-timeout, not by the kubelet at the end of the grace period |
