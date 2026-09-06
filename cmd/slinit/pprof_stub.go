//go:build !pprof

package main

import "github.com/sunlightlinux/slinit/pkg/logging"

// maybeStartPprof is a no-op in production builds. The counterpart
// in pprof.go (guarded by `//go:build pprof`) exposes pprof on
// /run/slinit/pprof.sock; that version + the whole net/http +
// net/http/pprof surface are excluded from stock builds to keep
// the binary lean and the attack surface small.
func maybeStartPprof(_ *logging.Logger) {}
