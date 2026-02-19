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
func (dl *DirLoader) ReloadService(svc service.Service) (service.Service, error) {
	return svc, nil // stub for Phase 1
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
		return svc
	default:
		// TypeBGProcess, TypeTriggered → placeholder as internal for now (Phase 3)
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
