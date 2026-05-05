package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/platform"
	"github.com/sunlightlinux/slinit/pkg/process"
	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

// Default init.d directories to search as fallback.
var DefaultInitDDirs = []string{"/etc/init.d", "/etc/rc.d"}

// DirLoader loads service descriptions from one or more directories.
type DirLoader struct {
	dirs        []string
	initDirs    []string // init.d directories for fallback (empty = disabled)
	overlayDirs []string // conf.d overlay directories (default: /etc/slinit.conf.d)
	set         *service.ServiceSet
	loading     map[string]bool // tracks loading state for circular dependency detection
	curDepth    int             // current recursion depth during loading
	platformSys platform.Type   // detected (or overridden) platform for keyword filtering
}

// defaultOverlayDir is the default conf.d overlay location.
const defaultOverlayDir = "/etc/slinit.conf.d"

// NewDirLoader creates a new directory-based service loader.
func NewDirLoader(set *service.ServiceSet, dirs []string) *DirLoader {
	return &DirLoader{
		dirs:        dirs,
		set:         set,
		loading:     make(map[string]bool),
		overlayDirs: []string{defaultOverlayDir},
	}
}

// SetPlatform sets the detected (or manually overridden) platform type.
// Services with matching keyword directives will be skipped during loading.
func (dl *DirLoader) SetPlatform(p platform.Type) {
	dl.platformSys = p
}

// Platform returns the configured platform type.
func (dl *DirLoader) Platform() platform.Type {
	return dl.platformSys
}

// SetInitDDirs configures init.d fallback directories.
// When set, the loader will search these directories for init.d scripts
// if a service is not found in the normal service directories.
func (dl *DirLoader) SetInitDDirs(dirs []string) {
	dl.initDirs = dirs
}

// SetOverlayDirs configures the conf.d overlay directories. Passing nil or
// an empty slice disables overlay discovery entirely.
func (dl *DirLoader) SetOverlayDirs(dirs []string) {
	dl.overlayDirs = dirs
}

// OverlayDirs returns the configured overlay directories.
func (dl *DirLoader) OverlayDirs() []string {
	return dl.overlayDirs
}

// ServiceDirs returns the configured service directories.
func (dl *DirLoader) ServiceDirs() []string {
	return dl.dirs
}

// LoadService loads a service and its dependencies by name.
func (dl *DirLoader) LoadService(name string) (service.Service, error) {
	// Check if already loaded
	if svc := dl.set.FindService(name, false); svc != nil {
		return svc, nil
	}

	return dl.loadServiceImpl(name, dl.curDepth)
}

// ReloadService reloads a service description from file.
// For stopped services: full replacement possible (including type change).
// For started services: in-place update with restrictions.
// For starting/stopping services: reload refused.
func (dl *DirLoader) ReloadService(svc service.Service) (service.Service, error) {
	name := svc.Name()

	// Re-parse the config file
	desc, filePath, err := dl.findAndParse(name)
	if err != nil {
		return nil, err
	}

	state := svc.State()
	switch state {
	case service.StateStopped:
		return dl.reloadStopped(svc, desc, filePath)
	case service.StateStarted:
		return dl.reloadStarted(svc, desc, filePath)
	default:
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     fmt.Sprintf("cannot reload service in state %d", state),
		}
	}
}

// reloadStopped handles reload of a stopped service. Can change type.
func (dl *DirLoader) reloadStopped(svc service.Service, desc *ServiceDescription, filePath string) (service.Service, error) {
	typeChanged := desc.Type != svc.Type()

	if typeChanged {
		// Create new service record of the new type
		newSvc := dl.createService(svc.Name(), desc)

		// Load dependencies for the new service
		dl.loading[svc.Name()] = true
		defer delete(dl.loading, svc.Name())

		if err := dl.loadDependencies(newSvc, desc, filePath); err != nil {
			return nil, err
		}

		// Apply common settings
		applyToService(newSvc, desc)

		// Transfer pipe fds and consumer links from old to new
		dl.transferConsumerOf(svc, newSvc)

		// Transfer dependents from old to new
		dl.transferDependents(svc, newSvc)

		// Remove old deps
		dl.removeDependencies(svc)

		// Set up consumer-of for new service
		if desc.ConsumerOf != "" {
			if err := dl.setupConsumerOf(newSvc, desc); err != nil {
				return nil, err
			}
		}

		// Replace in set
		dl.set.ReplaceService(svc, newSvc)

		return newSvc, nil
	}

	return dl.updateInPlace(svc, desc, filePath)
}

// reloadStarted handles reload of a running service. Restricted changes only.
func (dl *DirLoader) reloadStarted(svc service.Service, desc *ServiceDescription, filePath string) (service.Service, error) {
	name := svc.Name()

	// Cannot change type
	if desc.Type != svc.Type() {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "cannot change type of running service",
		}
	}

	// Cannot change console flags
	oldFlags := svc.Record().Flags
	if oldFlags.StartsOnConsole != desc.Flags.StartsOnConsole ||
		oldFlags.SharesConsole != desc.Flags.SharesConsole {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "cannot change console flags for running service",
		}
	}

	// Cannot change log type
	if err := dl.validateLogTypeUnchanged(svc, desc); err != nil {
		return nil, err
	}

	// Cannot change pid-file for BGProcess
	if err := dl.validatePidFileUnchanged(svc, desc); err != nil {
		return nil, err
	}

	// New regular deps must already be STARTED
	if err := dl.validateNewRegularDeps(svc, desc); err != nil {
		return nil, err
	}

	return dl.updateInPlace(svc, desc, filePath)
}

// updateInPlace updates a service's configuration without replacing the record.
// Dependencies are updated first (may fail), then type-specific fields and
// common settings (cannot fail), so a dependency error does not leave the
// service in a partially-updated state.
func (dl *DirLoader) updateInPlace(svc service.Service, desc *ServiceDescription, filePath string) (service.Service, error) {
	// Check for cycles before modifying
	if err := dl.checkCycle(svc, desc); err != nil {
		return nil, err
	}

	// Update dependencies first — this can fail (e.g. missing dep) and has rollback
	if err := dl.updateDependencies(svc, desc, filePath); err != nil {
		return nil, err
	}

	// Update type-specific fields (command, timeouts, etc.)
	dl.updateTypeSpecificFields(svc, desc)

	// Update common settings
	applyToService(svc, desc)

	// Update consumer-of relationship
	if desc.ConsumerOf != "" && svc.Record().ConsumerFor() == nil {
		if err := dl.setupConsumerOf(svc, desc); err != nil {
			return nil, err
		}
	}

	// Update shared-logger relationship
	if desc.SharedLogger != "" && svc.Record().SharedLoggerName() == "" {
		if err := dl.setupSharedLogger(svc, desc); err != nil {
			return nil, err
		}
	}

	return svc, nil
}

// updateTypeSpecificFields applies type-specific setters from the description.
func (dl *DirLoader) updateTypeSpecificFields(svc service.Service, desc *ServiceDescription) {
	switch s := svc.(type) {
	case *service.ProcessService:
		s.SetCommand(desc.Command)
		s.SetStopCommand(desc.StopCommand)
		s.SetFinishCommand(desc.FinishCommand)
		s.SetReadyCheckCommand(desc.ReadyCheckCommand, desc.ReadyCheckInterval)
		s.SetPreStopHook(desc.PreStopHook)
		s.SetControlCommands(desc.ControlCommands)
		s.SetWorkingDir(desc.WorkingDir)
		s.SetEnvFile(desc.EnvFile)
		s.SetEnvDir(desc.EnvDir)
		s.SetChroot(desc.Chroot)
		s.SetLockFile(desc.LockFile)
		s.SetNewSession(desc.NewSession)
		s.SetCloseFDs(desc.CloseStdin, desc.CloseStdout, desc.CloseStderr)
		if desc.StartTimeout > 0 {
			s.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			s.SetStopTimeout(desc.StopTimeout)
		}
		if desc.RestartDelay > 0 {
			s.SetRestartDelay(desc.RestartDelay)
		}
		if desc.RestartDelayStep > 0 || desc.RestartDelayCap > 0 {
			s.SetRestartBackoff(desc.RestartDelayStep, desc.RestartDelayCap)
		}
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			s.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		applyLogSettings(s, desc)
		s.SetLogRotation(desc.LogMaxSize, desc.LogMaxFiles, desc.LogRotateTime)
		s.SetLogProcessor(desc.LogProcessor)
		s.SetLogFilters(desc.LogInclude, desc.LogExclude)
		if len(desc.OutputLogger) > 0 {
			s.SetOutputLogger(desc.OutputLogger)
		}
		if len(desc.ErrorLogger) > 0 {
			s.SetErrorLogger(desc.ErrorLogger)
		}
		s.SetReadyNotification(desc.ReadyNotifyFD, desc.ReadyNotifyVar)
		if desc.WatchdogTimeout > 0 {
			s.SetWatchdogTimeout(desc.WatchdogTimeout)
		}
		if len(desc.CronCommand) > 0 {
			s.SetCronConfig(desc.CronCommand, desc.CronInterval, desc.CronDelay, desc.CronOnError)
		}
		if len(desc.HealthCheckCommand) > 0 {
			s.SetHealthCheck(desc.HealthCheckCommand, desc.HealthCheckInterval,
				desc.HealthCheckDelay, desc.HealthCheckMaxFail, desc.UnhealthyCommand)
		}
		if desc.SocketActivation == "on-demand" {
			s.SetSocketOnDemand(true)
		}
		if desc.VTTYEnabled {
			s.SetVTTY(true, desc.VTTYScrollback, "/run/slinit")
		}
	case *service.ScriptedService:
		s.SetStartCommand(desc.Command)
		s.SetStopCommand(desc.StopCommand)
		s.SetWorkingDir(desc.WorkingDir)
		if desc.StartTimeout > 0 {
			s.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			s.SetStopTimeout(desc.StopTimeout)
		}
		applyLogSettings(s, desc)
	case *service.BGProcessService:
		s.SetCommand(desc.Command)
		s.SetStopCommand(desc.StopCommand)
		s.SetWorkingDir(desc.WorkingDir)
		s.SetEnvFile(desc.EnvFile)
		s.SetPIDFile(desc.PIDFile)
		if desc.StartTimeout > 0 {
			s.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			s.SetStopTimeout(desc.StopTimeout)
		}
		if desc.RestartDelay > 0 {
			s.SetRestartDelay(desc.RestartDelay)
		}
		if desc.RestartDelayStep > 0 || desc.RestartDelayCap > 0 {
			s.SetRestartBackoff(desc.RestartDelayStep, desc.RestartDelayCap)
		}
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			s.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		applyLogSettings(s, desc)
	}
}

// updateDependencies atomically replaces dependencies on a service.
func (dl *DirLoader) updateDependencies(svc service.Service, desc *ServiceDescription, filePath string) error {
	rec := svc.Record()

	// Save old deps for rollback
	oldDeps := make([]*service.ServiceDep, len(rec.Dependencies()))
	copy(oldDeps, rec.Dependencies())

	// Remove all deps except BEFORE deps from other services
	for i := len(rec.Dependencies()) - 1; i >= 0; i-- {
		dep := rec.Dependencies()[i]
		if dep.DepType != service.DepBefore {
			rec.RmDep(dep.To, dep.DepType)
		}
	}

	// Load and add new deps
	if err := dl.loadDependencies(svc, desc, filePath); err != nil {
		// Rollback: re-add old deps
		for _, dep := range oldDeps {
			if dep.DepType != service.DepBefore {
				rec.AddDep(dep.To, dep.DepType)
			}
		}
		return err
	}

	// Recalculate dependency depth after dep changes
	var updater service.DepDepthUpdater
	updater.AddPotentialUpdate(svc)
	if err := updater.ProcessUpdates(); err != nil {
		// Rollback deps on depth overflow
		for i := len(rec.Dependencies()) - 1; i >= 0; i-- {
			dep := rec.Dependencies()[i]
			if dep.DepType != service.DepBefore {
				rec.RmDep(dep.To, dep.DepType)
			}
		}
		for _, dep := range oldDeps {
			if dep.DepType != service.DepBefore {
				rec.AddDep(dep.To, dep.DepType)
			}
		}
		updater.Rollback()
		return &ServiceLoadError{ServiceName: svc.Name(), Message: err.Error()}
	}
	updater.Commit()

	return nil
}

// transferConsumerOf transfers pipe fds and consumer-of links from old to new service.
func (dl *DirLoader) transferConsumerOf(oldSvc, newSvc service.Service) {
	oldRec := oldSvc.Record()

	// Transfer pipe file descriptors
	r, w := oldRec.TransferOutputPipe()
	if r != nil || w != nil {
		newSvc.Record().SetOutputPipeFDs(r, w)
	}

	// Transfer consumer link (we are a producer)
	if consumer := oldRec.LogConsumer(); consumer != nil {
		oldRec.SetLogConsumer(nil)
		newSvc.Record().SetLogConsumer(consumer)
		consumer.Record().SetConsumerFor(newSvc)
	}

	// Transfer producer link (we are a consumer)
	if producer := oldRec.ConsumerFor(); producer != nil {
		oldRec.SetConsumerFor(nil)
		newSvc.Record().SetConsumerFor(producer)
		producer.Record().SetLogConsumer(newSvc)
	}
}

// transferDependents moves dependents from old service to new service.
func (dl *DirLoader) transferDependents(oldSvc, newSvc service.Service) {
	oldRec := oldSvc.Record()
	for _, dept := range oldRec.Dependents() {
		dept.To = newSvc
	}
	newSvc.Record().SetDependents(oldRec.Dependents())
	oldRec.SetDependents(nil)
}

// removeDependencies clears all dependencies from a service.
func (dl *DirLoader) removeDependencies(svc service.Service) {
	rec := svc.Record()
	for len(rec.Dependencies()) > 0 {
		dep := rec.Dependencies()[0]
		rec.RmDep(dep.To, dep.DepType)
	}
}

// checkCycle detects if adding the described dependencies would create a cycle.
func (dl *DirLoader) checkCycle(svc service.Service, desc *ServiceDescription) error {
	// Collect all named deps from the description
	allDepNames := make([]string, 0)
	allDepNames = append(allDepNames, desc.DependsOn...)
	allDepNames = append(allDepNames, desc.DependsMS...)
	allDepNames = append(allDepNames, desc.WaitsFor...)
	allDepNames = append(allDepNames, desc.After...)

	// BFS: check if any transitive dependency leads back to svc
	visited := map[string]bool{}
	queue := make([]string, len(allDepNames))
	copy(queue, allDepNames)

	for len(queue) > 0 {
		depName := queue[0]
		queue = queue[1:]

		if depName == svc.Name() {
			return &ServiceLoadError{
				ServiceName: svc.Name(),
				Message:     "cyclic dependency detected during reload",
			}
		}

		if visited[depName] {
			continue
		}
		visited[depName] = true

		depSvc := dl.set.FindService(depName, false)
		if depSvc != nil {
			for _, dep := range depSvc.Record().Dependencies() {
				queue = append(queue, dep.To.Name())
			}
		}
	}

	return nil
}

// validateLogTypeUnchanged checks that log type is not changed for a running service.
func (dl *DirLoader) validateLogTypeUnchanged(svc service.Service, desc *ServiceDescription) error {
	switch s := svc.(type) {
	case *service.ProcessService:
		if s.GetLogType() != desc.LogType {
			return &ServiceLoadError{ServiceName: svc.Name(), Message: "cannot change log-type for running service"}
		}
	case *service.ScriptedService:
		if s.GetLogType() != desc.LogType {
			return &ServiceLoadError{ServiceName: svc.Name(), Message: "cannot change log-type for running service"}
		}
	case *service.BGProcessService:
		if s.GetLogType() != desc.LogType {
			return &ServiceLoadError{ServiceName: svc.Name(), Message: "cannot change log-type for running service"}
		}
	}
	return nil
}

// validatePidFileUnchanged checks that pid-file is not changed for a running BGProcess.
func (dl *DirLoader) validatePidFileUnchanged(svc service.Service, desc *ServiceDescription) error {
	if bgp, ok := svc.(*service.BGProcessService); ok {
		if bgp.GetPIDFile() != desc.PIDFile {
			return &ServiceLoadError{ServiceName: svc.Name(), Message: "cannot change pid-file for running service"}
		}
	}
	return nil
}

// validateNewRegularDeps checks that new regular deps are already STARTED.
func (dl *DirLoader) validateNewRegularDeps(svc service.Service, desc *ServiceDescription) error {
	// Build set of current regular deps
	currentDeps := map[string]bool{}
	for _, dep := range svc.Record().Dependencies() {
		if dep.DepType == service.DepRegular {
			currentDeps[dep.To.Name()] = true
		}
	}

	// Check new regular deps that don't already exist
	for _, depName := range desc.DependsOn {
		if currentDeps[depName] {
			continue
		}
		depSvc := dl.set.FindService(depName, false)
		if depSvc == nil || depSvc.State() != service.StateStarted {
			return &ServiceLoadError{
				ServiceName: svc.Name(),
				Message:     fmt.Sprintf("cannot add non-started dependency '%s' to running service", depName),
			}
		}
	}

	return nil
}

func (dl *DirLoader) loadServiceImpl(name string, depth int) (service.Service, error) {
	// Validate service name
	if err := ValidateServiceName(name); err != nil {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     err.Error(),
		}
	}

	// Check dependency depth limit
	if depth >= MaxDepDepth {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     fmt.Sprintf("dependency depth exceeds maximum (%d)", MaxDepDepth),
		}
	}

	// Check for circular dependency
	if dl.loading[name] {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "circular dependency detected",
		}
	}
	dl.loading[name] = true
	defer delete(dl.loading, name)

	// Set depth for nested LoadService calls via loadDependencies
	prevDepth := dl.curDepth
	dl.curDepth = depth + 1
	defer func() { dl.curDepth = prevDepth }()

	// Find and parse the service description file
	desc, filePath, err := dl.findAndParse(name)
	if err != nil {
		return nil, err
	}

	// Platform keyword filtering: skip services that declare keywords
	// matching the detected platform (e.g. "keyword -docker -lxc")
	if dl.platformSys != platform.None && platform.ShouldSkip(desc.Keywords, dl.platformSys) {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     fmt.Sprintf("service disabled on platform %q (keyword match)", dl.platformSys),
		}
	}

	// Validate: ready-notification not supported for bgprocess
	if desc.Type == service.TypeBGProcess && (desc.ReadyNotifyFD >= 0 || desc.ReadyNotifyVar != "") {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "ready-notification is not supported for bgprocess services",
		}
	}

	// Validate: watchdog-timeout piggybacks on the ready-notification pipe.
	// Without one configured the service has no channel through which to
	// send keepalives, so the watchdog would fire as soon as it armed.
	if desc.WatchdogTimeout > 0 && desc.ReadyNotifyFD < 0 && desc.ReadyNotifyVar == "" {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "watchdog-timeout requires ready-notification to be set " +
				"(the service uses the same pipe to send keepalives)",
		}
	}
	if desc.WatchdogTimeout > 0 && desc.Type != service.TypeProcess {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "watchdog-timeout is only supported for type=process services",
		}
	}

	// Validate: scheduling-policy cross-fields
	if desc.SchedPolicySet {
		switch desc.SchedPolicy {
		case unix.SCHED_FIFO, unix.SCHED_RR:
			if desc.SchedPriority == 0 {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "sched-priority is required for SCHED_FIFO / SCHED_RR (1..99)",
				}
			}
		case unix.SCHED_DEADLINE:
			if desc.SchedRuntime == 0 || desc.SchedDeadline == 0 || desc.SchedPeriod == 0 {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "SCHED_DEADLINE requires sched-runtime, sched-deadline AND sched-period",
				}
			}
			if desc.SchedRuntime > desc.SchedDeadline || desc.SchedDeadline > desc.SchedPeriod {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "SCHED_DEADLINE invariant: runtime ≤ deadline ≤ period",
				}
			}
			if desc.SchedPriority != 0 {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "sched-priority has no meaning under SCHED_DEADLINE — drop it or switch to sched-policy=fifo|rr",
				}
			}
		default:
			if desc.SchedPriority != 0 {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "sched-priority is only meaningful with sched-policy=fifo or rr",
				}
			}
			if desc.SchedRuntime != 0 || desc.SchedDeadline != 0 || desc.SchedPeriod != 0 {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "sched-runtime / sched-deadline / sched-period are only meaningful with sched-policy=deadline",
				}
			}
		}
	} else if desc.SchedPriority != 0 || desc.SchedRuntime != 0 ||
		desc.SchedDeadline != 0 || desc.SchedPeriod != 0 {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "sched-priority / sched-runtime / sched-deadline / sched-period set without sched-policy",
		}
	}

	// Validate: NUMA policy + nodes cross-fields
	if desc.NumaMempolicySet {
		switch desc.NumaMempolicy {
		case unix.MPOL_BIND, unix.MPOL_INTERLEAVE, unix.MPOL_PREFERRED:
			if len(desc.NumaNodes) == 0 {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "numa-mempolicy=bind|interleave|preferred requires numa-nodes",
				}
			}
		case unix.MPOL_DEFAULT, unix.MPOL_LOCAL:
			if len(desc.NumaNodes) > 0 {
				return nil, &ServiceLoadError{
					ServiceName: name,
					Message:     "numa-mempolicy=default|local does not accept numa-nodes",
				}
			}
		}
	} else if len(desc.NumaNodes) > 0 {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "numa-nodes set without numa-mempolicy",
		}
	}

	// Create the service based on type
	svc := dl.createService(name, desc)

	// Record the directory and modification time of the service description
	svc.Record().SetServiceDir(filepath.Dir(filePath))
	if fi, err := os.Stat(filePath); err == nil {
		svc.Record().SetLoadModTime(fi.ModTime())
	}

	// Add to set before loading dependencies (allows circular detection)
	dl.set.AddService(svc)

	// Load and connect dependencies
	if err := dl.loadDependencies(svc, desc, filePath); err != nil {
		dl.set.RemoveService(svc)
		return nil, err
	}

	// Calculate dependency depth
	svc.Record().SetDepDepth(calcServiceDepth(svc))

	// Apply settings to the service record
	applyToService(svc, desc)

	// Check for 'down' marker file (runit-inspired: service starts in stopped state).
	// Uses <service-name>.down in the same directory as the service file.
	// If the file exists, the service must be explicitly started via slinitctl.
	downPath := filepath.Join(filepath.Dir(filePath), name+".down")
	if _, err := os.Stat(downPath); err == nil {
		svc.Record().SetMarkedDown(true)
	}

	// Re-register alias now that provides is set (AddService was called
	// before applyToService, so the alias wasn't registered yet)
	if alias := svc.Record().Provides(); alias != "" {
		dl.set.RegisterAlias(alias, svc)
	}

	// Apply load-options
	applyLoadOptions(svc, desc)

	// Set up consumer-of relationship
	if desc.ConsumerOf != "" {
		if err := dl.setupConsumerOf(svc, desc); err != nil {
			dl.set.RemoveService(svc)
			return nil, err
		}
	}

	// Set up shared-logger relationship
	if desc.SharedLogger != "" {
		if err := dl.setupSharedLogger(svc, desc); err != nil {
			dl.set.RemoveService(svc)
			return nil, err
		}
	}

	return svc, nil
}

func (dl *DirLoader) findAndParse(name string) (*ServiceDescription, string, error) {
	// Extract service argument from name@argument pattern
	baseName := name
	var serviceArg *string
	if idx := strings.IndexByte(name, '@'); idx >= 0 {
		baseName = name[:idx]
		arg := name[idx+1:]
		serviceArg = &arg
	}

	// Try full name first, then base name (for templates)
	searchNames := []string{name}
	if baseName != name {
		searchNames = append(searchNames, baseName)
	}

	for _, dir := range dl.dirs {
		for _, sn := range searchNames {
			path := filepath.Join(dir, sn)
			f, err := os.Open(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, "", &ServiceLoadError{
					ServiceName: name,
					Message:     fmt.Sprintf("error reading %s: %v", path, err),
				}
			}

			var desc *ServiceDescription
			if serviceArg != nil {
				desc, err = ParseWithArg(f, name, path, *serviceArg)
			} else {
				desc, err = Parse(f, name, path)
			}
			f.Close()
			if err != nil {
				return nil, "", err
			}

			// Apply conf.d overlays (if any) on top of the primary description.
			if err := dl.applyOverlays(desc, name, baseName, serviceArg); err != nil {
				return nil, "", err
			}
			return desc, path, nil
		}
	}

	// Fallback: search init.d directories for SysV init scripts
	if len(dl.initDirs) > 0 {
		for _, dir := range dl.initDirs {
			path := filepath.Join(dir, name)
			if IsInitDScript(path) {
				desc, err := InitDToServiceDescription(path)
				if err != nil {
					return nil, "", &ServiceLoadError{
						ServiceName: name,
						Message:     fmt.Sprintf("init.d script '%s': %v", path, err),
					}
				}
				return desc, path, nil
			}
		}
	}

	return nil, "", &ServiceLoadError{
		ServiceName: name,
		Message:     "service description not found",
	}
}

// applyOverlays merges every matching overlay file from overlayDirs into desc.
// For each configured overlay directory, it tries <dir>/<name> first, then
// <dir>/<baseName> (template fallback). Missing files are silently ignored.
// A parse error in any overlay is fatal (returned wrapped in ServiceLoadError).
func (dl *DirLoader) applyOverlays(desc *ServiceDescription, name, baseName string, serviceArg *string) error {
	if len(dl.overlayDirs) == 0 {
		return nil
	}

	// Search order: full name first, then base name for templates.
	candidates := []string{name}
	if baseName != "" && baseName != name {
		candidates = append(candidates, baseName)
	}

	// Deduplicate overlay files across (dir, candidate) pairs so a file is
	// applied at most once even if the same dir matches multiple candidates.
	applied := make(map[string]bool)

	for _, dir := range dl.overlayDirs {
		if dir == "" {
			continue
		}
		for _, cand := range candidates {
			path := filepath.Join(dir, cand)
			if applied[path] {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return &ServiceLoadError{
					ServiceName: name,
					Message:     fmt.Sprintf("error reading overlay %s: %v", path, err),
				}
			}
			parseErr := ParseOverlay(f, name, path, desc, serviceArg)
			f.Close()
			if parseErr != nil {
				return parseErr
			}
			applied[path] = true
		}
	}
	return nil
}

func (dl *DirLoader) createService(name string, desc *ServiceDescription) service.Service {
	switch desc.Type {
	case service.TypeInternal:
		return service.NewInternalService(dl.set, name)
	case service.TypeProcess:
		svc := service.NewProcessService(dl.set, name)
		svc.SetCommand(desc.Command)
		svc.SetStopCommand(desc.StopCommand)
		svc.SetFinishCommand(desc.FinishCommand)
		svc.SetReadyCheckCommand(desc.ReadyCheckCommand, desc.ReadyCheckInterval)
		svc.SetPreStopHook(desc.PreStopHook)
		svc.SetControlCommands(desc.ControlCommands)
		svc.SetEnvDir(desc.EnvDir)
		svc.SetWorkingDir(desc.WorkingDir)
		svc.SetEnvFile(desc.EnvFile)
		svc.SetChroot(desc.Chroot)
		svc.SetLockFile(desc.LockFile)
		svc.SetNewSession(desc.NewSession)
		svc.SetCloseFDs(desc.CloseStdin, desc.CloseStdout, desc.CloseStderr)
		if desc.StartTimeout > 0 {
			svc.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			svc.SetStopTimeout(desc.StopTimeout)
		}
		if desc.RestartDelay > 0 {
			svc.SetRestartDelay(desc.RestartDelay)
		}
		if desc.RestartDelayStep > 0 || desc.RestartDelayCap > 0 {
			svc.SetRestartBackoff(desc.RestartDelayStep, desc.RestartDelayCap)
		}
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			svc.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		applyLogSettings(svc, desc)
		svc.SetLogRotation(desc.LogMaxSize, desc.LogMaxFiles, desc.LogRotateTime)
		svc.SetLogProcessor(desc.LogProcessor)
		svc.SetLogFilters(desc.LogInclude, desc.LogExclude)
		if len(desc.OutputLogger) > 0 {
			svc.SetOutputLogger(desc.OutputLogger)
		}
		if len(desc.ErrorLogger) > 0 {
			svc.SetErrorLogger(desc.ErrorLogger)
		}
		if desc.ReadyNotifyFD >= 0 || desc.ReadyNotifyVar != "" {
			svc.SetReadyNotification(desc.ReadyNotifyFD, desc.ReadyNotifyVar)
		}
		if desc.WatchdogTimeout > 0 {
			svc.SetWatchdogTimeout(desc.WatchdogTimeout)
		}
		if len(desc.CronCommand) > 0 {
			svc.SetCronConfig(desc.CronCommand, desc.CronInterval, desc.CronDelay, desc.CronOnError)
		}
		if len(desc.HealthCheckCommand) > 0 {
			svc.SetHealthCheck(desc.HealthCheckCommand, desc.HealthCheckInterval,
				desc.HealthCheckDelay, desc.HealthCheckMaxFail, desc.UnhealthyCommand)
		}
		if desc.SocketActivation == "on-demand" {
			svc.SetSocketOnDemand(true)
		}
		if desc.VTTYEnabled {
			svc.SetVTTY(true, desc.VTTYScrollback, "/run/slinit")
		}
		return svc
	case service.TypeScripted:
		svc := service.NewScriptedService(dl.set, name)
		svc.SetStartCommand(desc.Command)
		svc.SetStopCommand(desc.StopCommand)
		svc.SetWorkingDir(desc.WorkingDir)
		if desc.StartTimeout > 0 {
			svc.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			svc.SetStopTimeout(desc.StopTimeout)
		}
		applyLogSettings(svc, desc)
		return svc
	case service.TypeBGProcess:
		svc := service.NewBGProcessService(dl.set, name)
		svc.SetCommand(desc.Command)
		svc.SetStopCommand(desc.StopCommand)
		svc.SetWorkingDir(desc.WorkingDir)
		svc.SetEnvFile(desc.EnvFile)
		svc.SetPIDFile(desc.PIDFile)
		if desc.StartTimeout > 0 {
			svc.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			svc.SetStopTimeout(desc.StopTimeout)
		}
		if desc.RestartDelay > 0 {
			svc.SetRestartDelay(desc.RestartDelay)
		}
		if desc.RestartDelayStep > 0 || desc.RestartDelayCap > 0 {
			svc.SetRestartBackoff(desc.RestartDelayStep, desc.RestartDelayCap)
		}
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			svc.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		applyLogSettings(svc, desc)
		return svc
	case service.TypeTriggered:
		return service.NewTriggeredService(dl.set, name)
	default:
		return service.NewInternalService(dl.set, name)
	}
}

func (dl *DirLoader) loadDependencies(svc service.Service, desc *ServiceDescription, filePath string) error {
	depSpecs := []struct {
		names   []string
		depType service.DependencyType
	}{
		{desc.DependsOn, service.DepRegular},
		{desc.DependsMS, service.DepMilestone},
		{desc.WaitsFor, service.DepWaitsFor},
		{desc.Before, service.DepBefore},
		{desc.After, service.DepAfter},
	}

	for _, spec := range depSpecs {
		for _, depName := range spec.names {
			depSvc, err := dl.LoadService(depName)
			if err != nil {
				return fmt.Errorf("loading dependency '%s' for service '%s': %w",
					depName, svc.Name(), err)
			}
			svc.Record().AddDep(depSvc, spec.depType)
		}
	}

	// Load dependencies from directories (e.g., waits-for.d)
	dirDepSpecs := []struct {
		dirs    []string
		depType service.DependencyType
	}{
		{desc.DependsOnD, service.DepRegular},
		{desc.DependsMSD, service.DepMilestone},
		{desc.WaitsForD, service.DepWaitsFor},
	}

	for _, spec := range dirDepSpecs {
		for _, dir := range spec.dirs {
			depDir := dir
			if !filepath.IsAbs(depDir) {
				depDir = filepath.Join(filepath.Dir(filePath), dir)
			}
			if err := dl.loadDepsFromDir(svc, depDir, spec.depType); err != nil {
				return err
			}
		}
	}

	return nil
}

func (dl *DirLoader) loadDepsFromDir(svc service.Service, dir string, depType service.DependencyType) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // directory doesn't exist, that's OK
		}
		return fmt.Errorf("reading dependency directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || entry.Name()[0] == '.' {
			continue
		}

		depName := entry.Name()
		depSvc, err := dl.LoadService(depName)
		if err != nil {
			return fmt.Errorf("loading dependency '%s' from directory '%s': %w",
				depName, dir, err)
		}
		svc.Record().AddDep(depSvc, depType)
	}

	return nil
}

// logSettable is implemented by process-based services that support log configuration.
type logSettable interface {
	SetLogType(service.LogType)
	SetLogBufMax(int)
	SetLogFileDetails(path string, perms, uid, gid int)
}

// applyLogSettings applies log type configuration to a process-based service.
func applyLogSettings(svc logSettable, desc *ServiceDescription) {
	switch desc.LogType {
	case service.LogToBuffer:
		svc.SetLogType(desc.LogType)
		svc.SetLogBufMax(desc.LogBufMax)
	case service.LogToPipe:
		svc.SetLogType(desc.LogType)
	case service.LogToFile:
		svc.SetLogType(desc.LogType)
		svc.SetLogFileDetails(desc.LogFile, desc.LogFilePerms, desc.LogFileUID, desc.LogFileGID)
	case service.LogToCommand:
		svc.SetLogType(desc.LogType)
	}
}

// applyToService applies parsed configuration to the service record.
func applyToService(svc service.Service, desc *ServiceDescription) {
	rec := svc.Record()
	rec.SetDescription(desc.Description)
	rec.SetRequiredPaths(desc.RequiredFiles, desc.RequiredDirs)
	if len(desc.ExtraCommands) > 0 {
		rec.SetExtraCommands(desc.ExtraCommands)
	}
	if len(desc.ExtraStartedCommands) > 0 {
		rec.SetExtraStartedCommands(desc.ExtraStartedCommands)
	}
	rec.SetAutoRestart(desc.AutoRestart)
	rec.SetSmoothRecovery(desc.SmoothRecovery)
	rec.SetManualStart(desc.ManualStart)
	rec.SetNormalExitCodes(desc.NormalExitCodes)
	rec.SetNormalExitSignals(desc.NormalExitSignals)
	rec.SetFlags(desc.Flags)
	rec.SetTermSignal(desc.TermSignal)
	if desc.ChainTo != "" {
		rec.SetChainTo(desc.ChainTo)
	}
	if desc.SocketPath != "" {
		rec.SetSocketDetails(desc.SocketPath, desc.SocketPerms, desc.SocketUID, desc.SocketGID)
		if len(desc.SocketPaths) > 0 {
			rec.SetSocketPaths(desc.SocketPaths)
		}
	}
	if desc.Provides != "" {
		rec.SetProvides(desc.Provides)
	}
	if desc.EnableVia != "" {
		rec.SetEnableVia(desc.EnableVia)
	}
	if desc.InittabID != "" || desc.InittabLine != "" {
		rec.SetUtmpDetails(desc.InittabID, desc.InittabLine)
	}

	// Process attributes
	if desc.Nice != nil {
		rec.SetNice(desc.Nice)
	}
	if desc.OOMScoreAdj != nil {
		rec.SetOOMScoreAdj(desc.OOMScoreAdj)
	}
	if desc.NoNewPrivs {
		rec.SetNoNewPrivs(true)
	}
	if desc.CgroupPath != "" {
		rec.SetCgroupPath(desc.CgroupPath)
	}
	if len(desc.CgroupSettings) > 0 {
		cgSettings := make([]process.CgroupSetting, len(desc.CgroupSettings))
		for i, cs := range desc.CgroupSettings {
			cgSettings[i] = process.CgroupSetting{File: cs.File, Value: cs.Value}
		}
		rec.SetCgroupSettings(cgSettings)
	}
	if desc.IOPrio != "" {
		class, level := parseIOPrio(desc.IOPrio)
		if class >= 0 {
			rec.SetIOPrio(class, level)
		}
	}

	// Resource limits
	applyRlimits(rec, desc)

	// Capabilities
	if desc.Capabilities != "" {
		caps, err := process.ParseCapabilities(desc.Capabilities)
		if err == nil && len(caps) > 0 {
			rec.SetAmbientCaps(caps)
		}
	}
	if desc.Securebits != "" {
		bits, err := process.ParseSecurebits(desc.Securebits)
		if err == nil && bits != 0 {
			rec.SetSecurebits(bits)
		}
	}
	if len(desc.CPUAffinity) > 0 {
		rec.SetCPUAffinity(desc.CPUAffinity)
	}

	// Real-time scheduling
	if desc.SchedPolicySet {
		rec.SetSchedPolicy(desc.SchedPolicy, true)
		rec.SetSchedPriority(desc.SchedPriority)
		rec.SetSchedDeadlineParams(desc.SchedRuntime, desc.SchedDeadline, desc.SchedPeriod)
		rec.SetSchedResetOnFork(desc.SchedResetOnFork)
	}

	// Memory locking + NUMA (applied via slinit-runner)
	if desc.MlockallFlags != 0 {
		rec.SetMlockallFlags(desc.MlockallFlags)
	}
	if desc.NumaMempolicySet {
		rec.SetNumaMempolicy(desc.NumaMempolicy, true)
		if len(desc.NumaNodes) > 0 {
			rec.SetNumaNodes(desc.NumaNodes)
		}
	}

	// Namespace isolation (Linux clone flags)
	var cloneflags uintptr
	if desc.NamespacePID {
		cloneflags |= syscall.CLONE_NEWPID
	}
	if desc.NamespaceMount {
		cloneflags |= syscall.CLONE_NEWNS
	}
	if desc.NamespaceNet {
		cloneflags |= syscall.CLONE_NEWNET
	}
	if desc.NamespaceUTS {
		cloneflags |= syscall.CLONE_NEWUTS
	}
	if desc.NamespaceIPC {
		cloneflags |= syscall.CLONE_NEWIPC
	}
	if desc.NamespaceUser {
		cloneflags |= syscall.CLONE_NEWUSER
	}
	if desc.NamespaceCgroup {
		cloneflags |= syscall.CLONE_NEWCGROUP
	}
	if cloneflags != 0 {
		rec.SetCloneflags(cloneflags)
	}

	// User namespace UID/GID mappings
	if len(desc.NamespaceUidMap) > 0 {
		maps := make([]syscall.SysProcIDMap, len(desc.NamespaceUidMap))
		for i, m := range desc.NamespaceUidMap {
			maps[i] = syscall.SysProcIDMap{
				ContainerID: m.ContainerID,
				HostID:      m.HostID,
				Size:        m.Size,
			}
		}
		rec.SetUidMappings(maps)
	}
	if len(desc.NamespaceGidMap) > 0 {
		maps := make([]syscall.SysProcIDMap, len(desc.NamespaceGidMap))
		for i, m := range desc.NamespaceGidMap {
			maps[i] = syscall.SysProcIDMap{
				ContainerID: m.ContainerID,
				HostID:      m.HostID,
				Size:        m.Size,
			}
		}
		rec.SetGidMappings(maps)
	}
}

// setupConsumerOf establishes the consumer-of relationship between services.
// The consumer's stdin is connected to the producer's stdout/stderr via a pipe.
func (dl *DirLoader) setupConsumerOf(consumer service.Service, desc *ServiceDescription) error {
	producerName := desc.ConsumerOf

	// Load the producer service
	producer, err := dl.LoadService(producerName)
	if err != nil {
		return &ServiceLoadError{
			ServiceName: consumer.Name(),
			Message:     fmt.Sprintf("consumer-of: failed to load producer '%s': %v", producerName, err),
		}
	}

	// Validate: producer must be process, bgprocess, or scripted
	switch producer.Type() {
	case service.TypeProcess, service.TypeBGProcess, service.TypeScripted:
		// OK
	default:
		return &ServiceLoadError{
			ServiceName: consumer.Name(),
			Message:     fmt.Sprintf("consumer-of: producer '%s' must be process, bgprocess, or scripted", producerName),
		}
	}

	// Validate: producer must have log-type = pipe
	if producer.GetLogType() != service.LogToPipe {
		return &ServiceLoadError{
			ServiceName: consumer.Name(),
			Message:     fmt.Sprintf("consumer-of: producer '%s' must have log-type = pipe", producerName),
		}
	}

	// Validate: producer must not already have a consumer
	if existing := producer.Record().LogConsumer(); existing != nil {
		return &ServiceLoadError{
			ServiceName: consumer.Name(),
			Message:     fmt.Sprintf("consumer-of: producer '%s' already has consumer '%s'", producerName, existing.Name()),
		}
	}

	// Validate: consumer must be process, bgprocess, or scripted
	switch consumer.Type() {
	case service.TypeProcess, service.TypeBGProcess, service.TypeScripted:
		// OK
	default:
		return &ServiceLoadError{
			ServiceName: consumer.Name(),
			Message:     "consumer-of: consumer must be process, bgprocess, or scripted",
		}
	}

	// Establish bidirectional links
	producer.Record().SetLogConsumer(consumer)
	consumer.Record().SetConsumerFor(producer)

	return nil
}

// setupSharedLogger establishes the shared-logger relationship.
// The producer's output is multiplexed through a SharedLogMux into the logger's stdin.
func (dl *DirLoader) setupSharedLogger(producer service.Service, desc *ServiceDescription) error {
	loggerName := desc.SharedLogger

	// Load the logger service (ensures it exists)
	logger, err := dl.LoadService(loggerName)
	if err != nil {
		return &ServiceLoadError{
			ServiceName: producer.Name(),
			Message:     fmt.Sprintf("shared-logger: failed to load logger '%s': %v", loggerName, err),
		}
	}

	// Logger must be a process-type service
	switch logger.Type() {
	case service.TypeProcess, service.TypeBGProcess:
		// OK
	default:
		return &ServiceLoadError{
			ServiceName: producer.Name(),
			Message:     fmt.Sprintf("shared-logger: logger '%s' must be process or bgprocess", loggerName),
		}
	}

	// Producer must be process, bgprocess, or scripted
	switch producer.Type() {
	case service.TypeProcess, service.TypeBGProcess, service.TypeScripted:
		// OK
	default:
		return &ServiceLoadError{
			ServiceName: producer.Name(),
			Message:     "shared-logger: producer must be process, bgprocess, or scripted",
		}
	}

	// Get or create the mux for this logger
	if _, err := dl.set.GetOrCreateSharedLogMux(loggerName); err != nil {
		return &ServiceLoadError{
			ServiceName: producer.Name(),
			Message:     fmt.Sprintf("shared-logger: failed to create mux for '%s': %v", loggerName, err),
		}
	}

	// Store the logger name on the producer
	producer.Record().SetSharedLoggerName(loggerName)

	return nil
}

// applyLoadOptions processes load-options flags (export-passwd-vars, export-service-name).
func applyLoadOptions(svc service.Service, desc *ServiceDescription) {
	rec := svc.Record()

	if desc.ExportServiceName {
		rec.SetEnvVar("DINIT_SERVICENAME", svc.Name())
		if rec.ServiceDir() != "" {
			rec.SetEnvVar("DINIT_SERVICEDSCDIR", rec.ServiceDir())
		}
	}

	if desc.ExportPasswdVars {
		var u *user.User
		var err error
		if desc.RunAs != "" {
			// Try as username first, then as UID
			u, err = user.Lookup(desc.RunAs)
			if err != nil {
				u, err = user.LookupId(desc.RunAs)
			}
		} else {
			u, err = user.LookupId(fmt.Sprintf("%d", os.Getuid()))
		}
		if err == nil {
			rec.SetEnvVar("USER", u.Username)
			rec.SetEnvVar("LOGNAME", u.Username)
			rec.SetEnvVar("HOME", u.HomeDir)
			rec.SetEnvVar("UID", u.Uid)
			rec.SetEnvVar("GID", u.Gid)
			// Shell: os/user doesn't expose shell, read from /etc/passwd
			if shell := lookupShell(u.Uid); shell != "" {
				rec.SetEnvVar("SHELL", shell)
			}
		}
	}
}

// passwdShellCache caches UID→shell mappings from /etc/passwd.
// Populated once on first lookupShell call.
var (
	passwdShellOnce  sync.Once
	passwdShellCache map[string]string // uid string → shell path
)

// lookupShell finds the shell for a given UID string, caching /etc/passwd on first call.
func lookupShell(uid string) string {
	passwdShellOnce.Do(func() {
		passwdShellCache = make(map[string]string)
		data, err := os.ReadFile("/etc/passwd")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Split(line, ":")
			if len(fields) >= 7 {
				passwdShellCache[fields[2]] = fields[6]
			}
		}
	})
	return passwdShellCache[uid]
}

// ServiceLoadError represents a service loading failure.
type ServiceLoadError struct {
	ServiceName string
	Message     string
}

func (e *ServiceLoadError) Error() string {
	return fmt.Sprintf("service '%s': %s", e.ServiceName, e.Message)
}

// parseIOPrio parses an ioprio string "class:level" or just "class".
// Returns (class, level). class is -1 on error.
// Classes: "realtime"/"rt"=1, "best-effort"/"be"=2, "idle"=3.
func parseIOPrio(s string) (int, int) {
	parts := strings.SplitN(s, ":", 2)
	className := strings.TrimSpace(parts[0])

	var class int
	switch strings.ToLower(className) {
	case "realtime", "rt":
		class = 1
	case "best-effort", "be":
		class = 2
	case "idle":
		class = 3
	default:
		// Try numeric
		n, err := strconv.Atoi(className)
		if err != nil || n < 0 || n > 3 {
			return -1, 0
		}
		class = n
	}

	level := 0
	if len(parts) == 2 {
		n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err == nil && n >= 0 && n <= 7 {
			level = n
		}
	}

	return class, level
}

// rlimit resource constants from syscall.
const (
	rlimitNofile = syscall.RLIMIT_NOFILE // 7
	rlimitCore   = syscall.RLIMIT_CORE   // 4
	rlimitData   = syscall.RLIMIT_DATA   // 2
	rlimitAs     = syscall.RLIMIT_AS     // 9
)

// applyRlimits adds parsed resource limits to the service record.
func applyRlimits(rec *service.ServiceRecord, desc *ServiceDescription) {
	if desc.RlimitNofile != nil {
		rec.AddRlimit(process.Rlimit{Resource: rlimitNofile, Soft: desc.RlimitNofile[0], Hard: desc.RlimitNofile[1]})
	}
	if desc.RlimitCore != nil {
		rec.AddRlimit(process.Rlimit{Resource: rlimitCore, Soft: desc.RlimitCore[0], Hard: desc.RlimitCore[1]})
	}
	if desc.RlimitData != nil {
		rec.AddRlimit(process.Rlimit{Resource: rlimitData, Soft: desc.RlimitData[0], Hard: desc.RlimitData[1]})
	}
	if desc.RlimitAs != nil {
		rec.AddRlimit(process.Rlimit{Resource: rlimitAs, Soft: desc.RlimitAs[0], Hard: desc.RlimitAs[1]})
	}
}

// calcServiceDepth computes a service's depth as max(dep.depth + 1) over all deps.
func calcServiceDepth(svc service.Service) int {
	depth := 0
	for _, dep := range svc.Record().Dependencies() {
		d := dep.To.Record().DepDepth() + 1
		if d > depth {
			depth = d
		}
	}
	return depth
}
