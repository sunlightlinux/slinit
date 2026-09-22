# When a pod dies, `kubectl logs` is what anyone looks at first. It
# carries the container's stdout, which for slinit is its console. A
# service crash-looping to its restart limit, and the boot failure that
# ends the container, both have to be in there.
#
# This is the Kubernetes view of the bug tests/container case 14 pins:
# slinit used to mute its console once boot finished, so the interesting
# half of the story went to a file that dies with the container.

P=k03
trap 'k delete pod $P --now >/dev/null 2>&1' EXIT

svc_configmap $P-svc \
"boot:::type = internal
depends-on: flapper
" \
"flapper:::type = process
command = /bin/sh -c \"exit 3\"
restart = yes
restart-limit-count = 3
restart-limit-interval = 10
"

cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P
spec:
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

wait_pod_phase $P Failed 60
check $? "pod ended up Failed"

logs=$(k logs $P 2>/dev/null)
echo "$logs" | grep -q "process exited with code 3"
check $? "kubectl logs carries the service's own failure"
echo "$logs" | grep -qi "boot failure"
check $? "kubectl logs says why the container ended"

summary
