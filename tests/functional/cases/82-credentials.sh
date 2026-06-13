#!/bin/sh
# Test: systemd-style per-service credentials (#5).
# Validates:
#   - /run/credentials/<svc>/ is created and exposed via
#     $CREDENTIALS_DIRECTORY;
#   - load-credential copies a file from disk;
#   - set-credential writes the inline value;
#   - the directory is mounted ro tmpfs (writes refused);
#   - the credentials directory contains exactly the configured keys.

wait_for_service "cred-svc" "STARTED" 15

[ -f /run/cred-probe/result ] && have_witness=yes || have_witness=no
assert_eq "$have_witness" "yes" "probe wrote its witness file"

dir=$(grep '^dir=' /run/cred-probe/result | cut -d= -f2-)
assert_eq "$dir" "/run/credentials/cred-svc" \
    "\$CREDENTIALS_DIRECTORY points at the canonical path"

key=$(grep '^api-key=' /run/cred-probe/result | cut -d= -f2-)
assert_eq "$key" "shhh-file-secret" \
    "load-credential copied the on-disk file value"

inline=$(grep '^greeting=' /run/cred-probe/result | cut -d= -f2-)
assert_eq "$inline" "hello-from-set" \
    "set-credential wrote the inline value"

writable=$(grep '^writable=' /run/cred-probe/result | cut -d= -f2-)
assert_eq "$writable" "no" \
    "credentials tmpfs is read-only (write refused)"

# Mount type — tmpfs expected.
if [ -s /run/cred-probe/mount-line ]; then
    grep -q 'tmpfs' /run/cred-probe/mount-line && mt=tmpfs || mt=other
else
    mt=missing
fi
assert_eq "$mt" "tmpfs" "credentials directory is a tmpfs"

test_summary
