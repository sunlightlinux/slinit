package features

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// DiscoverOpcodes parses the given file (typically
// pkg/control/protocol.go) and returns every CmdXxx constant name
// found in a const block. It does NOT look at values (that's
// canonicality — pkg/control decides the numbers); we only care
// that the NAME exists so we can cross-reference against the
// provenance table.
//
// Failures (parse error, file missing) propagate — this function
// runs at CI-test time, not in a hot path; a missing/malformed
// source file is a bug worth surfacing loudly.
func DiscoverOpcodes(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("features: parse %s: %w", path, err)
	}
	var names []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if strings.HasPrefix(name.Name, "Cmd") && looksLikeOpcodeIdent(name.Name) {
					names = append(names, name.Name)
				}
			}
		}
	}
	return names, nil
}

// looksLikeOpcodeIdent filters out helper constants that happen to
// start with "Cmd" but aren't opcodes. Real opcodes follow
// PascalCase and are uint8 constants. We can't check the type here
// cheaply without full type-checking, so approximate by name shape
// (no underscore, second char uppercase). Good enough given our
// naming conventions.
func looksLikeOpcodeIdent(s string) bool {
	if len(s) < 4 { // "Cmd" + one letter minimum
		return false
	}
	if strings.Contains(s, "_") {
		return false
	}
	// Fourth character should be uppercase (Cmd + Xxx pattern).
	c := s[3]
	return c >= 'A' && c <= 'Z'
}

// DiscoverDirectives parses the given file (typically
// pkg/config/parser.go) and returns every string literal that
// appears as a case value in a switch statement. Directive keys
// live in `switch key { case "restart": ... case "command": ... }`
// blocks throughout the parser; walking every switch and pulling
// out string case values yields the complete accepted-key surface
// without depending on the parser to enumerate itself.
//
// Some case values are internal helpers ("true", "false", "yes",
// numeric literals) — we filter to keys that look like config
// directive names (lowercase, hyphenated, no colons except the
// `@meta:` and `option:` prefixes we handle separately).
func DiscoverDirectives(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("features: parse %s: %w", path, err)
	}
	seen := map[string]struct{}{}
	// Only walk switches whose tag is the identifier "setting" —
	// that pinpoints applySetting()'s canonical directive dispatcher
	// (pkg/config/parser.go:applySetting), skipping helper value
	// switches (parse type value, restart mode, etc.) that would
	// otherwise pollute the discovered list with non-directives
	// like arch names, log levels, and enum values.
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		tagIdent, ok := sw.Tag.(*ast.Ident)
		if !ok || tagIdent.Name != "setting" {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				bl, ok := expr.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				lit := unquoteString(bl.Value)
				if isDirectiveName(lit) {
					seen[lit] = struct{}{}
				}
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out, nil
}

// unquoteString strips the surrounding quotes from a Go string
// literal. Doesn't handle escape sequences beyond what parser keys
// legitimately use (letters, digits, hyphens, colons, at-sign).
func unquoteString(quoted string) string {
	if len(quoted) < 2 {
		return quoted
	}
	if quoted[0] == '"' && quoted[len(quoted)-1] == '"' {
		return quoted[1 : len(quoted)-1]
	}
	return quoted
}

// isDirectiveName is a light structural check for what CAN be a
// slinit directive name. Since discover restricts itself to
// `switch setting { ... }` (applySetting's canonical dispatcher),
// every case value there IS a directive by definition — we only
// need to guard against pathological cases (empty strings,
// oversized identifiers, unusual chars). NO keyword blacklist
// because valid directive names ("restart", "reload", "start-timeout")
// overlap with common English verbs the parser also happens to accept.
func isDirectiveName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	// Must start with letter or @ (for @meta directives).
	c := s[0]
	if !(c >= 'a' && c <= 'z') && c != '@' {
		return false
	}
	// Rest: lowercase letters, digits, hyphens, single colon
	// (@meta:foo / option:foo patterns show up in some tables).
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == ':' {
			continue
		}
		return false
	}
	return true
}

// DiscoverParserFile is the default location — pkg/config/parser.go
// relative to the module root. Consumers running from module root
// (go test) hit this straight.
const DiscoverParserFile = "pkg/config/parser.go"

// DiscoverProtocolFile is the opcode source file, same convention.
const DiscoverProtocolFile = "pkg/control/protocol.go"
