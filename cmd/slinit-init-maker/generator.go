package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// Config drives slinit-init-maker. It carries every tunable a user can
// override from the command line. Defaults are set by applyDefaults()
// and should be suitable for a typical Linux server installation.
type Config struct {
	// OutputDir is the service-description directory to populate.
	// Generated service files live directly inside this directory.
	OutputDir string

	// Force overwrites existing files without prompting. Without it,
	// the generator refuses to touch a file that already exists.
	Force bool

	// DryRun prints what would be written instead of touching the disk.
	DryRun bool

	// BootServiceName is the name of the top-level internal service
	// that collects every other boot target as a dependency.
	BootServiceName string

	// SlinitBin is the absolute path to the slinit binary — embedded
	// into the generated README as a hint for users wiring the
	// kernel cmdline.
	SlinitBin string

	// GettyCount is the number of virtual terminals to generate.
	// Set to 0 to skip getty generation entirely.
	GettyCount int

	// GettyCmd is the getty binary used for tty login.
	// Common choices: /sbin/agetty (util-linux) or /sbin/mingetty.
	GettyCmd string

	// GettyBaud is passed to agetty via --keep-baud. Ignored for
	// getty implementations that don't accept it.
	GettyBaud int

	// Hostname is written into the env-file so services inherit it.
	// Empty skips the hostname entry.
	Hostname string

	// Timezone is written into the env-file as TZ=. Empty skips it.
	Timezone string

	// WithMounts emits a system-mounts service that runs `mount -a`.
	WithMounts bool

	// WithNetwork emits a stub network service that the user can
	// replace with something real.
	WithNetwork bool

	// WithShutdownHook writes a commented template shutdown hook to
	// <OutputDir>/shutdown-hook.sample. The file is never executable
	// by default.
	WithShutdownHook bool
}

// DefaultConfig returns a Config populated with sensible defaults.
// Callers typically mutate a few fields after this.
func DefaultConfig() Config {
	return Config{
		OutputDir:        "/etc/slinit/boot.d",
		BootServiceName:  "boot",
		SlinitBin:        "/sbin/slinit",
		GettyCount:       6,
		GettyCmd:         "/sbin/agetty",
		GettyBaud:        38400,
		WithMounts:       true,
		WithNetwork:      false,
		WithShutdownHook: false,
	}
}

// Validate checks the config for internal consistency. Returns a
// human-readable error for display, not a wrapped sentinel.
func (c *Config) Validate() error {
	if c.OutputDir == "" {
		return fmt.Errorf("output directory is required")
	}
	if c.BootServiceName == "" {
		return fmt.Errorf("boot service name is required")
	}
	if c.GettyCount < 0 {
		return fmt.Errorf("getty count must be non-negative, got %d", c.GettyCount)
	}
	if c.GettyCount > 64 {
		return fmt.Errorf("getty count %d is unreasonably large", c.GettyCount)
	}
	if c.GettyCmd == "" && c.GettyCount > 0 {
		return fmt.Errorf("getty command is required when getty-count > 0")
	}
	// A blank hostname or timezone is fine — the env-file just omits them.
	return nil
}

// generatedFile is a single unit of output: a path relative to OutputDir
// and the body to write at that path.
type generatedFile struct {
	path string
	body string
	// exec marks files that should be chmod +x (scripts, hooks).
	exec bool
}

// Plan computes every file the generator will write for a given config.
// The returned slice is deterministic-sorted so diffs between runs only
// appear when the config actually changed.
func Plan(c Config) ([]generatedFile, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	var files []generatedFile

	// Collect the names of every service the top-level boot service
	// should wait for, so we can render the "waits-for:" lines in one
	// pass at the end.
	var waits []string

	// system-init: a pure marker service every other service depends on.
	// Having a single synchronization point makes reload/ordering simpler.
	files = append(files, generatedFile{
		path: "system-init",
		body: renderTemplate(tmplSystemInit, c),
	})
	waits = append(waits, "system-init")

	// system-mounts: scripted `mount -a` to bring up everything in fstab
	// that did not come up from the initramfs.
	if c.WithMounts {
		files = append(files, generatedFile{
			path: "system-mounts",
			body: renderTemplate(tmplSystemMounts, c),
		})
		waits = append(waits, "system-mounts")
	}

	if c.WithNetwork {
		files = append(files, generatedFile{
			path: "network",
			body: renderTemplate(tmplNetwork, c),
		})
		waits = append(waits, "network")
	}

	for i := 1; i <= c.GettyCount; i++ {
		name := fmt.Sprintf("getty-tty%d", i)
		ttyData := struct {
			Config
			Index int
			TTY   string
		}{
			Config: c,
			Index:  i,
			TTY:    fmt.Sprintf("tty%d", i),
		}
		files = append(files, generatedFile{
			path: name,
			body: renderTemplate(tmplGetty, ttyData),
		})
		waits = append(waits, name)
	}

	// Top-level boot target: internal service with waits-for pointing at
	// every real target above. Using waits-for (not depends-on) means a
	// failing getty does not tear the whole boot down.
	sort.Strings(waits)
	bootData := struct {
		Config
		Waits []string
	}{
		Config: c,
		Waits:  waits,
	}
	files = append(files, generatedFile{
		path: c.BootServiceName,
		body: renderTemplate(tmplBoot, bootData),
	})

	// env-file: lives alongside services and can be loaded via
	// `slinit --env-file`. Empty values are omitted by the template.
	files = append(files, generatedFile{
		path: "env",
		body: renderTemplate(tmplEnvFile, c),
	})

	if c.WithShutdownHook {
		files = append(files, generatedFile{
			path: "shutdown-hook.sample",
			body: renderTemplate(tmplShutdownHook, c),
			exec: false, // sample only — user must explicitly enable
		})
	}

	files = append(files, generatedFile{
		path: "README.md",
		body: renderTemplate(tmplReadme, c),
	})

	// Sort by path so Plan output is deterministic across runs.
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

// WriteAll executes a plan against the filesystem. The output directory
// is created (mkdir -p) unless it already exists. Each file is written
// atomically via <path>.tmp + rename so a failed run leaves no partial
// file behind. Returns the list of absolute paths that were written.
func WriteAll(c Config, files []generatedFile) ([]string, error) {
	if err := os.MkdirAll(c.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", c.OutputDir, err)
	}

	var written []string
	for _, f := range files {
		dst := filepath.Join(c.OutputDir, f.path)
		if !c.Force {
			if _, err := os.Stat(dst); err == nil {
				return written, fmt.Errorf("refusing to overwrite existing file %s (use --force)", dst)
			}
		}
		mode := os.FileMode(0644)
		if f.exec {
			mode = 0755
		}
		if err := writeAtomic(dst, []byte(f.body), mode); err != nil {
			return written, fmt.Errorf("write %s: %w", dst, err)
		}
		written = append(written, dst)
	}
	return written, nil
}

// PrintPlan writes a human-readable summary of what WriteAll would do.
// Used by --dry-run. Writes to w so tests can capture the output.
func PrintPlan(w io.Writer, c Config, files []generatedFile) {
	fmt.Fprintf(w, "slinit-init-maker: dry run — %d file(s) would be written to %s\n",
		len(files), c.OutputDir)
	for _, f := range files {
		marker := "  "
		if f.exec {
			marker = "x "
		}
		fmt.Fprintf(w, "%s%s (%d bytes)\n", marker, filepath.Join(c.OutputDir, f.path), len(f.body))
	}
}

// writeAtomic replaces dst with data by first writing to dst.tmp in the
// same directory and then renaming. This avoids leaving a half-written
// service file behind on a crash mid-generation.
func writeAtomic(dst string, data []byte, mode os.FileMode) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// renderTemplate executes a Go text/template against data and returns
// the result. Template parse errors are programming bugs (the templates
// are string constants in this package), so they panic — a broken
// template must never ship.
func renderTemplate(tmpl string, data any) string {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		panic(fmt.Sprintf("slinit-init-maker: broken template: %v", err))
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		panic(fmt.Sprintf("slinit-init-maker: template execute: %v", err))
	}
	return sb.String()
}
