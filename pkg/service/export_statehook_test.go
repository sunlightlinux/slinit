package service

func (s *ProcessService) waitingForStartSlotForTest() bool {
	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()
	return s.waitingForStartSlot
}
