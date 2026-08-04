//go:build !paniconce

package main

import "github.com/sunlightlinux/slinit/pkg/logging"

// maybeArmPanicTimer is a no-op in production builds. The active
// implementation lives in panictest_on.go and is compiled only
// when -tags paniconce is passed. Kept as an unconditional call
// site in main.go so removing the build tag doesn't leave a
// dangling reference; the linker eliminates the call at compile
// time when this file wins.
func maybeArmPanicTimer(_ *logging.Logger) {}
