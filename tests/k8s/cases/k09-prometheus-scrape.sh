# A real Prometheus, in the cluster, scraping slinit.
#
# k08 proves a scraper can reach the endpoint and that the numbers look
# right to grep. That is not the same as being scrapable. Prometheus
# parses the exposition itself and rejects what it does not like — a
# HELP line for a metric that never appears, a TYPE that disagrees with
# the samples, a duplicated series, a counter that does not end _total.
# curl is happy with all of those; Prometheus drops the scrape and the
# target goes down with a parse error.
#
# So this asks Prometheus, not ourselves: is the target up, and does a
# PromQL query come back with slinit's series in it.

# Needs the Prometheus image; skipped when it cannot be fetched.

P=k09
PROM_IMAGE=prom/prometheus:v3.1.0

if ! docker image inspect "$PROM_IMAGE" >/dev/null 2>&1; then
    if ! docker pull -q "$PROM_IMAGE" >/dev/null 2>&1; then
        echo "SKIP: $PROM_IMAGE is not available (no network?)"
        exit 0
    fi
fi
# kind nodes do not share the host's image store.
kind load docker-image "$PROM_IMAGE" --name "$CLUSTER" >/dev/null 2>&1 || true

trap 'k delete pod $P $P-prom --now >/dev/null 2>&1; k delete svc $P --now >/dev/null 2>&1; k delete configmap $P-svc $P-prom-cfg --now >/dev/null 2>&1' EXIT

svc_configmap $P-svc \
"boot:::type = internal
restart = yes
depends-on: worker
" \
"worker:::type = process
command = /bin/sh -c \"while :; do sleep 1; done\"
restart = yes
"

# Prometheus config: one static target, the Service in front of the pod.
# Discovery by annotation is k08's business; what is under test here is
# whether Prometheus can read what slinit writes.
k create configmap $P-prom-cfg --from-literal=prometheus.yml="global:
  scrape_interval: 2s
scrape_configs:
  - job_name: slinit
    static_configs:
      - targets: ['$P:9100']
" --dry-run=client -o yaml | k apply -f - >/dev/null

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
      image: $IMAGE
      imagePullPolicy: Never
      command: ["/sbin/slinit", "-o", "--services-dir", "/etc/slinit.d",
                "--metrics-listen", "0.0.0.0:9100", "boot"]
      ports:
        - {name: metrics, containerPort: 9100}
      volumeMounts:
        - {name: svc, mountPath: /etc/slinit.d}
  volumes:
    - name: svc
      configMap: {name: $P-svc}
---
apiVersion: v1
kind: Service
metadata:
  name: $P
spec:
  selector: {app: $P}
  ports:
    - {name: metrics, port: 9100, targetPort: 9100}
EOF

if ! wait_pod_ready $P 90; then
    fail "slinit pod did not become ready"
    summary
    exit 1
fi

cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $P-prom
spec:
  restartPolicy: Never
  containers:
    - name: prometheus
      image: $PROM_IMAGE
      imagePullPolicy: IfNotPresent
      args: ["--config.file=/etc/prometheus/prometheus.yml"]
      ports:
        - {name: web, containerPort: 9090}
      volumeMounts:
        - {name: cfg, mountPath: /etc/prometheus}
  volumes:
    - name: cfg
      configMap: {name: $P-prom-cfg}
EOF

if ! wait_pod_ready $P-prom 120; then
    fail "prometheus pod did not become ready"
    summary
    exit 1
fi

# Give it a few scrape intervals to make up its mind about the target.
_health=""
_lasterr=""
_i=0
while [ $_i -lt 30 ]; do
    _json=$(k exec $P-prom -- wget -qO- \
        "http://localhost:9090/api/v1/targets?state=active" 2>/dev/null)
    case "$_json" in
        *'"health":"up"'*) _health=up; break ;;
        *'"health":"down"'*) _health=down ;;
    esac
    # Keep the newest error around: if this never goes up, it is the
    # whole diagnosis.
    case "$_json" in
        *'"lastError":"'*) _lasterr=$(printf '%s' "$_json" | sed 's/.*"lastError":"\([^"]*\)".*/\1/') ;;
    esac
    _i=$((_i + 1))
    sleep 2
done

if [ "$_health" = up ]; then
    pass "prometheus reports the slinit target up"
else
    fail "prometheus target never came up (health=${_health:-unknown}, lastError=${_lasterr:-none})"
fi

# The target being up says the scrape parsed. This says the samples
# survived into the TSDB with the names we promised.
_q=$(k exec $P-prom -- wget -qO- \
    "http://localhost:9090/api/v1/query?query=slinit_service_up" 2>/dev/null)
case "$_q" in
    *'"worker"'*) pass "PromQL returns slinit_service_up for the worker service" ;;
    *) fail "slinit_service_up{service=\"worker\"} not in Prometheus: $_q" ;;
esac

# A counter has to be typed as one, or rate() silently refuses it.
_meta=$(k exec $P-prom -- wget -qO- \
    "http://localhost:9090/api/v1/metadata?metric=slinit_service_restarts_total" 2>/dev/null)
case "$_meta" in
    *'"type":"counter"'*) pass "restarts_total is registered as a counter" ;;
    *) fail "restarts_total metadata is not counter: $_meta" ;;
esac

summary
