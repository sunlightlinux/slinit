# 630-journalctl-kernel-dmesg — `-k` (kernel-only) filter. slinit-
# journalctl reads /dev/kmsg for kernel messages; measures the
# alternate read path (kmsg buffer, not journal file). Very
# different from the journal-file path measured in 050/080/210 —
# same command, different backend.
perf_run_iters "$ITERS" "JournalFetch_kernel_n100" "slinit-journalctl -k -n 100"
