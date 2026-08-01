// slinit-supports — self-introspection CLI.
//
// Answers "does slinit support X?" and where the feature originated
// (dinit, systemd, runit, s6, OpenRC, upstart, slinit-native). Uses
// pkg/features's hybrid canonical-list-plus-annotation design: the
// list of accepted directives + opcodes is auto-discovered from
// slinit's own source at test-time, so this CLI's answers can't
// drift from what slinit actually parses.
//
// Modes:
//
//	slinit-supports NAME               # yes/no + provenance for one entry
//	slinit-supports --list-directives  # every directive slinit's parser accepts
//	slinit-supports --list-opcodes     # every control-protocol opcode
//	slinit-supports --list-options     # every value valid under `options:`
//	slinit-supports --format=json      # machine-readable dump
//	slinit-supports --format=markdown  # doc/features.md source-of-truth
//	slinit-supports --group-by=source|category  # grouping for list/markdown modes
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/features"
)

// version stamped at build time via ldflags -X main.version=vX.Y.Z.
var version = "dev"

// The pkg/features Load call needs the auto-discovered lists from
// pkg/config/parser.go and pkg/control/protocol.go. Two source paths
// slinit-supports handles:
//   - test / development: relative paths within the module
//   - installed binary: no source available → provenance-only mode
//     (still fully useful for the yes/no + list use cases since
//     provenance already enumerates every real feature).
type options struct {
	name        string
	listMode    string // "" | "directives" | "opcodes" | "options" | "all"
	groupBy     string // "" | "source" | "category" | "kind"
	format      string // "text" | "json" | "markdown"
	showVersion bool
	showHelp    bool
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-supports: %v\n", err)
		os.Exit(2)
	}
	if opts.showHelp {
		printHelp(os.Stdout)
		return
	}
	if opts.showVersion {
		fmt.Println(version)
		return
	}

	registry, _ := loadRegistry()
	if err := run(os.Stdout, registry, opts); err != nil {
		fmt.Fprintf(os.Stderr, "slinit-supports: %v\n", err)
		os.Exit(1)
	}
}

// loadRegistry attempts source auto-discovery from a few likely
// locations. When source isn't reachable (installed binary), returns
// a provenance-only registry — the manual annotation table still
// covers every real feature; the discovery pass only adds
// placeholder "TODO" rows and reports drift.
func loadRegistry() (*features.Registry, features.ReconcileReport) {
	for _, base := range []string{".", "./..", "../..", "../../.."} {
		parser := base + "/" + features.DiscoverParserFile
		proto := base + "/" + features.DiscoverProtocolFile
		if _, err := os.Stat(parser); err != nil {
			continue
		}
		if _, err := os.Stat(proto); err != nil {
			continue
		}
		opcodes, oerr := features.DiscoverOpcodes(proto)
		if oerr != nil {
			continue
		}
		directives, derr := features.DiscoverDirectives(parser)
		if derr != nil {
			continue
		}
		return features.Load(opcodes, directives)
	}
	// Fallback: annotation-table-only (no discovery pass).
	return features.Load(nil, nil)
}

func parseArgs(args []string) (options, error) {
	opts := options{format: "text"}
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-h" || a == "--help":
			opts.showHelp = true
			return opts, nil
		case a == "--version":
			opts.showVersion = true
			return opts, nil
		case a == "--list-directives":
			opts.listMode = "directives"
			args = args[1:]
		case a == "--list-opcodes":
			opts.listMode = "opcodes"
			args = args[1:]
		case a == "--list-options":
			opts.listMode = "options"
			args = args[1:]
		case a == "--list-all":
			opts.listMode = "all"
			args = args[1:]
		case a == "--group-by":
			if len(args) < 2 {
				return opts, errors.New("--group-by requires an argument")
			}
			opts.groupBy = args[1]
			args = args[2:]
		case strings.HasPrefix(a, "--group-by="):
			opts.groupBy = strings.TrimPrefix(a, "--group-by=")
			args = args[1:]
		case a == "--format":
			if len(args) < 2 {
				return opts, errors.New("--format requires an argument")
			}
			opts.format = args[1]
			args = args[2:]
		case strings.HasPrefix(a, "--format="):
			opts.format = strings.TrimPrefix(a, "--format=")
			args = args[1:]
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("unknown flag %q (try -h)", a)
		default:
			if opts.name != "" {
				return opts, fmt.Errorf("multiple positional arguments not supported (%q and %q)", opts.name, a)
			}
			opts.name = a
			args = args[1:]
		}
	}
	// Format validation.
	switch opts.format {
	case "text", "json", "markdown":
	default:
		return opts, fmt.Errorf("--format must be text|json|markdown (got %q)", opts.format)
	}
	// Group-by validation.
	if opts.groupBy != "" {
		switch opts.groupBy {
		case "source", "category", "kind":
		default:
			return opts, fmt.Errorf("--group-by must be source|category|kind (got %q)", opts.groupBy)
		}
	}
	return opts, nil
}

// run dispatches: single-name lookup vs list mode.
func run(out io.Writer, reg *features.Registry, opts options) error {
	if opts.listMode != "" {
		return runList(out, reg, opts)
	}
	if opts.name == "" {
		return errors.New("no name given (use --list-directives / --list-opcodes / --list-all, or pass a name to look up)")
	}
	return runLookup(out, reg, opts)
}

func runLookup(out io.Writer, reg *features.Registry, opts options) error {
	f := reg.Lookup(opts.name)
	if f == nil {
		if opts.format == "json" {
			fmt.Fprintln(out, `{"supported":false}`)
			return fmt.Errorf("no")
		}
		fmt.Fprintf(out, "No — slinit does not accept %q as a directive, opcode, or option.\n", opts.name)
		return fmt.Errorf("no")
	}
	switch opts.format {
	case "json":
		return json.NewEncoder(out).Encode(struct {
			Supported bool              `json:"supported"`
			Feature   *features.Feature `json:"feature"`
		}{true, f})
	case "markdown":
		return renderMarkdownEntry(out, f)
	default:
		return renderTextEntry(out, f)
	}
}

func renderTextEntry(out io.Writer, f *features.Feature) error {
	fmt.Fprintf(out, "Yes  %s (%s)\n", f.Name, f.Kind)
	fmt.Fprintf(out, "     Source:   %s\n", f.Source)
	fmt.Fprintf(out, "     Category: %s\n", f.Category)
	if len(f.Aliases) > 0 {
		fmt.Fprintf(out, "     Aliases:  %s\n", strings.Join(f.Aliases, ", "))
	}
	if f.DocURL != "" {
		fmt.Fprintf(out, "     DocURL:   %s\n", f.DocURL)
	}
	if f.Since != "" {
		fmt.Fprintf(out, "     Since:    %s\n", f.Since)
	}
	if f.Notes != "" {
		fmt.Fprintf(out, "     Notes:    %s\n", f.Notes)
	}
	return nil
}

func renderMarkdownEntry(out io.Writer, f *features.Feature) error {
	fmt.Fprintf(out, "### `%s`\n\n", f.Name)
	fmt.Fprintf(out, "- **Kind:** %s\n", f.Kind)
	fmt.Fprintf(out, "- **Source:** %s\n", f.Source)
	fmt.Fprintf(out, "- **Category:** %s\n", f.Category)
	if len(f.Aliases) > 0 {
		fmt.Fprintf(out, "- **Aliases:** %s\n", strings.Join(f.Aliases, ", "))
	}
	if f.DocURL != "" {
		fmt.Fprintf(out, "- **Docs:** %s\n", f.DocURL)
	}
	if f.Notes != "" {
		fmt.Fprintf(out, "- %s\n", f.Notes)
	}
	fmt.Fprintln(out)
	return nil
}

func runList(out io.Writer, reg *features.Registry, opts options) error {
	items := filterForList(reg, opts.listMode)
	if len(items) == 0 {
		fmt.Fprintln(out, "(empty)")
		return nil
	}
	switch opts.format {
	case "json":
		return json.NewEncoder(out).Encode(items)
	case "markdown":
		return renderMarkdownList(out, items, opts.groupBy)
	default:
		return renderTextList(out, items, opts.groupBy)
	}
}

func filterForList(reg *features.Registry, mode string) []*features.Feature {
	all := reg.All()
	if mode == "all" {
		return all
	}
	var want features.Kind
	switch mode {
	case "directives":
		want = features.KindDirective
	case "opcodes":
		want = features.KindOpcode
	case "options":
		want = features.KindOption
	default:
		return all
	}
	out := make([]*features.Feature, 0, len(all))
	for _, f := range all {
		if f.Kind == want {
			out = append(out, f)
		}
	}
	return out
}

func renderTextList(out io.Writer, items []*features.Feature, groupBy string) error {
	if groupBy == "" {
		for _, f := range items {
			fmt.Fprintf(out, "%-40s  %s (%s)\n", f.Name, f.Source, f.Category)
		}
		return nil
	}
	groups := groupItems(items, groupBy)
	keys := sortedKeys(groups)
	for _, k := range keys {
		fmt.Fprintf(out, "\n== %s ==\n", k)
		for _, f := range groups[k] {
			fmt.Fprintf(out, "  %-38s  %s\n", f.Name, f.Notes)
		}
	}
	return nil
}

func renderMarkdownList(out io.Writer, items []*features.Feature, groupBy string) error {
	fmt.Fprintln(out, "# slinit feature surface")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Auto-generated by `slinit-supports --format=markdown --list-all`.")
	fmt.Fprintln(out, "Every entry corresponds to a name slinit's parser or control-protocol")
	fmt.Fprintln(out, "actually accepts (auto-discovered from source), with hand-curated")
	fmt.Fprintln(out, "provenance annotations.")
	fmt.Fprintln(out)
	if groupBy == "" {
		groupBy = "kind"
	}
	groups := groupItems(items, groupBy)
	for _, k := range sortedKeys(groups) {
		fmt.Fprintf(out, "## %s\n\n", k)
		fmt.Fprintln(out, "| Name | Source | Category | Notes |")
		fmt.Fprintln(out, "|------|--------|----------|-------|")
		for _, f := range groups[k] {
			note := strings.ReplaceAll(f.Notes, "|", "\\|")
			fmt.Fprintf(out, "| `%s` | %s | %s | %s |\n", f.Name, f.Source, f.Category, note)
		}
		fmt.Fprintln(out)
	}
	return nil
}

func groupItems(items []*features.Feature, by string) map[string][]*features.Feature {
	out := map[string][]*features.Feature{}
	for _, f := range items {
		var k string
		switch by {
		case "source":
			k = string(f.Source)
		case "category":
			k = string(f.Category)
		case "kind":
			k = string(f.Kind)
		default:
			k = "all"
		}
		out[k] = append(out[k], f)
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].Name < out[k][j].Name })
	}
	return out
}

func sortedKeys(m map[string][]*features.Feature) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `Usage: slinit-supports [flags] [NAME]

Self-introspection: check whether slinit supports a given directive,
opcode, or option — and where the feature originated.

Positional:
  NAME                One directive, opcode, or option name to look up.
                      Omit and pass a --list-* flag for enumeration.

Flags:
      --list-directives    List every directive slinit's parser accepts
      --list-opcodes       List every control-protocol opcode
      --list-options       List every value valid under 'options:'
      --list-all           List everything (directives + opcodes + options)
      --group-by=SPEC      Group list output by 'source', 'category', or 'kind'
      --format=SPEC        Output format: text (default) | json | markdown
      --version            Print version and exit
  -h, --help               Show this help

Examples:
  slinit-supports restart                       # yes/no + provenance
  slinit-supports --list-directives             # names + source
  slinit-supports --list-all --group-by=source  # grouped by upstream
  slinit-supports --format=markdown --list-all  # regenerate doc/features.md
`)
}
