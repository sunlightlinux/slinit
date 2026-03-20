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
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

func main() {
	dirs := []string{}
	services := []string{}
	var envFile string
	var socketPath string
	onlineMode := false
	userMode := false

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
			userMode = false
		case "-u", "--user":
			dirs = defaultUserDirs()
			userMode = true
		case "-e", "--env-file":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			envFile = args[i]
		case "-n", "--online":
			onlineMode = true
		case "-p", "--socket-path":
			if i+1 >= len(args) {
				fatal("missing argument for %s", args[i])
			}
			i++
			socketPath = args[i]
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

	if onlineMode {
		// Online mode: query running daemon for service dirs and environment
		if socketPath == "" {
			socketPath = resolveCheckSocketPath(userMode)
		}
		remoteDirs, remoteEnv, err := queryDaemon(socketPath)
		if err != nil {
			fatal("online mode: %v", err)
		}
		if len(remoteDirs) > 0 {
			dirs = remoteDirs
		}
		for k, v := range remoteEnv {
			os.Setenv(k, v)
		}
	}

	if len(dirs) == 0 {
		dirs = defaultSystemDirs()
	}

	if len(services) == 0 {
		services = []string{"boot"}
	}

	// Load env file if specified (offline mode or override)
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

	// Phase 2: Cycle detection via topological sort + DFS with pruning
	fmt.Println("\nChecking for dependency cycles...")

	allServices := set.ListServices()
	cycleErrors := detectCycles(allServices)
	if cycleErrors > 0 {
		errors += cycleErrors
	}

	// Phase 3: Depth check — report services exceeding MaxDepDepth
	depthErrors := checkDepthLimits(allServices)
	if depthErrors > 0 {
		errors += depthErrors
	}

	// Phase 4: Secondary checks on all loaded services
	fmt.Println("\nPerforming secondary checks...")
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
	dirs := []string{}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "slinit.d"))
	} else {
		home := os.Getenv("HOME")
		if home == "" {
			home = "~"
		}
		dirs = append(dirs, filepath.Join(home, ".config", "slinit.d"))
	}
	dirs = append(dirs,
		"/etc/slinit.d/user",
		"/usr/lib/slinit.d/user",
		"/usr/local/lib/slinit.d/user",
	)
	return dirs
}

func printUsage() {
	fmt.Println(`Usage: slinit-check [options] [service-name ...]

Configuration linter for slinit service files.

If no service names are given, "boot" is checked by default.

Options:
  -d, --services-dir <dir>   Service directory to search (can be repeated)
  -s, --system               Use system service directories
  -u, --user                 Use user service directories
  -n, --online               Query running daemon for service dirs and env
  -p, --socket-path <path>   Socket path for online mode
  -e, --env-file <file>      Load environment variables from file
  -h, --help                 Show this help message`)
}

const defaultSystemSocket = "/run/slinit.ctl"

func resolveCheckSocketPath(userMode bool) string {
	if !userMode && os.Getuid() == 0 {
		return defaultSystemSocket
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg + "/slinitctl"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultSystemSocket
	}
	return home + "/.slinitctl"
}

// queryDaemon connects to a running slinit instance and retrieves
// service directories and global environment.
func queryDaemon(socketPath string) ([]string, map[string]string, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot connect to %s: %v", socketPath, err)
	}
	defer conn.Close()

	// Query service directories
	if err := control.WritePacket(conn, control.CmdQueryServiceDscDir, nil); err != nil {
		return nil, nil, fmt.Errorf("query service dirs: %v", err)
	}
	rply, payload, err := control.ReadPacket(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("read service dirs reply: %v", err)
	}
	if rply != control.RplyServiceDscDir || len(payload) < 2 {
		return nil, nil, fmt.Errorf("unexpected reply for service dirs: %d", rply)
	}

	count := int(binary.LittleEndian.Uint16(payload))
	off := 2
	var dirs []string
	for i := 0; i < count; i++ {
		if len(payload) < off+2 {
			break
		}
		dirLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if len(payload) < off+dirLen {
			break
		}
		dirs = append(dirs, string(payload[off:off+dirLen]))
		off += dirLen
	}

	// Query global environment (handle=0)
	if err := control.WritePacket(conn, control.CmdGetAllEnv, control.EncodeHandle(0)); err != nil {
		return dirs, nil, fmt.Errorf("query env: %v", err)
	}
	rply, payload, err = control.ReadPacket(conn)
	if err != nil {
		return dirs, nil, fmt.Errorf("read env reply: %v", err)
	}
	if rply != control.RplyEnvList {
		return dirs, nil, fmt.Errorf("unexpected reply for env: %d", rply)
	}

	env, err := control.DecodeEnvList(payload)
	if err != nil {
		return dirs, nil, fmt.Errorf("decode env: %v", err)
	}

	return dirs, env, nil
}

// detectCycles performs DFS-based cycle detection with pruning on the loaded
// service graph. Returns the number of cycles found (reports only the first).
func detectCycles(services []service.Service) int {
	type state int
	const (
		unvisited state = iota
		visiting
		cycleFree
	)

	states := make(map[string]state)
	for _, svc := range services {
		states[svc.Name()] = unvisited
	}

	svcMap := make(map[string]service.Service)
	for _, svc := range services {
		svcMap[svc.Name()] = svc
	}

	// DFS with explicit stack: (service, dep-index)
	type frame struct {
		svc   service.Service
		deps  []*service.ServiceDep
		index int
	}

	for _, root := range services {
		if states[root.Name()] != unvisited {
			continue
		}

		stack := []frame{{svc: root, deps: root.Record().Dependencies(), index: 0}}
		states[root.Name()] = visiting

		for len(stack) > 0 {
			top := &stack[len(stack)-1]

			if top.index >= len(top.deps) {
				// All deps processed — mark cycle-free
				states[top.svc.Name()] = cycleFree
				stack = stack[:len(stack)-1]
				if len(stack) > 0 {
					stack[len(stack)-1].index++
				}
				continue
			}

			dep := top.deps[top.index]
			depName := dep.To.Name()
			depState := states[depName]

			switch depState {
			case cycleFree:
				// Already known cycle-free — prune
				top.index++
			case visiting:
				// Found a cycle — report it
				fmt.Fprintf(os.Stderr, "  ERROR: dependency cycle detected:\n")
				// Find cycle start in stack
				cycleStart := -1
				for i, f := range stack {
					if f.svc.Name() == depName {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					for i := cycleStart; i < len(stack); i++ {
						nextIdx := i + 1
						var nextName string
						if nextIdx < len(stack) {
							nextName = stack[nextIdx].svc.Name()
						} else {
							nextName = depName
						}
						fmt.Fprintf(os.Stderr, "    %s -> %s\n", stack[i].svc.Name(), nextName)
					}
				}
				return 1
			case unvisited:
				depSvc := svcMap[depName]
				if depSvc == nil {
					top.index++
					continue
				}
				states[depName] = visiting
				stack = append(stack, frame{svc: depSvc, deps: depSvc.Record().Dependencies(), index: 0})
			}
		}
	}

	return 0
}

// checkDepthLimits computes the maximum dependency depth for each service
// using BFS (topological order) and reports any that exceed MaxDepDepth.
func checkDepthLimits(services []service.Service) int {
	svcMap := make(map[string]service.Service)
	inDegree := make(map[string]int)
	depth := make(map[string]int)

	for _, svc := range services {
		name := svc.Name()
		svcMap[name] = svc
		inDegree[name] = 0
		depth[name] = 0
	}

	// Count in-degree (only regular/milestone/waits-for deps)
	for _, svc := range services {
		for _, dep := range svc.Record().Dependencies() {
			dt := dep.DepType
			if dt == service.DepBefore || dt == service.DepAfter {
				continue
			}
			if _, ok := inDegree[dep.To.Name()]; ok {
				inDegree[dep.To.Name()]++
			}
		}
	}

	// BFS from roots (in-degree 0)
	var queue []service.Service
	for _, svc := range services {
		if inDegree[svc.Name()] == 0 {
			queue = append(queue, svc)
		}
	}

	errors := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, dep := range cur.Record().Dependencies() {
			dt := dep.DepType
			if dt == service.DepBefore || dt == service.DepAfter {
				continue
			}
			depName := dep.To.Name()
			newDepth := depth[cur.Name()] + 1
			if newDepth > depth[depName] {
				depth[depName] = newDepth
			}
			inDegree[depName]--
			if inDegree[depName] == 0 {
				if newDepth > config.MaxDepDepth {
					fmt.Fprintf(os.Stderr, "  ERROR: service '%s' exceeds maximum dependency depth (%d)\n",
						depName, config.MaxDepDepth)
					errors++
				}
				queue = append(queue, svcMap[depName])
			}
		}
	}

	return errors
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "slinit-check: "+format+"\n", args...)
	os.Exit(2)
}
