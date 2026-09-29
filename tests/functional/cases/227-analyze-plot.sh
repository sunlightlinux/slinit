#!/bin/sh
# Test: `slinitctl analyze plot` renders the boot as an SVG timeline.
# Validates: the per-service start instants carried in the BootTime reply
# tail, and that the plot agrees with what `analyze time` reports.

# The plot's header reports the userspace figure, which only exists once
# the boot service has reached STARTED. boot waits-for test-runner, so it
# settles while this script is running — but not necessarily before the
# first line of it.
i=0
while [ $i -lt 100 ]; do
    slinitctl --system is-started boot >/dev/null 2>&1 && break
    sleep 0.1
    i=$((i + 1))
done

svg=$(slinitctl --system analyze plot 2>&1)

assert_contains "$svg" "<?xml version=" "plot emits an XML declaration"
assert_contains "$svg" "<svg xmlns=" "plot emits an SVG root element"
assert_contains "$svg" "</svg>" "plot closes the SVG element"
assert_contains "$svg" "slinit boot timeline" "plot carries a title"
assert_contains "$svg" 'class="activating"' "plot draws service bars"

# Lanes for the services this case adds. Their absence is how a daemon
# that stopped sending the timestamp tail would show up: the renderer
# refuses and prints an error instead of drawing anything.
assert_contains "$svg" "quick-svc (" "quick-svc has a lane"
assert_contains "$svg" "slow-svc (" "slow-svc has a lane"

# The plot and the list form read the same reply, so the userspace
# figure has to be identical in both.
userspace=$(slinitctl --system analyze time 2>&1 | \
    sed -n 's/.*reached after \([^ ]*\) in userspace.*/\1/p')
# An empty needle matches anything, so a failure to read the figure has
# to become a value that cannot match rather than a silent pass.
[ -n "$userspace" ] || userspace="<no userspace figure from analyze time>"
assert_contains "$svg" "$userspace" "plot header matches analyze time"

# A service started after the boot target is an operator action, not
# part of the boot, and must not stretch the axis.
slinitctl --system start late-svc >/dev/null 2>&1
svg2=$(slinitctl --system analyze plot 2>&1)
assert_not_contains "$svg2" "late-svc (" "a post-boot service is not plotted"
assert_contains "$svg2" "started after the boot target" "the header accounts for it"

test_summary
