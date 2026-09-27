#!/bin/sh
# 201-slinit-systemd-convert — v2.1.4 converter for .service units.
# Covers the systemd → slinit Restart= mapping (5 values → 3) plus
# Type= dispatch (simple → process, forking → bgprocess, oneshot →
# scripted) plus [Unit] After/Requires → waits-for/depends-on.
# The dep-name suffix stripping (.service, .target, .socket, ...)
# is the subtle one — bare names are what slinit expects.

WORK=/tmp/acc-201-systemd
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM
mkdir -p "$WORK"

# --- simple service, Restart=always -----------------------------------
cat > "$WORK/simpled.service" <<'EOF'
[Unit]
Description=Simple daemon
After=network.target
Requires=dbus.service

[Service]
Type=simple
ExecStart=/usr/bin/simpled --daemon
Restart=always
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
EOF

_out=$(slinit-systemd-convert "$WORK/simpled.service" 2>/dev/null)
assert_contains "$_out" "type = process" "Type=simple → process"
assert_contains "$_out" "command = /usr/bin/simpled --daemon" "ExecStart extracted"
assert_contains "$_out" "restart = yes" "Restart=always → yes"
assert_contains "$_out" "run-as = nobody:nogroup" "User+Group merged into run-as"
assert_contains "$_out" "depends-on: dbus" "Requires=dbus.service → depends-on: dbus"

# After=network.target is dropped, not stripped to a `network` dep.
# slinit has no target concept, and a dependency on a service that does
# not exist is fatal — that one line appears in nearly every unit a
# distribution ships, so inventing the dep made them unloadable.
assert_not_contains "$_out" "network" "After=network.target not turned into a dep"
_notes=$(slinit-systemd-convert -verbose "$WORK/simpled.service" 2>&1 >/dev/null)
assert_contains "$_notes" "network.target" "the dropped target is named in the notes"

# --- oneshot with no explicit Restart → restart=no default -----------
# Systemd allows Restart= on Type=oneshot even though it's unusual;
# the converter respects the operator's explicit choice (last-write
# wins for Restart=). This case verifies the DEFAULT: no explicit
# Restart on a oneshot yields restart=no via the Type=oneshot case
# in the parser.
cat > "$WORK/oneshot.service" <<'EOF'
[Service]
Type=oneshot
ExecStart=/usr/bin/one-shot-command
EOF

_out=$(slinit-systemd-convert "$WORK/oneshot.service" 2>/dev/null)
assert_contains "$_out" "type = scripted" "Type=oneshot → scripted"
assert_contains "$_out" "restart = no" "oneshot defaults restart=no"

# --- forking → bgprocess ---------------------------------------------
cat > "$WORK/forking.service" <<'EOF'
[Service]
Type=forking
ExecStart=/usr/bin/forking-daemon
PIDFile=/run/forking.pid
EOF

_out=$(slinit-systemd-convert "$WORK/forking.service" 2>/dev/null)
assert_contains "$_out" "type = bgprocess" "Type=forking → bgprocess"
assert_contains "$_out" "pid-file = /run/forking.pid" "PIDFile extracted"

test_summary
