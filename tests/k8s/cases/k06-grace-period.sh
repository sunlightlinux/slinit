# Kubernetes sends SIGTERM, waits terminationGracePeriodSeconds, then
# SIGKILLs whatever is left. A service that ignores SIGTERM must not
# push the pod into that SIGKILL: slinit owns the escalation and kills
# it after its own stop-timeout, so the pod goes away in seconds rather
# than at the end of the grace period.

P=k06
trap 'k delete pod $P --now >/dev/null 2>&1' EXIT

svc_configmap $P-svc \
"boot:::type = internal
restart = yes
depends-on: stubborn
" \
"stubborn:::type = process
command = /bin/sh -c \"trap '' TERM; while :; do sleep 1; done\"
stop-timeout = 2
restart = yes
"

cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P
spec:
  terminationGracePeriodSeconds: 60
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

t0=$(date +%s%N)
k delete pod $P --wait=false >/dev/null 2>&1
wait_gone $P 70
gone=$?
ms=$(( ($(date +%s%N) - t0) / 1000000 ))

[ $gone -eq 0 ] && [ "$ms" -lt 20000 ]
check $? "pod gone in ${ms}ms: slinit killed the stubborn service itself, nowhere near the 60s grace period"

summary
