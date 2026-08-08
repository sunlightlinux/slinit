#!/bin/sh
# 196-metadata-render-e2e — author / version / usage directives
# surface as labeled lines in `slinitctl status` output. Functional
# test 173-metadata covers the parse path; this case verifies the
# CLI rendering on a live daemon (end-to-end).

SVC="acceptance-test-metadata"
SVC_BARE="acceptance-test-metadata-bare"

cleanup() { svc_remove "$SVC" "$SVC_BARE"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
author = alice@example.org
version = 2.7.1-alpha
usage = svc --config /etc/foo
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

svc_deploy "$SVC_BARE" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
slinitctl --system start "$SVC_BARE" >/dev/null
wait_for_service "$SVC" "STARTED" 10
wait_for_service "$SVC_BARE" "STARTED" 10

_out=$(slinitctl --system status "$SVC" 2>&1)

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*"Author:"*"alice@example.org"*) echo "OK: Author renders in status output" ;;
*) _TESTS_FAILED=$((_TESTS_FAILED + 1))
   echo "FAIL: Author line missing or wrong" ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*"Version:"*"2.7.1-alpha"*) echo "OK: Version renders in status output" ;;
*) _TESTS_FAILED=$((_TESTS_FAILED + 1))
   echo "FAIL: Version line missing or wrong" ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*"Usage:"*"svc --config /etc/foo"*) echo "OK: Usage renders in status output" ;;
*) _TESTS_FAILED=$((_TESTS_FAILED + 1))
   echo "FAIL: Usage line missing or wrong" ;;
esac

# Bare service: NO metadata labels should render — the CLI gates
# each line on the field being non-empty. A leak would show phantom
# empty "Author: " lines on every service without metadata.
_bare=$(slinitctl --system status "$SVC_BARE" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_bare" in
*"Author:"*|*"Version:"*|*"Usage:"*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: bare svc status shows metadata labels despite no directives" ;;
*)
    echo "OK: bare svc has no phantom metadata lines" ;;
esac

test_summary
