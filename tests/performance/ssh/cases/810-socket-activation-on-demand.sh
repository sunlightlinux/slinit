# 810-socket-activation-on-demand — INTENDED to measure true
# cold-start latency via socket-activation=on-demand: arm the
# listen socket, connect via nc, time until svc reaches STARTED.
#
# NOT working as authored (ceres v2.2.6, 2026-09-06):
#   1. slinitctl start on a socket-activation=on-demand svc puts
#      it directly into STARTED — slinit does NOT defer the exec
#      to the first client connection the way systemd would.
#      Semantics of `on-demand` here need spec review before we
#      can measure the systemd-parity claim.
#   2. Bundled `netcat` (netcat-0.7.1_7) doesn't accept `-q 0`
#      (traditional variant, not GNU) — client-side connect+
#      close needs a different tool (socat, ncat, python).
#
# Skipping until the semantics + tooling are pinned down. Kept
# in-tree as a placeholder so the follow-up doesn't get lost.
echo "SKIP: on-demand socket-activation semantics + netcat -q 0 both need pinning"
return 0 2>/dev/null || exit 0
_name="perf-sock-lazy-$$"
_svcfile="/etc/slinit.d/$_name"
_sock="/tmp/perf-sock-lazy-$$.sock"
cat > "$_svcfile" <<EOF
type = process
command = /bin/true
socket-listen = $_sock
socket-activation = on-demand
manual = yes
EOF
slinitctl load "$_name" > /dev/null 2>&1 || true
# Arm the socket by starting the svc once (slinit binds + waits)
slinitctl start "$_name" > /dev/null 2>&1
sleep 0.3

_connect_and_poll() {
    _t0=$(perf_now_ns)
    # Connect + close immediately; svc should get the fd
    nc -U -q 0 "$_sock" < /dev/null > /dev/null 2>&1 || true
    # Poll is-started with tight loop
    _cnt=0
    while [ $_cnt -lt 5000 ]; do
        if slinitctl is-started "$_name" > /dev/null 2>&1; then
            _t1=$(perf_now_ns)
            echo $(( _t1 - _t0 ))
            return
        fi
        _cnt=$((_cnt + 1))
    done
    echo 0
    # Re-arm after each iter
    slinitctl start "$_name" > /dev/null 2>&1
}

_samples="$(mktemp)"
_i=0
while [ $_i -lt "$ITERS" ]; do
    _connect_and_poll >> "$_samples"
    _i=$((_i + 1))
done

_med=$(sort -n "$_samples" | awk 'BEGIN{c=0}{a[c++]=$1}END{if(c%2)print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2}')
_p95=$(sort -n "$_samples" | awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.95*(c-1)+0.5)]}')
_min=$(sort -n "$_samples" | head -1)
rm -f "$_samples"
printf "BenchmarkSocketActivate_connect_to_started %3d  median=%8s ms  p95=%8s ms  min=%8s ms\n" \
    "$ITERS" \
    "$(awk -v n="$_med" 'BEGIN{printf "%.3f", n/1e6}')" \
    "$(awk -v n="$_p95" 'BEGIN{printf "%.3f", n/1e6}')" \
    "$(awk -v n="$_min" 'BEGIN{printf "%.3f", n/1e6}')"

# Teardown
slinitctl stop "$_name"   > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile" "$_sock"
