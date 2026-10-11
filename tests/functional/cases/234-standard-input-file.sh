#!/bin/sh
# 234-standard-input-file — fd 0 reads from a file (launchd
# StandardInPath), and a missing file fails the start rather than
# leaving the service up on an empty descriptor.

IN=/run/sif-in.txt
OUT=/run/sif-out.txt
rm -f "$IN" "$OUT"
printf 'alpha\nbeta\n' > "$IN"

cat > /etc/slinit.d/test-sif <<EOF
type = scripted
command = /bin/sh -c 'cat > $OUT'
standard-input-file = $IN
EOF

slinitctl --system start test-sif 2>/dev/null
_i=0
while [ "$_i" -lt 40 ]; do
    [ -s "$OUT" ] && break
    sleep 0.25
    _i=$((_i + 1))
done
echo "INFO: child read: $(tr '\n' ' ' < "$OUT" 2>/dev/null)"
assert_eq "$(tr -d '\n' < "$OUT" 2>/dev/null)" "alphabeta" \
    "stdin came from the file"

slinitctl --system --ignore-unstarted stop test-sif 2>/dev/null
slinitctl --system unload test-sif 2>/dev/null
rm -f /etc/slinit.d/test-sif "$OUT"

# A missing file must fail the start. This is the half that keeps a
# misconfigured service from looking healthy.
cat > /etc/slinit.d/test-sif-missing <<'EOF'
type = process
command = /bin/cat
standard-input-file = /run/definitely-absent-sif
restart = no
EOF

slinitctl --system start test-sif-missing 2>/dev/null
_state=$(slinitctl --system status test-sif-missing 2>/dev/null | awk '/^ *State:/ { print $2; exit }')
echo "INFO: state with a missing input file: $_state"
assert_eq "$_state" "STOPPED" "a missing standard-input-file failed the start"

slinitctl --system unload test-sif-missing 2>/dev/null
rm -f /etc/slinit.d/test-sif-missing "$IN"

test_summary
