# The baseline: a Pod whose container is slinit, managing services
# inside it. The kubelet must see a healthy container, `kubectl exec`
# has to reach slinitctl, and deleting the Pod has to end it quickly
# rather than waiting out the grace period and being SIGKILLed.

P=k01
trap 'k delete pod $P --now >/dev/null 2>&1' EXIT

svc_configmap $P-svc \
"boot:::type = internal
restart = yes
depends-on: worker
" \
"worker:::type = process
command = /bin/sh -c \"while :; do sleep 1; done\"
restart = yes
"

cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P
spec:
  terminationGracePeriodSeconds: 30
  restartPolicy: Never
  containers:
  - name: slinit
    image: $IMAGE
    imagePullPolicy: Never
    command: ["/sbin/slinit", "-o"]
    volumeMounts:
    - name: svc
      mountPath: /etc/slinit.d
  volumes:
  - name: svc
    configMap:
      name: $P-svc
EOF

wait_pod_ready $P 60
check $? "pod became Ready"

k exec $P -- slinitctl list 2>/dev/null | grep -q "worker"
check $? "kubectl exec reaches slinitctl inside the pod"

k exec $P -- cat /proc/1/cmdline 2>/dev/null | tr '\0' ' ' | grep -q "slinit -o"
check $? "slinit is PID 1 of the container"

t0=$(date +%s%N)
k delete pod $P --now=false --wait=false >/dev/null 2>&1
wait_gone $P 40
gone=$?
ms=$(( ($(date +%s%N) - t0) / 1000000 ))
[ $gone -eq 0 ] && [ "$ms" -lt 15000 ]
check $? "pod terminated in ${ms}ms, well inside the 30s grace period"

summary
