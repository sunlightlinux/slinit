# 810-socket-activation-on-demand — cold-start latency of an on-demand
# socket-activated service: time from a client's connect() until the
# process slinit launches for it has accepted the connection and
# answered.
#
# `slinitctl start` arms the socket (the service is STARTED with no
# process); every client then launches the process, which serves one
# connection and exits, after which the service listens again — so each
# iteration is a full launch, with no re-arming in between.
#
# The image ships no socat / ncat / python, and its netcat cannot do a
# one-shot Unix-socket connect, so a tiny C helper is compiled here, as
# in the acceptance suite's fdstore case: `serve` accepts on fd 3
# (LISTEN_FDS) and writes one byte; `client` connects, waits for that
# byte and prints the elapsed nanoseconds.

_work="/tmp/perf-sock-lazy-$$"
_name="perf-sock-lazy-$$"
_svcfile="/etc/slinit.d/$_name"
_sock="$_work/sock"
_helper="$_work/helper"

if ! command -v cc > /dev/null 2>&1; then
    echo "SKIP: 810 needs a C compiler for its socket helper"
    return 0 2>/dev/null || exit 0
fi
mkdir -p "$_work"
cat > "$_work/helper.c" <<'EOF'
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

int main(int argc, char **argv)
{
	if (argc == 2 && strcmp(argv[1], "serve") == 0) {
		int c = accept(3, NULL, NULL);
		if (c < 0)
			return 1;
		return write(c, "x", 1) == 1 ? 0 : 1;
	}
	if (argc == 3 && strcmp(argv[1], "client") == 0) {
		struct sockaddr_un a = { .sun_family = AF_UNIX };
		struct timespec t0, t1;
		char b;
		int s = socket(AF_UNIX, SOCK_STREAM, 0);
		strncpy(a.sun_path, argv[2], sizeof(a.sun_path) - 1);
		clock_gettime(CLOCK_MONOTONIC, &t0);
		if (connect(s, (struct sockaddr *)&a, sizeof(a)) != 0 || read(s, &b, 1) != 1)
			return 1;
		clock_gettime(CLOCK_MONOTONIC, &t1);
		printf("%lld\n", (long long)(t1.tv_sec - t0.tv_sec) * 1000000000LL +
		       (t1.tv_nsec - t0.tv_nsec));
		return 0;
	}
	return 2;
}
EOF
if ! cc -O2 -o "$_helper" "$_work/helper.c"; then
    echo "FAIL: 810 could not build its socket helper"
    rm -rf "$_work"
    return 0 2>/dev/null || exit 0
fi

cat > "$_svcfile" <<EOF
type = process
command = $_helper serve
socket-listen = $_sock
socket-activation = on-demand
EOF
slinitctl start "$_name" > /dev/null 2>&1

_samples="$(mktemp)"
_i=0
_fail=0
while [ $_i -lt "$ITERS" ]; do
    if ! "$_helper" client "$_sock" >> "$_samples"; then
        _fail=$((_fail + 1))
    fi
    _i=$((_i + 1))
done

if [ ! -s "$_samples" ]; then
    echo "FAIL: 810 no client was served ($_fail failures)"
else
    _med=$(sort -n "$_samples" | awk 'BEGIN{c=0}{a[c++]=$1}END{if(c%2)print a[int(c/2)]; else print (a[c/2-1]+a[c/2])/2}')
    _p95=$(sort -n "$_samples" | awk 'BEGIN{c=0}{a[c++]=$1}END{print a[int(0.95*(c-1)+0.5)]}')
    _min=$(sort -n "$_samples" | head -1)
    printf "BenchmarkSocketActivate_connect_to_served %3d  median=%8s ms  p95=%8s ms  min=%8s ms  failed=%d\n" \
        "$ITERS" \
        "$(awk -v n="$_med" 'BEGIN{printf "%.3f", n/1e6}')" \
        "$(awk -v n="$_p95" 'BEGIN{printf "%.3f", n/1e6}')" \
        "$(awk -v n="$_min" 'BEGIN{printf "%.3f", n/1e6}')" \
        "$_fail"
fi
rm -f "$_samples"

# Teardown
slinitctl stop "$_name"   > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
rm -rf "$_work"
