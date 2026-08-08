#!/bin/sh
# 207-slinit-openrc-convert — v2.1.4 OpenRC init.d converter.
# Two paths: variable-only script → self-contained slinit file;
# script with custom start()/stop() → wrapped via openrc-run.
# Both are covered here.

WORK=/tmp/oconv
mkdir -p "$WORK"

# --- variable-only path ---
cat > "$WORK/simple" <<'EOF'
#!/sbin/openrc-run
description="Simple test daemon"
command=/usr/sbin/testd
command_args="--flag one"
command_user=nobody
pidfile=/run/testd.pid

depend() {
    need localmount
    use net
}
EOF
chmod +x "$WORK/simple"

_out=$(slinit-openrc-convert "$WORK/simple" 2>/dev/null)
assert_contains "$_out" "command = /usr/sbin/testd --flag one" "simple: command extracted"
assert_contains "$_out" "run-as = nobody" "simple: run-as from command_user"
assert_contains "$_out" "pid-file = /run/testd.pid" "simple: pidfile extracted"
assert_contains "$_out" "depends-on: localmount" "simple: need → depends-on"
assert_contains "$_out" "waits-for: net" "simple: use → waits-for"
assert_not_contains "$_out" "openrc-run" "simple: no runtime wrapper needed"

# --- custom start() path ---
cat > "$WORK/complex" <<'EOF'
#!/sbin/openrc-run
depend() { need localmount; }
start() {
    ebegin "Starting complex"
    start-stop-daemon --exec /usr/sbin/complexd
    eend $?
}
EOF
chmod +x "$WORK/complex"

_out=$(slinit-openrc-convert "$WORK/complex" 2>/dev/null)
assert_contains "$_out" "openrc-run" "complex: wrapped via openrc-run"
assert_contains "$_out" "$WORK/complex start" "complex: wrapper carries script + start"
assert_contains "$_out" "type = scripted" "complex: type=scripted"

test_summary
