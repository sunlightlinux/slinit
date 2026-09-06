# 760-path-activation-fire — measure `start-on-path-exists` fire
# latency: touch a trigger file, then poll until slinit observes
# it and starts the svc. Reveals the inotify/poll cadence of
# slinit's path watcher — an admin-facing feature that determines
# how "instant" udev-style rules feel.
#
# Uses manual=yes so slinit doesn't try to start the svc from
# boot; only the path trigger should start it.
_trigger="/tmp/perf-path-trig-$$"
_name="perf-pathact-$$"
_svcfile="/etc/slinit.d/$_name"
cat > "$_svcfile" <<EOF
type = scripted
command = /bin/true
start-on-path-exists = $_trigger
manual = yes
EOF
# Load service so path watcher gets armed
slinitctl load "$_name" > /dev/null 2>&1 || true

_fire_and_wait() {
    rm -f "$_trigger"
    # Reset svc to STOPPED so is-started returns non-zero
    slinitctl stop "$_name" > /dev/null 2>&1
    _t0=$(perf_now_ns)
    touch "$_trigger"
    # Poll is-started at ~1kHz (busy-loop) until we observe STARTED
    _cnt=0
    while [ $_cnt -lt 5000 ]; do
        if slinitctl is-started "$_name" > /dev/null 2>&1; then
            _t1=$(perf_now_ns)
            echo $(( _t1 - _t0 ))
            return
        fi
        _cnt=$((_cnt + 1))
    done
    echo 0  # timeout (5000 polls)
}

_samples="$(mktemp)"
_i=0
while [ $_i -lt "$ITERS" ]; do
    _fire_and_wait >> "$_samples"
    _i=$((_i + 1))
done

_med=$(sort -n "$_samples" | awk 'BEGIN{c=0}{a[c++]=$1}END{if(c%2)print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2}')
_p95=$(sort -n "$_samples" | awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.95*(c-1)+0.5)]}')
_min=$(sort -n "$_samples" | head -1)
rm -f "$_samples"
printf "BenchmarkPathActivate_touch_to_started %3d  median=%8s ms  p95=%8s ms  min=%8s ms\n" \
    "$ITERS" \
    "$(awk -v n="$_med" 'BEGIN{printf "%.3f", n/1e6}')" \
    "$(awk -v n="$_p95" 'BEGIN{printf "%.3f", n/1e6}')" \
    "$(awk -v n="$_min" 'BEGIN{printf "%.3f", n/1e6}')"

# Teardown
rm -f "$_trigger"
slinitctl stop "$_name"   > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
