#!/bin/sh
# 264-tmpfiles-mode-and-symlink: two slinit-tmpfiles fixes, as the boot-time
# tmpfiles service would hit them.
#  - A mode's sticky/setgid/setuid bits are kept: "d /var/tmp 1777" made
#    /var/tmp 0777 — any user could delete or replace any other user's files.
#  - "d" does not chmod through a symlink: a link planted at a d path in a
#    world-writable directory (/tmp/.X11-unix -> /etc) had root give the
#    link's target the entry's mode at boot.

F=/tmp/tf264
rm -rf "$F" /tmp/zz264-sticky /tmp/zz264-link /tmp/zz264-target
mkdir -p "$F" /tmp/zz264-target
chmod 0700 /tmp/zz264-target
ln -s /tmp/zz264-target /tmp/zz264-link
cat > "$F/probe.conf" <<'CONF'
d /tmp/zz264-sticky 1777 root root -
d /tmp/zz264-link 0777 root root -
CONF
slinit-tmpfiles --dirs="$F" > /dev/null 2>/tmp/tf264.err
echo "slinit-tmpfiles rc=$? $(cat /tmp/tf264.err)"

assert_eq "$(stat -c %a /tmp/zz264-sticky 2>/dev/null)" "1777" "d 1777 keeps the sticky bit"
assert_eq "$(stat -c %a /tmp/zz264-target 2>/dev/null)" "700" "d on a symlink left the link's target alone"

test_summary
