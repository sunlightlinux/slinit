package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// systemd drop-ins: foo.service.d/*.conf beside a unit, in any of the
// unit directories, adding to or overriding the unit's assignments.
// `systemctl edit foo` writes /etc/systemd/system/foo.service.d/
// override.conf, and distributions ship drop-ins to adjust upstream
// units, so a unit read without them is not the unit systemd runs: the
// loader used to ignore them, and an admin's override changed nothing.
//
// The search follows systemd's src/shared/dropin.c and
// src/basic/conf-files.c:
//
//   - names, most specific first: the unit; for an instance, its
//     template; then the dash prefixes, so foo-bar-baz.service also
//     reads foo-bar-.service.d and foo-.service.d; and last the
//     top-level service.d, which applies to every service;
//   - directories: for each unit directory, in priority order (/etc
//     before /run before /usr/lib), each name's NAME.d that exists;
//   - files: every *.conf in those directories, one per file name, the
//     first found winning, so /etc/foo.service.d/10-x.conf overrides
//     /usr/lib/foo.service.d/10-x.conf and a /dev/null symlink of that
//     name masks it; applied in the lexical order of the file names.

// systemdDropinNames returns the names whose NAME.d directories hold
// drop-ins for unit, most specific first, the top-level "service" last.
func systemdDropinNames(unit string) []string {
	var names []string
	seen := map[string]bool{}
	var expand func(name string)
	expand = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
		base, ok := strings.CutSuffix(name, ".service")
		if !ok {
			return
		}
		prefix, instance, isInstance := base, "", false
		if i := strings.IndexByte(base, '@'); i >= 0 {
			prefix, instance = base[:i], base[i+1:]
			if instance != "" {
				isInstance = true
				expand(prefix + "@.service") // the template
			}
		}
		// The dash prefix: "foo-bar" gives "foo-", and "foo-" (already a
		// prefix, with its trailing dash) gives "foo" with the dash chopped
		// once and then the next dash to the left, as unit_file_expand_
		// dropin_names does. A leading dash ends the walk.
		chopped := false
		for {
			dash := strings.LastIndexByte(prefix, '-')
			if dash <= 0 {
				return
			}
			if dash+1 < len(prefix) || chopped {
				prefix = prefix[:dash+1]
				break
			}
			prefix = prefix[:dash]
			chopped = true
		}
		// unit_name_build_from_type: an instance keeps its instance, a
		// template loses its "@" (foo-bar@.service gives foo-.service).
		built := prefix
		if isInstance {
			built += "@" + instance
		}
		expand(built + ".service")
	}
	expand(unit)
	if !seen["service"] {
		names = append(names, "service")
	}
	return names
}

// systemdDropins returns the drop-in files of unit (a NAME.service file
// name) found in unitDirs, highest priority first, in the order systemd
// applies them.
func systemdDropins(unitDirs []string, unit string) []string {
	names := systemdDropinNames(unit)
	byFile := map[string]string{} // file name -> path; the first one found wins
	for _, dir := range unitDirs {
		for _, n := range names {
			entries, err := os.ReadDir(filepath.Join(dir, n+".d"))
			if err != nil {
				continue
			}
			for _, e := range entries {
				f := e.Name()
				if !strings.HasSuffix(f, ".conf") {
					continue
				}
				if _, ok := byFile[f]; !ok {
					byFile[f] = filepath.Join(dir, n+".d", f)
				}
			}
		}
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = byFile[f]
	}
	return out
}

// SystemdDropins returns the drop-in files that apply to the unit file
// named unit (NAME.service) in unitDirs, in the order they are applied.
func SystemdDropins(unitDirs []string, unit string) []string {
	return systemdDropins(unitDirs, unit)
}
