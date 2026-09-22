# nginx under slinit as a Pod, with traffic from two directions: the
# kubelet's own httpGet readiness probe, and a second Pod fetching the
# page over the cluster network through a Service.
#
# This is the Kubernetes half of tests/container case 21. What it adds
# is that Kubernetes itself has to be able to reach the workload: an
# httpGet probe is the kubelet asking, and a Service means the traffic
# arrives the way another application's would.
#
# Needs the nginx base image; skipped when it cannot be fetched.

P=k07
IMG=slinit-container-test-nginx:local
BASE=nginx:1.27-alpine
BUILD="${TMPDIR:-/tmp}/k8s-nginx-$$"
trap 'k delete pod $P $P-client --now >/dev/null 2>&1; k delete svc $P --now >/dev/null 2>&1; rm -rf "$BUILD"' EXIT

if ! docker image inspect "$BASE" >/dev/null 2>&1; then
    if ! docker pull -q "$BASE" >/dev/null 2>&1; then
        echo "SKIP: $BASE is not available (no network?)"
        exit 0
    fi
fi

mkdir -p "$BUILD/bin"
cp "$BUILD_DIR/bin/slinit" "$BUILD_DIR/bin/slinitctl" "$BUILD/bin/"
printf 'FROM %s\nCOPY bin/ /sbin/\n' "$BASE" > "$BUILD/Dockerfile"
docker build -q -t "$IMG" "$BUILD" >/dev/null 2>&1
check $? "built an nginx image with slinit as its init"
kind load docker-image "$IMG" --name "$CLUSTER" >/dev/null 2>&1
check $? "loaded it into the cluster"

svc_configmap $P-svc \
"boot:::type = internal
restart = yes
depends-on: nginx
" \
"nginx:::type = process
command = /usr/sbin/nginx -g \"daemon off;\"
restart = yes
term-signal = QUIT
stop-timeout = 5
options = runs-on-console
"

cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P
  labels: {app: $P}
spec:
  restartPolicy: Never
  containers:
  - name: slinit
    image: $IMG
    imagePullPolicy: Never
    command: ["/sbin/slinit", "-o"]
    ports:
    - containerPort: 80
    readinessProbe:
      httpGet: {path: /, port: 80}
      periodSeconds: 2
      failureThreshold: 30
    volumeMounts:
    - name: svc
      mountPath: /etc/slinit.d
  volumes:
  - name: svc
    configMap:
      name: $P-svc
---
apiVersion: v1
kind: Service
metadata:
  name: $P
spec:
  selector: {app: $P}
  ports:
  - port: 80
    targetPort: 80
EOF

# Ready here means the kubelet's httpGet probe got a 200 from nginx.
# The budget is generous because the first pod after `kind load` waits
# for containerd to unpack the image it has just been handed, which on
# the first run of this case took longer than a 90s wait allowed.
wait_pod_ready $P 180
check $? "pod became Ready — the kubelet's httpGet probe reached nginx"

# Traffic from another pod, over the cluster network, through the Service.
cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P-client
spec:
  restartPolicy: Never
  containers:
  - name: client
    image: $IMAGE
    imagePullPolicy: Never
    command: ["/bin/sh", "-c"]
    args:
    - |
      ok=0
      i=0
      while [ \$i -lt 20 ]; do
        if wget -q -O /dev/null http://$P/; then ok=\$((ok+1)); fi
        i=\$((i+1))
        sleep 0.5
      done
      echo "CLIENT_OK=\$ok"
EOF

wait_pod_phase $P-client Succeeded 90
check $? "client pod finished"

got=$(k logs $P-client 2>/dev/null | grep -o 'CLIENT_OK=[0-9]*' | cut -d= -f2)
[ -n "$got" ] && [ "$got" -ge 18 ]
check $? "client fetched the page through the Service (${got:-0}/20 succeeded)"

served=$(k logs $P 2>/dev/null | grep -c 'GET / HTTP/1.1" 200')
[ -n "$served" ] && [ "$served" -ge "${got:-0}" ]
check $? "nginx's own access log is in kubectl logs ($served requests recorded)"

k exec $P -- slinitctl stop nginx >/dev/null 2>&1
check $? "slinitctl stop nginx succeeded"

wait_pod_phase $P Failed 30 || wait_pod_phase $P Succeeded 5
check $? "pod terminated once its only workload was stopped"

k logs $P 2>/dev/null | grep -q "\[STOPPD\] nginx"
check $? "slinit reports the service stopped"

k delete pod $P $P-client --now >/dev/null 2>&1
k delete svc $P --now >/dev/null 2>&1
wait_gone $P 30
check $? "pod deleted"

summary
