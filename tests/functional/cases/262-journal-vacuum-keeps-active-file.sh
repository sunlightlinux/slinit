#!/bin/sh
# 262-journal-vacuum-keeps-active-file: slinit-journalctl --vacuum-* never
# removes the file slinit-journald is writing. The daemon names its file by
# the date it OPENED it and rotates by age, not at midnight, but vacuum only
# protected today's UTC name: a day after a daemon started, --vacuum-files
# deleted its active file and every later event went into an unlinked inode.

D=/tmp/vac262
rm -rf "$D"; mkdir -p "$D"
now=$(date +%s)
# Open the daemon's file "yesterday", then put the clock back.
date -s "$(date -d @$((now - 86400)) '+%Y-%m-%d %H:%M:%S')" > /dev/null 2>&1 || skip_case "cannot set the clock"
slinit-journald --format=jsonl --dir="$D" --pid-file=/tmp/vac262.pid \
    --socket=/tmp/vac262.sock --admin-socket=/tmp/vac262.ctl > /dev/null 2>/tmp/vac262.log &
jd=$!
sleep 1
date -s "$(date -d @$((now + 1)) '+%Y-%m-%d %H:%M:%S')" > /dev/null 2>&1
active=$(ls "$D" | grep -E '^[0-9]{4}-[0-9]{2}-[0-9]{2}\.jsonl$' | head -1)
echo "daemon's active file: $active (today is $(date -u +%Y-%m-%d))"
[ -n "$active" ] || { cat /tmp/vac262.log; kill "$jd"; skip_case "daemon created no file before an event"; }
# The case only means something if the active file is NOT today's name.
[ "$active" != "$(date -u +%Y-%m-%d).jsonl" ] || { kill "$jd"; skip_case "clock step did not take: active file has today's name"; }

# Over the size cap even without the rotated files, so a vacuum that does
# not recognise the active file reaches it: oldest first, then the active.
i=0; while [ $i -lt 64 ]; do echo '{"msg":"padding padding padding padding padding padding"}'; i=$((i + 1)); done >> "$D/$active"
for f in 2026-01-01.1000 2026-02-01.2000; do
    head -c 2048 /dev/zero | tr '\0' 'x' > "$D/$f.jsonl"
done
touch -t 202601010000 "$D/2026-01-01.1000.jsonl"
touch -t 202602010000 "$D/2026-02-01.2000.jsonl"
slinit-journalctl --vacuum-size=1K --directory="$D" > /dev/null 2>&1

assert_eq "$(test -e "$D/$active" && echo kept || echo removed)" "kept" "vacuum kept the daemon's active file ($active)"
assert_eq "$(test -e "$D/2026-01-01.1000.jsonl" && echo kept || echo removed)" "removed" "vacuum still removed an old rotated file"

kill "$jd" 2>/dev/null; wait "$jd" 2>/dev/null
test_summary
