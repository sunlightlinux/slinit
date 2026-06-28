#!/bin/sh
# 63-fdstore — file-descriptor-store config + sd_notify FDSTORE round-trip.
#
# `file-descriptor-store-max=N` (systemd #14 parity) opens a per-service
# Unix datagram socket at /run/slinit/notify/<svc>.sock and exports its
# path as $NOTIFY_SOCKET to the child. The child can then send
# sd_notify FDSTORE=1 packets with SCM_RIGHTS to stash fds; on the next
# start the stored fds are prepended to the child's ExtraFiles and
# exposed as LISTEN_FDS / LISTEN_FDNAMES — same env API systemd uses,
# so an upstream daemon's reconnection logic carries over verbatim.
#
# Layout: a tiny C helper (compiled here because the Void image ships
# no systemd-notify / socat / python) does the SCM_RIGHTS sendmsg; a
# shell runner reports which run it is via /tmp/acceptance-fdstore.log
# so we can prove the second invocation observed LISTEN_FDS=1.

WORK="/tmp/acceptance-fdstore"
SVC="acceptance-test-fdstore"
SVCFILE="/etc/slinit.d/$SVC"
RUN="$WORK/run.sh"
HELPER_SRC="$WORK/helper.c"
HELPER="$WORK/helper"
SENTINEL="$WORK/sentinel"
LOG="$WORK/log"
ENVDUMP="$WORK/env"

cleanup() {
    slinitctl --system stop "$SVC" 2>/dev/null
    slinitctl --system unload "$SVC" 2>/dev/null
    rm -f "$SVCFILE"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup

mkdir -p "$WORK"

# --- Parser probes (still valid; cheap, run them up front) ----------
cat > "$SVCFILE" <<EOF
type = process
command = /bin/true
file-descriptor-store-max = 2
restart = false
EOF
_chk=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinit-check accepts file-descriptor-store-max=2"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check rejected the config:"; echo "$_chk" | sed 's/^/  | /'
fi

sed -i 's/file-descriptor-store-max = 2/file-descriptor-store-max = -3/' "$SVCFILE"
_chk2=$(slinit-check -d /etc/slinit.d "$SVC" 2>&1)
_crc2=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_crc2" -ne 0 ] && echo "$_chk2" | grep -qi "non-negative"; then
    echo "OK: slinit-check rejects negative max (rc=$_crc2)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: negative max not rejected (rc=$_crc2, out='$_chk2')"
fi

# --- Round-trip setup -----------------------------------------------
echo "SENTINEL-PAYLOAD-$$" > "$SENTINEL"

cat > "$HELPER_SRC" <<'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#include <sys/socket.h>
#include <sys/un.h>

int main(int argc, char **argv) {
    if (argc != 2) { fprintf(stderr, "usage: %s <file>\n", argv[0]); return 2; }
    const char *sockpath = getenv("NOTIFY_SOCKET");
    if (!sockpath || !*sockpath) { fprintf(stderr, "NOTIFY_SOCKET unset\n"); return 3; }
    int sf = open(argv[1], O_RDONLY);
    if (sf < 0) { perror("open"); return 4; }

    int s = socket(AF_UNIX, SOCK_DGRAM, 0);
    if (s < 0) { perror("socket"); return 5; }

    struct sockaddr_un sa = {0};
    sa.sun_family = AF_UNIX;
    strncpy(sa.sun_path, sockpath, sizeof(sa.sun_path) - 1);

    const char body[] = "FDSTORE=1\nFDNAME=sentinel\n";
    struct iovec iov = { (void*)body, sizeof(body) - 1 };

    char cbuf[CMSG_SPACE(sizeof(int))];
    memset(cbuf, 0, sizeof(cbuf));
    struct msghdr msg = {0};
    msg.msg_name = &sa; msg.msg_namelen = sizeof(sa);
    msg.msg_iov = &iov; msg.msg_iovlen = 1;
    msg.msg_control = cbuf; msg.msg_controllen = sizeof(cbuf);
    struct cmsghdr *cm = CMSG_FIRSTHDR(&msg);
    cm->cmsg_level = SOL_SOCKET; cm->cmsg_type = SCM_RIGHTS;
    cm->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(cm), &sf, sizeof(int));

    ssize_t n = sendmsg(s, &msg, 0);
    if (n < 0) { perror("sendmsg"); return 6; }
    close(sf); close(s);
    return 0;
}
EOF

gcc -O0 -o "$HELPER" "$HELPER_SRC" 2>"$WORK/gcc.err" || {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: gcc failed to build helper:"
    sed 's/^/  | /' "$WORK/gcc.err"
    test_summary
    exit 1
}

# Runner: dumps env on every start. On the first start it pushes the
# sentinel fd via the helper; on subsequent starts it expects the fd
# back via LISTEN_FDS (kernel maps stored fds starting at fd 3).
cat > "$RUN" <<EOF
#!/bin/sh
env > "$ENVDUMP.\$\$\$\$"
if [ -n "\$LISTEN_FDS" ] && [ "\$LISTEN_FDS" -ge 1 ]; then
    fd3=\$(cat /proc/self/fd/3 2>/dev/null || true)
    printf 'restart LISTEN_FDS=%s LISTEN_FDNAMES=%s fd3=%s\n' \\
        "\$LISTEN_FDS" "\$LISTEN_FDNAMES" "\$fd3" >> "$LOG"
else
    printf 'first_run\n' >> "$LOG"
    "$HELPER" "$SENTINEL"
    printf 'after_helper rc=%d\n' "\$?" >> "$LOG"
fi
exec sleep 600
EOF
chmod +x "$RUN"

# Real config for the runtime probes (overwrites the linter probe file).
cat > "$SVCFILE" <<EOF
type = process
command = $RUN
file-descriptor-store-max = 2
restart = false
EOF

# --- Probe 3: first start brings the service up + env carries socket -
slinitctl --system start "$SVC" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 8 ]; do
    _st=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: $SVC STARTED (1st run)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC stuck at '$_st' on 1st run"
    test_summary
    exit 1
fi

# Let the sd_notify packet land.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q '^first_run' "$LOG" 2>/dev/null; then
    echo "OK: 1st run reached the helper (log: first_run)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 1st run never logged first_run"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q '^after_helper rc=0' "$LOG" 2>/dev/null; then
    echo "OK: helper exit 0 (sendmsg succeeded)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: helper failed:"; sed 's/^/  log: /' "$LOG" 2>/dev/null
fi

# Sanity-check that $NOTIFY_SOCKET pointed at the right path and the
# listener socket actually bound.
_first_env=$(ls -1 "$ENVDUMP".* 2>/dev/null | head -1)
_ns=$(grep '^NOTIFY_SOCKET=' "$_first_env" 2>/dev/null | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_ns" in
    "NOTIFY_SOCKET=/run/slinit/notify/$SVC.sock")
        echo "OK: NOTIFY_SOCKET=/run/slinit/notify/$SVC.sock (1st run env)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: NOTIFY_SOCKET wrong on 1st run: '$_ns'"
        ;;
esac

# --- Round-trip: restart and assert LISTEN_FDS reflects stored fd ----
slinitctl --system restart "$SVC" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 8 ]; do
    _st=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: $SVC STARTED (2nd run via restart)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC stuck at '$_st' on restart"
fi

sleep 1
_line=$(grep '^restart ' "$LOG" 2>/dev/null | tail -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_line" in
    *"LISTEN_FDS=1"*)
        echo "OK: 2nd run sees LISTEN_FDS=1 ($_line)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: LISTEN_FDS != 1 on 2nd run; line='$_line'"
        sed 's/^/  log: /' "$LOG" 2>/dev/null
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_line" in
    *"LISTEN_FDNAMES=sentinel"*)
        echo "OK: LISTEN_FDNAMES carries 'sentinel' (FDNAME survived)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: LISTEN_FDNAMES missing 'sentinel'; line='$_line'"
        ;;
esac

_payload=$(cat "$SENTINEL")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_line" in
    *"fd3=$_payload"*)
        echo "OK: fd 3 still reads the original sentinel payload ($_payload)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: fd 3 content mismatch (want $_payload); line='$_line'"
        ;;
esac

test_summary
