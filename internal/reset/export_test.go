package reset

// SetHookForTest makes the wipe call f between its steps, inside the transaction, so a test can make it fail halfway.
func (s *Service) SetHookForTest(f func(step string) error) { s.hook = f }
