#!/bin/sh
# Test: `slinitctl edit NAME` opens the file the service was loaded from,
# and the change is live afterwards.
#
# edit asked the daemon for the description path with a request whose
# reply the daemon never sends, so it failed for every service and no
# test noticed. The editor here is a script: it records the path it was
# given and rewrites the command, so the case checks both halves — the
# right file was opened, and the reload that follows picked it up.
#
# Three spellings, one per candidate edit has to try: a plain name, a
# template instance (the file is the part before '@'), and a systemd
# unit (the file is NAME.service, in /etc/systemd/system).

cat > /tmp/278-editor.sh <<'EOF'
#!/bin/sh
echo "$1" >> /tmp/278-opened
sed -i 's/sleep 2781\.1/sleep 2781.2/' "$1"
EOF
chmod +x /tmp/278-editor.sh
: > /tmp/278-opened

printf '%s\n' 'type = process' 'command = /bin/sleep 2781.1' \
    'restart = false' > /etc/slinit.d/test-edit
printf '%s\n' 'type = internal' > /etc/slinit.d/test-edit-tpl
# test-edit-unit.service comes from 278-slinitctl-edit.d/systemd: the
# loader only searches /etc/systemd/system if it exists at boot.

# Loaded but not running: reload swaps a STOPPED service's description.
slinitctl --system status test-edit >/dev/null 2>&1

VISUAL= EDITOR=/tmp/278-editor.sh timeout 10 slinitctl --system edit test-edit >/dev/null 2>&1
assert_eq "$?" "0" "edit of a plain service succeeds"
assert_eq "$(sed -n 1p /tmp/278-opened)" "/etc/slinit.d/test-edit" \
    "the editor got the service's own file"
assert_contains "$(cat /etc/slinit.d/test-edit)" "sleep 2781.2" "the editor's change is on disk"

# The reload after the editor is what makes the change live.
slinitctl --system start test-edit >/dev/null 2>&1
wait_for_service test-edit STARTED 10 || true
_running=$(ps -o args 2>/dev/null | grep -c '[s]leep 2781\.2')
assert_eq "$_running" "1" "the edited command is the one that runs"
slinitctl --system stop test-edit >/dev/null 2>&1

slinitctl --system start test-edit-tpl@a >/dev/null 2>&1
VISUAL= EDITOR=/tmp/278-editor.sh timeout 10 slinitctl --system edit test-edit-tpl@a >/dev/null 2>&1
assert_eq "$?" "0" "edit of a template instance succeeds"
assert_eq "$(sed -n 2p /tmp/278-opened)" "/etc/slinit.d/test-edit-tpl" \
    "a template instance opens the template file"

slinitctl --system status test-edit-unit >/dev/null 2>&1
VISUAL= EDITOR=/tmp/278-editor.sh timeout 10 slinitctl --system edit test-edit-unit >/dev/null 2>&1
assert_eq "$?" "0" "edit of a systemd unit succeeds"
assert_eq "$(sed -n 3p /tmp/278-opened)" "/etc/systemd/system/test-edit-unit.service" \
    "a systemd unit opens NAME.service"

# A failing editor must not reload: the file may be half written.
printf '%s\n' '#!/bin/sh' 'echo "$1" >> /tmp/278-opened; exit 3' > /tmp/278-badeditor.sh
chmod +x /tmp/278-badeditor.sh
_out=$(VISUAL= EDITOR=/tmp/278-badeditor.sh timeout 10 slinitctl --system edit test-edit 2>&1)
assert_eq "$?" "1" "a failing editor makes edit fail"
assert_contains "$_out" "NOT reloaded" "and says the change was not reloaded"

test_summary
