# 420-slinitctl-list-actions — list any custom extra-command
# actions declared for the target svc. Most stock svcs declare
# none → empty response, so this measures the empty-list path
# specifically.
perf_run_iters "$ITERS" "CtlListActions_perftarget" "slinitctl list-actions $PERF_TARGET"
