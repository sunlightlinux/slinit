#!/bin/sh
# 14-enable-disable — `slinitctl enable --from FROM SVC` must persist by
# creating FROM.d/waits-for/SVC; `disable --from FROM SVC` must remove it.
# Mirrors how rc-update works underneath but exercises the slinitctl path
# directly, including a non-default `--from` parent.

PARENT="acceptance-test-parent"
CHILD="acceptance-test-child"
# slinit shares one waits-for.d/ per services-dir; the symlink lives at the
# dir-level path, not under a per-parent <parent>.d/waits-for subdir.
WAITS_LINK="${ACCEPTANCE_SVCDIR}/waits-for.d/${CHILD}"

cleanup() {
    slinitctl --system --from "$PARENT" disable "$CHILD" 2>/dev/null || true
    rm -f "$WAITS_LINK"
    svc_remove "$CHILD" "$PARENT"
}
trap cleanup EXIT INT TERM

svc_deploy "$PARENT" <<EOF
type = internal
EOF

svc_deploy "$CHILD" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

# enable
slinitctl --system --from "$PARENT" enable "$CHILD" >/dev/null 2>&1
assert_eq "$(test -L "$WAITS_LINK" && echo yes || echo no)" "yes" \
    "enable created waits-for.d/${CHILD}"

# child should be at least loaded; enable triggers a start too
wait_for_service "$CHILD" "STARTED" 10 || true
assert_service_state "$CHILD" "STARTED" "$CHILD STARTED after enable"

# disable
slinitctl --system --from "$PARENT" disable "$CHILD" >/dev/null 2>&1
assert_eq "$(test -L "$WAITS_LINK" && echo yes || echo no)" "no" \
    "disable removed the symlink"

# disable triggers a stop too — wait for it.
wait_for_service "$CHILD" "STOPPED" 10 || true
assert_service_state "$CHILD" "STOPPED" "$CHILD STOPPED after disable"

test_summary
