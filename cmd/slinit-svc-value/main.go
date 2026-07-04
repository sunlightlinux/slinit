// slinit-svc-value — OpenRC-compatible per-service key=value store.
//
// One binary dispatches every applet from OpenRC's `value` family
// (service_get_value, service_set_value, service_export, plus the
// get_options/save_options aliases OpenRC still ships) by inspecting
// basename(argv[0]). Installers ship symlinks whose names match the
// applet an init.d script already invokes.
//
// The tool is stateless per invocation. Service identity and store
// root come from the environment:
//
//   RC_SVCNAME | SLINIT_SERVICENAME   service the values belong to
//   RC_SVCDIR                         alternative runtime dir
//                                     (defaults to /run/slinit)
//
// Backing: one file per key under $RC_SVCDIR/options/$SVC/$KEY, byte-
// for-byte compatible with OpenRC's librc layout.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitBadUsage = 2
)

var version = "dev"

func main() {
	name := filepath.Base(os.Args[0])
	// Strip the `slinit-` prefix so a symlink installed as either
	// `service_get_value` or `slinit-service_get_value` dispatches
	// the same way. Matches the pattern slinit-einfo established.
	name = strings.TrimPrefix(name, "slinit-")
	os.Exit(dispatch(name, os.Args[1:]))
}

func dispatch(applet string, argv []string) int {
	switch applet {
	case "-h", "--help", "help":
		printUsage()
		return exitOK
	case "-V", "--version", "version":
		fmt.Printf("slinit-svc-value %s\n", version)
		return exitOK
	}

	s, err := newStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", applet, err)
		return exitBadUsage
	}

	switch applet {
	case "service_get_value", "get_options":
		return runGet(s, applet, argv)
	case "service_set_value", "save_options":
		return runSet(s, applet, argv)
	case "service_export":
		return runExport(s, applet, argv)
	default:
		fmt.Fprintf(os.Stderr, "slinit-svc-value: unknown applet %q\n", applet)
		return exitBadUsage
	}
}

func runGet(s *store, applet string, argv []string) int {
	if len(argv) < 1 {
		fmt.Fprintf(os.Stderr, "%s: missing key argument\n", applet)
		return exitBadUsage
	}
	val, ok, err := s.Get(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", applet, err)
		return exitFailure
	}
	if !ok {
		return exitFailure
	}
	// Emit without adding a newline — the OpenRC C original does the
	// same, and consumers embed the output directly in shell
	// expansions where a trailing \n would be surprising.
	fmt.Print(val)
	return exitOK
}

func runSet(s *store, applet string, argv []string) int {
	if len(argv) < 1 {
		fmt.Fprintf(os.Stderr, "%s: missing key argument\n", applet)
		return exitBadUsage
	}
	value := ""
	if len(argv) >= 2 {
		value = argv[1]
	}
	if err := s.Set(argv[0], value); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", applet, err)
		return exitFailure
	}
	return exitOK
}

func runExport(s *store, applet string, argv []string) int {
	if len(argv) < 1 {
		fmt.Fprintf(os.Stderr, "%s: missing variable argument\n", applet)
		return exitBadUsage
	}
	missing := s.Export(argv)
	for _, v := range missing {
		fmt.Fprintf(os.Stderr, "%s: %s: variable is not set, skipping\n", applet, v)
	}
	return exitOK
}

func printUsage() {
	fmt.Print(`Usage: (dispatch via applet name; ship as symlinks)

  service_get_value KEY          print stored value for KEY (exit 1 on miss)
  get_options       KEY          alias for service_get_value
  service_set_value KEY [VALUE]  persist KEY=VALUE; empty VALUE deletes
  save_options      KEY [VALUE]  alias for service_set_value
  service_export    VAR [VAR...] capture each VAR from env if not stored

Environment:
  RC_SVCNAME or SLINIT_SERVICENAME  service name (required)
  RC_SVCDIR                         alternative runtime dir (default /run/slinit)

Backing store: <RC_SVCDIR>/options/<SVCNAME>/<KEY>

Exit codes: 0 ok  1 key missing or write failed  2 bad usage
`)
}
