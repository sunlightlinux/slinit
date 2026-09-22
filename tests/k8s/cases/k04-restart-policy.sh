# With restartPolicy: Always the kubelet restarts the container each
# time it exits, which for slinit means a full boot every round. The
# restart count must climb, each round must reach Ready, and the exit
# code the kubelet records for the previous round must be the
# workload's.

P=k04
trap 'k delete pod $P --now >/dev/null 2>&1' EXIT

svc_configmap $P-svc \
"boot:::type = process
command = /bin/sh -c \"sleep 2; exit 5\"
restart = no
"

cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P
spec:
  restartPolicy: Always
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

i=0
count=0
while [ $i -lt 120 ]; do
    count=$(restart_count $P)
    [ -n "$count" ] && [ "$count" -ge 1 ] && break
    sleep 1
    i=$((i + 1))
done
[ -n "$count" ] && [ "$count" -ge 1 ]
check $? "kubelet restarted the container (restartCount=$count)"

last=$(k get pod $P -o jsonpath='{.status.containerStatuses[0].lastState.terminated.exitCode}' 2>/dev/null)
[ "$last" = "5" ]
check $? "the previous round's exit code is the workload's 5 (got '$last')"

summary
