# A real service, with real traffic through a published port.
#
# Everything else in this suite runs `sleep` loops. This one runs nginx
# under slinit, serves HTTP to the host for a few seconds, then stops
# the service the way an operator would and checks the port closes.
#
# It also pins the recipe for seeing an application's own logs in a
# container. slinit discards a service's output by default —
# `log-type = none`, which is dinit's default too — so nginx's access
# log, symlinked to /dev/stdout in the official image, went to
# /dev/null. `options = runs-on-console` gives the service slinit's own
# stdout: in a container there is no /dev/console, so it falls back to
# it, and that is the runtime's log stream.
#
# Needs the nginx base image. Skipped when it cannot be fetched.

N=slinit-ct-nginx-$$
IMG=slinit-container-test-nginx:local
BASE=nginx:1.27-alpine
PORT=${NGINX_TEST_PORT:-18099}
SVC=$(new_svcdir)
BUILD=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC" "$BUILD"' EXIT

if ! "$RUNTIME" image inspect "$BASE" >/dev/null 2>&1; then
    if ! "$RUNTIME" pull -q "$BASE" >/dev/null 2>&1; then
        echo "SKIP: $BASE is not available (no network?)"
        exit 0
    fi
fi

mkdir -p "$BUILD/bin"
cp "$BUILD_DIR/bin/slinit" "$BUILD_DIR/bin/slinitctl" "$BUILD/bin/"
printf 'FROM %s\nCOPY bin/ /sbin/\n' "$BASE" > "$BUILD/Dockerfile"
"$RUNTIME" build -q -t "$IMG" "$BUILD" >/dev/null 2>&1
check $? "built an nginx image with slinit as its init"

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: nginx
EOF
cat > "$SVC/nginx" <<'EOF'
type = process
command = /usr/sbin/nginx -g "daemon off;"
restart = yes
term-signal = QUIT
stop-timeout = 5
options = runs-on-console
EOF

"$RUNTIME" rm -f "$N" >/dev/null 2>&1
"$RUNTIME" run -d --name "$N" -p "127.0.0.1:$PORT:80" \
    -v "$SVC:/etc/slinit.d:ro" "$IMG" /sbin/slinit -o >/dev/null
check $? "container started"

ct_wait_ready $N 20
check $? "boot service reached STARTED"

# Serve traffic for a few seconds.
ok=0 bad=0
end=$(( $(date +%s) + 5 ))
while [ "$(date +%s)" -lt "$end" ]; do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$PORT/")" = "200" ]; then
        ok=$((ok + 1))
    else
        bad=$((bad + 1))
    fi
    sleep 0.3
done
[ "$ok" -ge 5 ] && [ "$bad" -eq 0 ]
check $? "nginx served every request over 5s ($ok answered 200, $bad failed)"

served=$(ct_logs $N | grep -c 'GET / HTTP/1.1" 200')
[ "$served" -ge "$ok" ]
check $? "the requests are in nginx's own access log, in the container log ($served lines)"

"$RUNTIME" exec $N slinitctl stop nginx >/dev/null 2>&1
check $? "slinitctl stop nginx succeeded"

sleep 1
[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$PORT/" 2>/dev/null)" != "200" ]
check $? "the port stops answering once the service is stopped"

ct_logs $N | grep -q "\[STOPPD\] nginx"
check $? "slinit reports the service stopped"

"$RUNTIME" rm -f "$N" >/dev/null 2>&1
"$RUNTIME" rmi "$IMG" >/dev/null 2>&1
! "$RUNTIME" inspect "$N" >/dev/null 2>&1
check $? "container destroyed"

summary
