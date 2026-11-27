#!/bin/sh
# Test: a restarted log-type = buffer service still has its output read.
#
# The service keeps one log buffer across restarts and each run gets a
# fresh pipe. Up to v4.0.3 the reader of the previous run's pipe was
# still running when something held that pipe's write end (here a
# setsid'd sleep the first run leaves behind), and nothing read the new
# pipe: the new run's output never reached `slinitctl catlog`, and after
# 64 KiB the service blocked in write(2) on stdout for good (f042a6c).
#
# The second run writes 100 KB (more than a pipe holds) and only then
# drops a marker file, so a missing reader shows twice: no marker, and no
# second-run line in catlog.

R=/run/306
rm -rf "$R"; mkdir -p "$R"
cat > "$R/run.sh" <<'EOF'
#!/bin/sh
R=/run/306
if [ ! -e "$R/first" ]; then
    : > "$R/first"
    setsid sleep 60 &          # keeps this run's stdout open
    echo first-run-306
    exit 1
fi
echo second-run-306
head -c 100000 /dev/zero | tr '\000' x
echo
: > "$R/second-done"
exec sleep 600
EOF
chmod +x "$R/run.sh"

printf '%s\n' 'type = process' "command = $R/run.sh" 'log-type = buffer' \
    'log-buffer-size = 4096' 'restart = on-failure' 'restart-delay = 1' \
    > /etc/slinit.d/test-306

slinitctl --system --no-wait start test-306 >/dev/null 2>&1

_e=0
while [ ! -e "$R/second-done" ] && [ "$_e" -lt 30 ]; do
    sleep 0.5; _e=$((_e + 1))
done
[ -e "$R/first" ] || skip_case "the first run never happened"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$R/second-done" ]; then
    echo "OK: the second run wrote 100 KB to stdout without blocking"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the second run never finished writing (stdout not read)"
fi
assert_service_state test-306 STARTED "the restarted run is up"
_log=$(timeout 5 slinitctl --system catlog test-306 2>&1)
# The 4 KiB buffer may have rotated past the marker line, but not past
# the tail of the 100 KB block, which only the second run writes.
assert_contains "$_log" "xxxxxxxxxxxxxxxx" "catlog has the second run's output"

slinitctl --system stop test-306 >/dev/null 2>&1
test_summary
