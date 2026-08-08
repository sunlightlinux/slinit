#!/bin/sh
# 200-slinit-openrc-convert — v2.1.4 converter, two paths:
#   (1) variable-only script (command=, depend()) → self-contained
#       slinit file with no runtime openrc-run dependency.
#   (2) script with custom start()/stop() functions → wrapped
#       command that invokes openrc-run at runtime, preserving
#       every ebegin/einfo/start-stop-daemon call.

WORK=/tmp/acc-200-openrc
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM
mkdir -p "$WORK"

# --- Path 1: variable-only ---------------------------------------------
cat > "$WORK/simple" <<'EOF'
#!/sbin/openrc-run
description="Simple daemon"
command=/usr/sbin/simpled
command_args="--flag value"
command_user=nobody

depend() {
    need localmount
    use net
}
EOF
chmod +x "$WORK/simple"

_out=$(slinit-openrc-convert "$WORK/simple" 2>/dev/null)
assert_contains "$_out" "command = /usr/sbin/simpled --flag value" "simple: direct command"
assert_contains "$_out" "run-as = nobody" "simple: run-as extracted"
assert_contains "$_out" "depends-on: localmount" "simple: need → depends-on"
assert_contains "$_out" "waits-for: net" "simple: use → waits-for"
assert_not_contains "$_out" "openrc-run" "simple: no runtime wrapper needed"

# --- Path 2: custom start() function ---------------------------------
cat > "$WORK/complex" <<'EOF'
#!/sbin/openrc-run
description="Complex daemon"

depend() {
    need localmount
}

start() {
    ebegin "Starting complex"
    start-stop-daemon --exec /usr/sbin/complexd
    eend $?
}
EOF
chmod +x "$WORK/complex"

_out=$(slinit-openrc-convert "$WORK/complex" 2>/dev/null)
assert_contains "$_out" "openrc-run" "complex: wrapped via openrc-run"
assert_contains "$_out" "$WORK/complex start" "complex: wrapper carries script path + start"
assert_contains "$_out" "type = scripted" "complex: type=scripted (custom funcs)"

test_summary
