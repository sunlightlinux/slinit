package snapshot

// SoftRebootPath is where slinit drops the operator-intent snapshot
// just before re-exec'ing on `slinitctl shutdown soft-reboot`. Lives
// under /run on purpose: tmpfs survives an in-place exec but does not
// persist across a real boot — a hard reboot must come up clean from
// the boot graph rather than replaying a stale snapshot.
const SoftRebootPath = "/run/slinit/soft-reboot-snapshot.json"
