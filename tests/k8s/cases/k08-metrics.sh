# The metrics endpoint as a fleet would reach it: a named container port
# with the usual prometheus.io annotations, a Service in front, and the
# scrape coming from another pod over the cluster network.
#
# What this adds over container case 23 is the part Kubernetes owns —
# whether a scraper that only knows the Service can get the numbers, and
# whether the pod carries what a ServiceMonitor or the annotation-based
# discovery looks for.

P=k08
trap 'k delete pod $P $P-scraper --now >/dev/null 2>&1; k delete svc $P --now >/dev/null 2>&1' EXIT

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
  labels: {app: $P}
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "9099"
    prometheus.io/path: /metrics
spec:
  restartPolicy: Never
  containers:
  - name: slinit
    image: $IMAGE
    imagePullPolicy: Never
    command: ["/sbin/slinit", "-o", "--metrics-listen", ":9099"]
    ports:
    - name: metrics
      containerPort: 9099
    readinessProbe:
      httpGet: {path: /metrics, port: metrics}
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
  - name: metrics
    port: 9099
    targetPort: metrics
EOF

# Ready means the kubelet's httpGet probe scraped /metrics and got a
# 200 — the endpoint is reachable before anything else is asserted.
wait_pod_ready $P 120
check $? "pod became Ready — the kubelet scraped /metrics itself"

# And now a scrape from somewhere else in the cluster, through the
# Service, which is how Prometheus would reach it.
cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P-scraper
spec:
  restartPolicy: Never
  containers:
  - name: scraper
    image: $IMAGE
    imagePullPolicy: Never
    command: ["/bin/sh", "-c"]
    args:
    - wget -q -O - http://$P:9099/metrics
EOF

wait_pod_phase $P-scraper Succeeded 90
check $? "scraper pod finished"

out=$(k logs $P-scraper 2>/dev/null)
echo "$out" | grep -q "^slinit_build_info{version="
check $? "the scrape carries build info"
[ "$(echo "$out" | awk '$1 == "slinit_boot_ready" {print $2}')" = "1" ]
check $? "boot_ready is 1 in the scraped output"
[ "$(echo "$out" | awk '$1 == "slinit_service_up{service=\"worker\"}" {print $2}')" = "1" ]
check $? "the worker is reported up"
echo "$out" | grep -q "^# TYPE slinit_service_restarts_total counter"
check $? "the restart counter is declared a counter"

# The annotations are what annotation-based discovery keys on; a pod
# serving metrics nobody is told to scrape is not much use.
[ "$(k get pod $P -o jsonpath='{.metadata.annotations.prometheus\.io/scrape}')" = "true" ]
check $? "the pod is annotated for scraping"

summary
