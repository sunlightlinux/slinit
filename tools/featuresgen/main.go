// tools/featuresgen — regenerates pkg/features/zdiscovered.go, the
// directive and opcode names slinit-supports answers from.
//
//	go generate ./pkg/features
//	go run ./tools/featuresgen -root .
//
// The names are discovered from pkg/config/parser.go and
// pkg/control/protocol.go. slinit-supports used to run that discovery
// at run time against ./pkg/... relative to its working directory, so
// an installed copy (no source tree) answered from the hand-curated
// table alone and called real directives unsupported. Compiling the
// lists in makes the answer the build's own; TestBuiltinListsCurrent
// in pkg/features fails when the committed file falls behind.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sunlightlinux/slinit/pkg/features"
)

func main() {
	root := flag.String("root", ".", "module root")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "featuresgen:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	opcodes, directives, err := features.DiscoverAll(root)
	if err != nil {
		return err
	}
	src, err := features.RenderDiscovered(opcodes, directives)
	if err != nil {
		return err
	}
	out := filepath.Join(root, features.GeneratedFile)
	if err := os.WriteFile(out, src, 0o644); err != nil {
		return err
	}
	fmt.Printf("featuresgen: %d opcodes, %d directives -> %s\n", len(opcodes), len(directives), out)
	return nil
}
