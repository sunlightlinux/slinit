// slinit-check — offline configuration linter for slinit service files.
//
// Loads and validates service descriptions without requiring a running
// slinit instance. Checks include: parse errors, dependency cycles,
// consumer-of relationships, executable paths, and file accessibility.
//
// Usage:
//
//	slinit-check [options] [service-name ...]
//
// If no service names are given, "boot" is checked by default.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func main() {
	dirs := []string{}
	services := []string{}
	var envFile string

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--services-dir":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			dirs = append(dirs, args[i])
		case "-s", "--system":
			dirs = defaultSystemDirs()
		case "-u", "--user":
			dirs = defaultUserDirs()
		case "-e", "--env-file":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			envFile = args[i]
		case "--help", "-h":
			printUsage()
			os.Exit(0)
		default:
			if strings.HasPrefix(args[i], "-") {
				fatal("unknown option: %s", args[i])
			}
			services = append(services, args[i])
		}
	}

	if len(dirs) == 0 {
		dirs = defaultSystemDirs()
	}

	if len(services) == 0 {
		services = []string{"boot"}
	}

	// Load env file if specified
	if envFile != "" {
		envVars, err := readEnvFile(envFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read env-file %q: %v\n", envFile, err)
		} else {
			for _, e := range envVars {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					os.Setenv(parts[0], parts[1])
				}
			}
		}
	}

	logger := logging.New(logging.LevelInfo)
	set := service.NewServiceSet(logger)
	loader := config.NewDirLoader(set, dirs)
	set.SetLoader(loader)

	var errors int
	var warnings int

	// Phase 1: Load and parse each service (recursively with deps)
	for _, name := range services {
		fmt.Printf("Checking service: %s...\n", name)

		svc, err := loader.LoadService(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ERROR: %v\n", err)
			errors++
			continue
		}
		_ = svc
	}

	// Phase 2: Secondary checks on all loaded services
	fmt.Println("\nPerforming secondary checks...")

	allServices := set.ListServices()
	for _, svc := range allServices {
		name := svc.Name()

		// Find the service file to re-parse for deeper checks
		desc, path := findServiceDesc(dirs, name)
		if desc == nil {
			continue // Already reported during load
		}

		// Check command executable
		if len(desc.Command) > 0 {
			w := checkExecutable(desc.Command[0], name, "command", path)
			warnings += w
		} else if desc.Type != service.TypeInternal && desc.Type != service.TypeTriggered {
			fmt.Fprintf(os.Stderr, "  WARNING [%s]: no command specified for %s service\n",
				name, desc.Type)
			warnings++
		}

		// Check stop-command executable
		if len(desc.StopCommand) > 0 {
			w := checkExecutable(desc.StopCommand[0], name, "stop-command", path)
			warnings += w
		}

		// Check working directory
		if desc.WorkingDir != "" {
			if info, err := os.Stat(desc.WorkingDir); err != nil {
				fmt.Fprintf(os.Stderr, "  WARNING [%s]: working-dir %q: %v\n",
					name, desc.WorkingDir, err)
				warnings++
			} else if !info.IsDir() {
				fmt.Fprintf(os.Stderr, "  WARNING [%s]: working-dir %q is not a directory\n",
					name, desc.WorkingDir)
				warnings++
			}
		}

		// Check env-file
		if desc.EnvFile != "" {
			if _, err := os.Stat(desc.EnvFile); err != nil {
				fmt.Fprintf(os.Stderr, "  WARNING [%s]: env-file %q: %v\n",
					name, desc.EnvFile, err)
				warnings++
			}
		}

		// Check PID file directory
		if desc.PIDFile != "" {
			dir := filepath.Dir(desc.PIDFile)
			if _, err := os.Stat(dir); err != nil {
				fmt.Fprintf(os.Stderr, "  WARNING [%s]: pid-file directory %q: %v\n",
					name, dir, err)
				warnings++
			}
		}

		// Check log file directory
		if desc.LogType == service.LogToFile && desc.LogFile != "" {
			dir := filepath.Dir(desc.LogFile)
			if _, err := os.Stat(dir); err != nil {
				fmt.Fprintf(os.Stderr, "  WARNING [%s]: logfile directory %q: %v\n",
					name, dir, err)
				warnings++
			}
		}

		// Check socket path directory
		if desc.SocketPath != "" {
			dir := filepath.Dir(desc.SocketPath)
			if _, err := os.Stat(dir); err != nil {
				fmt.Fprintf(os.Stderr, "  WARNING [%s]: socket-listen directory %q: %v\n",
					name, dir, err)
				warnings++
			}
		}
	}

	fmt.Println("\nSecondary checks complete.")

	if errors == 0 && warnings == 0 {
		fmt.Println("No problems found.")
		os.Exit(0)
	}

	parts := []string{}
	if errors > 0 {
		parts = append(parts, fmt.Sprintf("%d error(s)", errors))
	}
	if warnings > 0 {
		parts = append(parts, fmt.Sprintf("%d warning(s)", warnings))
	}
	fmt.Printf("%s issued.\n", strings.Join(parts, " and "))

	if errors > 0 {
		os.Exit(1)
	}
}

func checkExecutable(path, svcName, setting, svcFile string) int {
	warns := 0

	if !filepath.IsAbs(path) {
		fmt.Fprintf(os.Stderr, "  WARNING [%s] (%s): %s %q is not an absolute path\n",
			svcName, svcFile, setting, path)
		warns++
		return warns
	}

	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARNING [%s] (%s): %s %q: %v\n",
			svcName, svcFile, setting, path, err)
		warns++
		return warns
	}

	if info.IsDir() {
		fmt.Fprintf(os.Stderr, "  WARNING [%s] (%s): %s %q is a directory\n",
			svcName, svcFile, setting, path)
		warns++
		return warns
	}

	if info.Mode()&0111 == 0 {
		fmt.Fprintf(os.Stderr, "  WARNING [%s] (%s): %s %q is not executable\n",
			svcName, svcFile, setting, path)
		warns++
	}

	return warns
}

func findServiceDesc(dirs []string, name string) (*config.ServiceDescription, string) {
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()

		desc, err := config.Parse(f, name, path)
		if err != nil {
			return nil, ""
		}
		return desc, path
	}
	return nil, ""
}

func readEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "=") {
			result = append(result, line)
		}
	}
	return result, nil
}

func defaultSystemDirs() []string {
	return []string{"/etc/slinit.d", "/usr/lib/slinit.d", "/lib/slinit.d"}
}

func defaultUserDirs() []string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "~"
	}
	return []string{
		filepath.Join(home, ".config", "slinit.d"),
		"/etc/slinit.d",
	}
}

func printUsage() {
	fmt.Println(`Usage: slinit-check [options] [service-name ...]

Offline configuration linter for slinit service files.

If no service names are given, "boot" is checked by default.

Options:
  -d, --services-dir <dir>   Service directory to search (can be repeated)
  -s, --system               Use system service directories
  -u, --user                 Use user service directories
  -e, --env-file <file>      Load environment variables from file
  -h, --help                 Show this help message`)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "slinit-check: "+format+"\n", args...)
	os.Exit(2)
}
