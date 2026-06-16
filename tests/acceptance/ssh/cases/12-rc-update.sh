#!/bin/sh
# 12-rc-update — rc-update add SVC LEVEL maps to
# `slinitctl --from runlevel-<level> enable SVC`, which drops a symlink
# under runlevel-<level>.d/waits-for/. Verify the symlink shows up, that
# `rc-update show` lists the member, and that `rc-update del` reverses it.
#
# Slinit doesn't ship a built-in runlevel-* meta-service; the test creates
# one (internal/milestone) for the duration of this case.

RL_NAME="acceptance-test-rl"
RL_META="runlevel-${RL_NAME}"   # rc-update prefix is hard-coded
SVC="acceptance-test-rc-member"
# Persistence is anchored at the runlevel parent's *service-dir*, not under a
# <parent>.d/ subdir — slinit shares one waits-for.d/ per services-dir
# (see pkg/control/connection.go persistEnable). The path is therefore the
# canonical /etc/slinit.d/waits-for.d/<child>.
WAITS_LINK="${ACCEPTANCE_SVCDIR}/waits-for.d/${SVC}"

cleanup() {
    rc-update del "$SVC" "$RL_NAME" 2>/dev/null || true
    rm -f "$WAITS_LINK"
    svc_remove "$SVC"
    slinitctl --system --ignore-unstarted stop "$RL_META" 2>/dev/null || true
    slinitctl --system unload "$RL_META" 2>/dev/null || true
    rm -f "${ACCEPTANCE_SVCDIR}/${RL_META}"
}
trap cleanup EXIT INT TERM

# Deploy a no-op milestone for the runlevel. We bypass svc_deploy because
# its acceptance-test- prefix guard would refuse `runlevel-…`.
cat > "${ACCEPTANCE_SVCDIR}/${RL_META}" <<EOF
type = internal
EOF

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

# rc-update add
rc-update add "$SVC" "$RL_NAME" >/dev/null 2>&1
assert_eq "$(test -L "$WAITS_LINK" && echo yes || echo no)" "yes" \
    "rc-update add created waits-for.d/${SVC}"

# rc-update show should mention the member.
_show="$(rc-update show "$RL_NAME" 2>&1)"
assert_contains "$_show" "$SVC" "rc-update show lists '$SVC'"

# rc-update del removes the symlink.
rc-update del "$SVC" "$RL_NAME" >/dev/null 2>&1
assert_eq "$(test -L "$WAITS_LINK" && echo yes || echo no)" "no" \
    "rc-update del removed the symlink"

test_summary
