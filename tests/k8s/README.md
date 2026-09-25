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
| k07-nginx-workload | nginx under slinit answers the kubelet's httpGet readiness probe and a second Pod's requests through a Service, with its access log in `kubectl logs` |
| k08-metrics | the endpoint is reachable as a fleet would reach it: a named container port, the `prometheus.io/*` annotations discovery keys on, a Service in front, and the scrape coming from another pod |
| k09-prometheus-scrape | a real Prometheus in the cluster reports the target **up**, PromQL returns `slinit_service_up`, and the restart counter is registered as a counter |
| k10-counter-semantics | `slinit_service_restarts_total` only climbs while the pod lives, and starts lower after the pod is replaced — what `rate()` needs to be meaningful |

k09 needs `prom/prometheus`, and k07 the `nginx:1.27-alpine` base
image; both skip themselves when the image cannot be fetched.

k09 is the one that cannot be replaced by `curl`. Reaching the endpoint
and liking what you read is not the same as being scrapable: Prometheus
parses the exposition itself and drops a scrape it dislikes — a HELP
line for a metric that never appears, a TYPE that disagrees with the
samples, a counter not ending `_total`. curl is happy with all of
those. So k09 asks Prometheus rather than asking ourselves.

k10 is the one a single read cannot do. A counter's value at an instant
says nothing; what makes it usable is that it only ever climbs within a
process and resets when the process is replaced. Kubernetes is the
natural place to test the second half, and `restartCount` is an
independent witness that the container really was replaced.

k07 also It also shows the recipe for an application's own
logs: slinit discards a service's output by default, and
`options = runs-on-console` sends it to the container's log stream. See
[../container/README.md](../container/README.md).
