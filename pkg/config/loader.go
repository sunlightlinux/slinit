package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// DirLoader loads service descriptions from one or more directories.
type DirLoader struct {
	dirs    []string
	set     *service.ServiceSet
	loading map[string]bool // tracks loading state for circular dependency detection
}

// NewDirLoader creates a new directory-based service loader.
func NewDirLoader(set *service.ServiceSet, dirs []string) *DirLoader {
	return &DirLoader{
		dirs:    dirs,
		set:     set,
		loading: make(map[string]bool),
	}
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

	return dl.loadServiceImpl(name)
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

		// Transfer dependents from old to new
		dl.transferDependents(svc, newSvc)

		// Remove old deps
		dl.removeDependencies(svc)

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
func (dl *DirLoader) updateInPlace(svc service.Service, desc *ServiceDescription, filePath string) (service.Service, error) {
	// Check for cycles before modifying
	if err := dl.checkCycle(svc, desc); err != nil {
		return nil, err
	}

	// Update type-specific fields
	dl.updateTypeSpecificFields(svc, desc)

	// Update dependencies
	if err := dl.updateDependencies(svc, desc, filePath); err != nil {
		return nil, err
	}

	// Update common settings
	applyToService(svc, desc)

	return svc, nil
}

// updateTypeSpecificFields applies type-specific setters from the description.
func (dl *DirLoader) updateTypeSpecificFields(svc service.Service, desc *ServiceDescription) {
	switch s := svc.(type) {
	case *service.ProcessService:
		s.SetCommand(desc.Command)
		s.SetStopCommand(desc.StopCommand)
		s.SetWorkingDir(desc.WorkingDir)
		s.SetEnvFile(desc.EnvFile)
		if desc.StartTimeout > 0 {
			s.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			s.SetStopTimeout(desc.StopTimeout)
		}
		if desc.RestartDelay > 0 {
			s.SetRestartDelay(desc.RestartDelay)
		}
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			s.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		if desc.LogType == service.LogToBuffer {
			s.SetLogType(desc.LogType)
			s.SetLogBufMax(desc.LogBufMax)
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
		if desc.LogType == service.LogToBuffer {
			s.SetLogType(desc.LogType)
			s.SetLogBufMax(desc.LogBufMax)
		}
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
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			s.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		if desc.LogType == service.LogToBuffer {
			s.SetLogType(desc.LogType)
			s.SetLogBufMax(desc.LogBufMax)
		}
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

	return nil
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

func (dl *DirLoader) loadServiceImpl(name string) (service.Service, error) {
	// Check for circular dependency
	if dl.loading[name] {
		return nil, &ServiceLoadError{
			ServiceName: name,
			Message:     "circular dependency detected",
		}
	}
	dl.loading[name] = true
	defer delete(dl.loading, name)

	// Find and parse the service description file
	desc, filePath, err := dl.findAndParse(name)
	if err != nil {
		return nil, err
	}

	// Create the service based on type
	svc := dl.createService(name, desc)

	// Add to set before loading dependencies (allows circular detection)
	dl.set.AddService(svc)

	// Load and connect dependencies
	if err := dl.loadDependencies(svc, desc, filePath); err != nil {
		dl.set.RemoveService(svc)
		return nil, err
	}

	// Apply settings to the service record
	applyToService(svc, desc)

	return svc, nil
}

func (dl *DirLoader) findAndParse(name string) (*ServiceDescription, string, error) {
	for _, dir := range dl.dirs {
		path := filepath.Join(dir, name)
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
		defer f.Close()

		desc, err := Parse(f, name, path)
		if err != nil {
			return nil, "", err
		}
		return desc, path, nil
	}

	return nil, "", &ServiceLoadError{
		ServiceName: name,
		Message:     "service description not found",
	}
}

func (dl *DirLoader) createService(name string, desc *ServiceDescription) service.Service {
	switch desc.Type {
	case service.TypeInternal:
		return service.NewInternalService(dl.set, name)
	case service.TypeProcess:
		svc := service.NewProcessService(dl.set, name)
		svc.SetCommand(desc.Command)
		svc.SetStopCommand(desc.StopCommand)
		svc.SetWorkingDir(desc.WorkingDir)
		svc.SetEnvFile(desc.EnvFile)
		if desc.StartTimeout > 0 {
			svc.SetStartTimeout(desc.StartTimeout)
		}
		if desc.StopTimeout > 0 {
			svc.SetStopTimeout(desc.StopTimeout)
		}
		if desc.RestartDelay > 0 {
			svc.SetRestartDelay(desc.RestartDelay)
		}
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			svc.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		if desc.LogType == service.LogToBuffer {
			svc.SetLogType(desc.LogType)
			svc.SetLogBufMax(desc.LogBufMax)
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
		if desc.LogType == service.LogToBuffer {
			svc.SetLogType(desc.LogType)
			svc.SetLogBufMax(desc.LogBufMax)
		}
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
		if desc.RestartInterval > 0 || desc.RestartLimitCount > 0 {
			svc.SetRestartLimits(desc.RestartInterval, desc.RestartLimitCount)
		}
		if desc.LogType == service.LogToBuffer {
			svc.SetLogType(desc.LogType)
			svc.SetLogBufMax(desc.LogBufMax)
		}
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

// applyToService applies parsed configuration to the service record.
func applyToService(svc service.Service, desc *ServiceDescription) {
	rec := svc.Record()
	rec.SetAutoRestart(desc.AutoRestart)
	rec.SetSmoothRecovery(desc.SmoothRecovery)
	rec.SetFlags(desc.Flags)
	rec.SetTermSignal(desc.TermSignal)
	if desc.ChainTo != "" {
		rec.SetChainTo(desc.ChainTo)
	}
	if desc.SocketPath != "" {
		rec.SetSocketDetails(desc.SocketPath, desc.SocketPerms)
	}
}

// ServiceLoadError represents a service loading failure.
type ServiceLoadError struct {
	ServiceName string
	Message     string
}

func (e *ServiceLoadError) Error() string {
	return fmt.Sprintf("service '%s': %s", e.ServiceName, e.Message)
}
