# fixture.sh — the services perf cases aim at, run ON the target by run.sh:
#
#   sh fixture.sh up   DIR    provision and start them; DIR holds the script
#   sh fixture.sh down DIR    stop, unload and remove them
#
# perf-target      type=process, log-type=buffer. A shell that survives
#                  HUP and ignores WINCH, so cases can signal it 30 times
#                  over; it prints a line at start and on each HUP, so
#                  catlog has something to return.
# perf-target-dep  type=internal: the far end of 430's add-dep/rm-dep edge.
#
# The names must match PERF_TARGET / PERF_TARGET_DEP in remote-prelude.sh.
# They are fixed rather than per-run so `up` replaces a leftover from a run
# that was killed before `down`.
set -u
_dir="$2"
_target=perf-target
_dep=perf-target-dep

_down() {
    for _s in "$_target" "$_dep"; do
        slinitctl stop "$_s" > /dev/null 2>&1
        slinitctl unload "$_s" > /dev/null 2>&1
        rm -f "/etc/slinit.d/$_s"
    done
    return 0
}

case "$1" in
up)
    _down
    # The trap kills the current sleep so a HUP does not leave one behind:
    # 480 alone sends 30.
    cat > "$_dir/perf-target.sh" <<'EOF'
#!/bin/sh
echo "perf-target up"
_p=
trap 'kill $_p 2>/dev/null; echo hup' HUP
trap 'kill $_p 2>/dev/null; exit 0' TERM
while :; do
    sleep 3600 &
    _p=$!
    wait $_p
done
EOF
    printf 'type = process\ncommand = /bin/sh %s/perf-target.sh\nlog-type = buffer\n' \
        "$_dir" > "/etc/slinit.d/$_target"
    printf 'type = internal\n' > "/etc/slinit.d/$_dep"
    slinitctl start "$_dep" > /dev/null &&
        slinitctl start "$_target" > /dev/null || {
        echo "fixture: could not start $_target / $_dep" >&2
        _down
        exit 1
    }
    ;;
down)
    _down
    ;;
*)
    echo "usage: fixture.sh up|down DIR" >&2
    exit 2
    ;;
esac
