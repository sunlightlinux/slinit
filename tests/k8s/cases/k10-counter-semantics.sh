# Counter semantics across time and across a pod replacement.
#
# Every other metrics case reads the endpoint once. A counter cannot be
# checked that way: what makes `slinit_service_restarts_total` usable is
# not its value at an instant but that it only ever climbs while the
# process lives, and starts from zero when the process is replaced.
# Prometheus relies on exactly that — rate() treats a decrease as a
# counter reset and starts over, so a counter that drifted downward
# mid-life would produce a rate spike out of nothing, and one that
# survived a restart would hide a real one.
#
# Kubernetes is the natural place to test the second half: replacing a
# pod is one command, and restartCount gives an independent witness that
# the container really was restarted rather than merely re-read.

P=k10
trap 'k delete pod $P --now >/dev/null 2>&1; k delete configmap $P-svc --now >/dev/null 2>&1' EXIT

svc_configmap $P-svc \
"boot:::type = internal
restart = yes
depends-on: flapping
" \
"flapping:::type = process
command = /bin/sh -c \"sleep 1\"
restart = yes
restart-delay = 0
"

start_pod() {
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
      command: ["/sbin/slinit", "-o", "--services-dir", "/etc/slinit.d",
                "--metrics-listen", "0.0.0.0:9100", "boot"]
      volumeMounts:
        - {name: svc, mountPath: /etc/slinit.d}
  volumes:
    - name: svc
      configMap: {name: $P-svc}
EOF
}

# restarts_total prints the counter for the flapping service, or empty.
restarts_total() {
    k exec $P -- wget -qO- http://localhost:9100/metrics 2>/dev/null |
        awk '$1 == "slinit_service_restarts_total{service=\"flapping\"}" {print $2}'
}

start_pod
if ! wait_pod_ready $P 90; then
    fail "pod did not become ready"
    summary
    exit 1
fi

# --- climbs while the process lives ------------------------------------

_first=$(restarts_total)
if [ -z "$_first" ]; then
    fail "restarts_total not exposed for the flapping service"
    summary
    exit 1
fi
pass "restarts_total is exposed (first read: $_first)"

# The service exits every second, so a few seconds is several restarts.
# Sample repeatedly rather than twice: a single later read proves it
# grew, sampling proves it never went backwards, which is the property
# rate() actually needs.
_prev=$_first
_backwards=0
_i=0
while [ $_i -lt 6 ]; do
    sleep 1
    _now=$(restarts_total)
    [ -z "$_now" ] && continue
    # Shell arithmetic on the exposition's float form: these are whole
    # numbers, but strip any exponent/decimal defensively.
    _n=${_now%%.*}
    _p=${_prev%%.*}
    if [ "$_n" -lt "$_p" ] 2>/dev/null; then
        _backwards=1
        fail "restarts_total went backwards: $_prev then $_now"
    fi
    _prev=$_now
    _i=$((_i + 1))
done

[ "$_backwards" -eq 0 ]
check $? "restarts_total never decreased across 6 samples"

_last=${_prev%%.*}
_start=${_first%%.*}
[ "$_last" -gt "$_start" ] 2>/dev/null
check $? "restarts_total climbed while the pod ran ($_first -> $_prev)"

# --- resets when the process is replaced -------------------------------

k delete pod $P --now >/dev/null 2>&1
start_pod
if ! wait_pod_ready $P 90; then
    fail "replacement pod did not become ready"
    summary
    exit 1
fi

# Read early: the point is that the new process started from zero, and
# the flapping service climbs fast enough to obscure it if we dawdle.
_after=$(restarts_total)
_a=${_after%%.*}
if [ -z "$_after" ]; then
    fail "restarts_total not exposed after pod replacement"
else
    [ "$_a" -lt "$_last" ] 2>/dev/null
    check $? "restarts_total restarted from a lower value after replacement ($_last -> $_after)"
fi

# The counter resetting is only meaningful if the container really is a
# new one. restartCount is Kubernetes' own record, independent of
# anything slinit reports about itself.
_rc=$(k get pod $P -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null)
[ "${_rc:-0}" = "0" ]
check $? "the replacement is a fresh container, not a restarted one (restartCount=${_rc:-unset})"

summary
