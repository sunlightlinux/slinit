#!/bin/sh
# Test: slinit-seedrng neither reads through nor writes through a symlink
# planted in its seed directory.
#
# It runs as root. Seeds were read with a path-following read, so
# seed.credit -> /etc/passwd fed that file to the kernel and credited it
# as entropy; a dangling seed.no-credit was left in place and the new
# seed was then written THROUGH it, so root created (or truncated) any
# file the link named. Now anything that is not a regular file is
# unlinked and skipped, and the new seed is a fresh inode.

SEEDDIR=/var/lib/273-seedrng
VICTIM=/tmp/273-victim-target
rm -rf "$SEEDDIR" "$VICTIM"
mkdir -p "$SEEDDIR"
chmod 0700 "$SEEDDIR"
ln -s "$VICTIM" "$SEEDDIR/seed.no-credit"
ln -s /etc/passwd "$SEEDDIR/seed.credit"
_passwd_sum=$(sha256sum /etc/passwd | cut -d' ' -f1)

slinit-seedrng -seed-dir "$SEEDDIR" >/tmp/273.out 2>&1
echo "seedrng rc=$? output:"; sed 's/^/  | /' /tmp/273.out

assert_eq "$(test -e "$VICTIM" -o -L "$VICTIM" && echo yes || echo no)" "no" \
    "the dangling seed.no-credit link was not followed to create its target"

_links=$(find "$SEEDDIR" -type l | wc -l)
assert_eq "$_links" "0" "the planted symlinks were removed from the seed dir"

assert_eq "$(sha256sum /etc/passwd | cut -d' ' -f1)" "$_passwd_sum" \
    "/etc/passwd is untouched"

# A fresh seed must still have been written, as a regular file.
_TESTS_RUN=$((_TESTS_RUN + 1))
_seed=$(find "$SEEDDIR" -type f -name 'seed.*' | head -1)
if [ -n "$_seed" ] && [ "$(stat -c %h "$_seed")" = "1" ]; then
    echo "OK: a new regular seed file was written ($_seed)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no regular seed file in $SEEDDIR: $(ls -la "$SEEDDIR")"
fi

rm -rf "$SEEDDIR" "$VICTIM"
test_summary
