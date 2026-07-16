#!/bin/sh
# 169-condition-file-not-empty + condition-directory-not-empty — bundled
# because both predicates share the same simple stat-and-check
# semantics. Each is exercised with a positive path (real content) and
# a negative path (empty file / empty directory).

WORK="/run/acceptance-169"
FILE_FULL="$WORK/full.txt"
FILE_EMPTY="$WORK/empty.txt"
DIR_FULL="$WORK/dir-full"
DIR_EMPTY="$WORK/dir-empty"

SVC_FF="acceptance-test-cond-file-full"
SVC_FE="acceptance-test-cond-file-empty"
SVC_DF="acceptance-test-cond-dir-full"
SVC_DE="acceptance-test-cond-dir-empty"

MARK_FF="$WORK/ff.mark"
MARK_FE="$WORK/fe.mark"
MARK_DF="$WORK/df.mark"
MARK_DE="$WORK/de.mark"

cleanup() {
    svc_remove "$SVC_FF" "$SVC_FE" "$SVC_DF" "$SVC_DE"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

rm -rf "$WORK"
mkdir -p "$DIR_FULL" "$DIR_EMPTY"
printf 'has content\n' > "$FILE_FULL"
: > "$FILE_EMPTY"
touch "$DIR_FULL/marker"

svc_deploy "$SVC_FF" <<EOF
type = scripted
condition-file-not-empty = $FILE_FULL
command = /bin/sh -c 'touch $MARK_FF; exit 0'
restart = false
EOF

svc_deploy "$SVC_FE" <<EOF
type = scripted
condition-file-not-empty = $FILE_EMPTY
command = /bin/sh -c 'touch $MARK_FE; exit 0'
restart = false
EOF

svc_deploy "$SVC_DF" <<EOF
type = scripted
condition-directory-not-empty = $DIR_FULL
command = /bin/sh -c 'touch $MARK_DF; exit 0'
restart = false
EOF

svc_deploy "$SVC_DE" <<EOF
type = scripted
condition-directory-not-empty = $DIR_EMPTY
command = /bin/sh -c 'touch $MARK_DE; exit 0'
restart = false
EOF

for _s in "$SVC_FF" "$SVC_FE" "$SVC_DF" "$SVC_DE"; do
    slinitctl --system start "$_s" >/dev/null 2>&1
done
for _s in "$SVC_FF" "$SVC_FE" "$SVC_DF" "$SVC_DE"; do
    wait_for_service "$_s" "STARTED" 10 || true
done

# file-not-empty: full → run, empty → skip
assert_service_state "$SVC_FF" "STARTED" "file-full reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_FF" ]; then
    echo "OK: file-not-empty=true on populated file → command ran"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: populated file did not satisfy file-not-empty"
fi

assert_service_state "$SVC_FE" "STARTED" "file-empty reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_FE" ]; then
    echo "OK: file-not-empty=false on empty file → command skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: empty file spuriously satisfied file-not-empty"
fi

# directory-not-empty: full → run, empty → skip
assert_service_state "$SVC_DF" "STARTED" "dir-full reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARK_DF" ]; then
    echo "OK: directory-not-empty=true on populated dir → command ran"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: populated dir did not satisfy directory-not-empty"
fi

assert_service_state "$SVC_DE" "STARTED" "dir-empty reaches STARTED"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARK_DE" ]; then
    echo "OK: directory-not-empty=false on empty dir → command skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: empty dir spuriously satisfied directory-not-empty"
fi

test_summary
