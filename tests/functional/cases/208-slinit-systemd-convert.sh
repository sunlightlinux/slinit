#!/bin/sh
# 208-slinit-systemd-convert — v2.1.4 systemd .service converter.
# INI parser handles `\`-line continuation, Type/Restart/PIDFile
# mapping, User+Group merge, and dep-suffix stripping across
# .service/.target/.socket/.path/.mount/.timer/.swap/.device.

WORK=/tmp/sconv
mkdir -p "$WORK"

# --- simple unit with the workhorse directives ---
cat > "$WORK/simple.service" <<'EOF'
[Unit]
Description=Simple test
After=network.target
Requires=dbus.service

[Service]
Type=simple
ExecStart=/usr/bin/simpled --daemon
Restart=always
User=nobody
Group=nogroup
EOF

_out=$(slinit-systemd-convert "$WORK/simple.service" 2>/dev/null)
assert_contains "$_out" "type = process" "Type=simple → process"
assert_contains "$_out" "command = /usr/bin/simpled --daemon" "ExecStart extracted"
assert_contains "$_out" "restart = yes" "Restart=always → yes"
assert_contains "$_out" "run-as = nobody:nogroup" "User+Group → run-as"
assert_contains "$_out" "waits-for: network" ".target suffix stripped"
assert_contains "$_out" "depends-on: dbus" ".service suffix stripped"

# --- forking service with PIDFile → bgprocess ---
cat > "$WORK/fork.service" <<'EOF'
[Service]
Type=forking
ExecStart=/usr/bin/forkd
PIDFile=/run/forkd.pid
EOF

_out=$(slinit-systemd-convert "$WORK/fork.service" 2>/dev/null)
assert_contains "$_out" "type = bgprocess" "Type=forking → bgprocess"
assert_contains "$_out" "pid-file = /run/forkd.pid" "PIDFile extracted"

# --- oneshot with no explicit Restart → restart=no default ---
cat > "$WORK/one.service" <<'EOF'
[Service]
Type=oneshot
ExecStart=/bin/true
EOF

_out=$(slinit-systemd-convert "$WORK/one.service" 2>/dev/null)
assert_contains "$_out" "type = scripted" "Type=oneshot → scripted"
assert_contains "$_out" "restart = no" "oneshot defaults restart=no"

# --- line continuation: `\` joins across lines ---
# The exact join spacing depends on how the parser trims each
# continuation segment. We assert on presence + same-line
# grouping (all three fragments must land on the same `command =`
# line) rather than exact byte layout.
printf '[Service]\nType=simple\nExecStart=/bin/echo hello \\\n  --arg1 value \\\n  --arg2\n' > "$WORK/cont.service"

_out=$(slinit-systemd-convert "$WORK/cont.service" 2>/dev/null)
_cmdline=$(echo "$_out" | grep '^command =' | head -1)
assert_contains "$_cmdline" "/bin/echo" "line continuation: /bin/echo present on command line"
assert_contains "$_cmdline" "hello" "line continuation: 'hello' present"
assert_contains "$_cmdline" "--arg1" "line continuation: --arg1 joined"
assert_contains "$_cmdline" "--arg2" "line continuation: --arg2 joined (past two \\ boundaries)"

test_summary
