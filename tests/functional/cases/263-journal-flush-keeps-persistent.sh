#!/bin/sh
# 263-journal-flush-keeps-persistent: --flush moves the volatile journal into
# the persistent directory without destroying what is already there. A boot
# that started with the persistent directory read-only wrote the volatile
# file under the same day name, and the flush renamed it over the earlier
# boot's persistent file — that day's earlier journal was gone.

P=/tmp/pers263
V=/tmp/vol263
rm -rf "$P" "$V"; mkdir -p "$P" "$V"
mount -t tmpfs pers263 "$P" || skip_case "cannot mount a tmpfs"
today=$(date -u +%Y-%m-%d)
printf '{"msg":"EARLIER-BOOT-1"}\n{"msg":"EARLIER-BOOT-2"}\n{"msg":"EARLIER-BOOT-3"}\n' > "$P/$today.jsonl"
mount -o remount,ro "$P"

slinit-journald --format=jsonl --compress=false --dir="$P" --volatile-dir="$V" \
    --pid-file=/tmp/fl263.pid --socket=/tmp/fl263.sock --admin-socket=/tmp/fl263.ctl \
    > /dev/null 2>/tmp/fl263.log &
jd=$!
sleep 1
echo "volatile dir: $(ls "$V" | tr '\n' ' ')"
[ -n "$(ls "$V")" ] || { cat /tmp/fl263.log; kill "$jd"; umount "$P"; skip_case "daemon did not fall back to the volatile directory"; }

mount -o remount,rw "$P"
ln -sf /tmp/fl263.ctl /run/slinit-journald.ctl
slinit-journalctl --flush > /dev/null 2>&1
sleep 1
echo "persistent dir after flush: $(ls "$P" | tr '\n' ' ')"

kept=$(cat "$P"/*.jsonl 2>/dev/null | grep -c 'EARLIER-BOOT')
assert_eq "$kept" "3" "the earlier boot's three entries survived the flush"

kill "$jd" 2>/dev/null; wait "$jd" 2>/dev/null
rm -f /run/slinit-journald.ctl
umount "$P" 2>/dev/null
test_summary
