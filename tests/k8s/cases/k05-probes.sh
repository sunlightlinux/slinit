# Probes are how Kubernetes asks a container whether it is healthy, and
# with slinit inside, the honest answer comes from slinitctl. A readiness
# probe that queries a service's state must hold the pod un-Ready until
# that service is up, and a liveness probe must keep passing afterwards
# without slinit's control socket ever refusing a connection.
#
# `slow` is a scripted service, so it stays STARTING for the eight
# seconds its script runs and only then reports STARTED. That is a real
# delay for the readiness probe to wait out, with no directive invented
# for the occasion.

P=k05
trap 'k delete pod $P --now >/dev/null 2>&1' EXIT

svc_configmap $P-svc \
"boot:::type = internal
restart = yes
depends-on: slow
" \
"slow:::type = scripted
command = /bin/sh -c \"sleep 8\"
restart = no
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
    readinessProbe:
      exec:
        command: ["/bin/sh", "-c", "slinitctl status slow | grep -q STARTED"]
      periodSeconds: 2
      failureThreshold: 30
    livenessProbe:
      exec:
        command: ["/bin/sh", "-c", "slinitctl list >/dev/null"]
      periodSeconds: 3
      failureThreshold: 3
    volumeMounts:
    - name: svc
      mountPath: /etc/slinit.d
  volumes:
  - name: svc
    configMap:
      name: $P-svc
EOF

wait_pod_phase $P Running 60
check $? "pod is Running"

wait_pod_ready $P 90
check $? "readiness probe driven by slinitctl eventually reports Ready"

# Liveness must keep passing: a restart would mean slinitctl failed.
sleep 12
[ "$(restart_count $P)" = "0" ]
check $? "liveness probe kept passing (restartCount=$(restart_count $P))"

summary
