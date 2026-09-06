# 780-config-parse-error — measure the fail-path latency when
# slinit is asked to load a syntactically broken service file.
# Should return an error quickly — the parser detects malformed
# input on the first bad line. If this is materially slower than
# a happy-path load (case 460), the parser is doing wasted work
# after the first error (partial recovery, deferred validation).
_name="perf-bad-syntax-$$"
_svcfile="/etc/slinit.d/$_name"
# Malformed on purpose: `type` value not one of the accepted enum
# labels; parser rejects immediately.
cat > "$_svcfile" <<EOF
type = thisIsNotAValidType
command = /bin/true
EOF

perf_run_iters "$ITERS" "CtlStart_parse_error_negative" \
    "slinitctl start $_name || true"

# Teardown (slinit didn't load it, so unload would fail; just rm file)
rm -f "$_svcfile"
