package service

// sleeper adds a process service that sleeps, never restarted.
func sleeper(set *ServiceSet, name string) *ProcessService {
	s := NewProcessService(set, name)
	s.SetCommand([]string{"/bin/sleep", "60"})
	s.autoRestart = RestartNever
	set.AddService(s)
	return s
}
