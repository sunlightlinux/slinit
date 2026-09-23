# The Prometheus endpoint, scraped from outside the container the way a
# fleet would scrape it.
#
# The interesting assertion is the restart counter: it is the number a
# fleet alerts on ("which service is flapping"), and it is the one that
# would be easy to render as something that resets. A service is killed
# from outside, comes back, and the counter must have moved and stayed
# moved.

N=slinit-ct-metrics-$$
PORT=${METRICS_TEST_PORT:-19099}
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = yes
EOF
# Not part of the boot target: stopping it has to leave the container —
# and the endpoint — alive, which stopping `worker` would not.
cat > "$SVC/extra" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = no
EOF

"$RUNTIME" rm -f "$N" >/dev/null 2>&1
"$RUNTIME" run -d --name "$N" -p "127.0.0.1:$PORT:9099" \
    -v "$SVC:/etc/slinit.d:ro" "$IMAGE" \
    /sbin/slinit -o --metrics-listen :9099 >/dev/null
ct_wait_ready $N 15
check $? "boot service reached STARTED"

scrape() { curl -sS --max-time 5 "http://127.0.0.1:$PORT/metrics"; }
value() { scrape | awk -v s="$1" '$1 == s {print $2}'; }

code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$PORT/metrics")
[ "$code" = "200" ]; check $? "GET /metrics answers 200 (got $code)"

ct=$(curl -s -o /dev/null -w '%{content_type}' --max-time 5 "http://127.0.0.1:$PORT/metrics")
case "$ct" in text/plain*) pass "Content-Type is the exposition format ($ct)" ;;
              *) fail "Content-Type is '$ct'" ;; esac

scrape | grep -q "^slinit_build_info{version=" ; check $? "build info is reported"
[ "$(value slinit_boot_ready)" = "1" ]; check $? "boot_ready is 1 once the target is up"
[ "$(value 'slinit_service_up{service="worker"}')" = "1" ]; check $? "worker reports up"
[ "$(value 'slinit_services{state="started"}')" = "2" ]; check $? "both services counted as started"

# Kill the worker from outside; the supervisor restarts it and the
# counter has to move.
before=$(value 'slinit_service_restarts_total{service="worker"}')
pid=$("$RUNTIME" exec $N slinitctl status worker | awk '/PID:/ {print $2; exit}')
"$RUNTIME" exec $N kill -9 "$pid"

after="$before"
i=0
while [ $i -lt 40 ]; do
    after=$(value 'slinit_service_restarts_total{service="worker"}')
    [ -n "$after" ] && [ "$after" != "$before" ] && break
    sleep 0.25
    i=$((i + 1))
done
[ "${after:-0}" -gt "${before:-0}" ]
check $? "restart counter moved after the service was killed ($before -> $after)"
[ "$(value slinit_restarts_total)" -ge 1 ]
check $? "the daemon-wide restart counter moved too"

# Counters must not go backwards between scrapes: a fleet rates them.
again=$(value 'slinit_service_restarts_total{service="worker"}')
[ "$again" -ge "$after" ]
check $? "counter did not go backwards on the next scrape ($after -> $again)"

# A stopped service reports down rather than disappearing, so an alert
# on "up == 0" fires instead of going stale.
"$RUNTIME" exec $N slinitctl start extra >/dev/null 2>&1
[ "$(value 'slinit_service_up{service="extra"}')" = "1" ]
check $? "a service started after boot appears as up"
"$RUNTIME" exec $N slinitctl stop extra >/dev/null 2>&1
sleep 1
[ "$(value 'slinit_service_up{service="extra"}')" = "0" ]
check $? "a stopped service still has a series, reporting 0"

summary
