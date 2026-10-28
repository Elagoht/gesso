package lock

// Held is how many names m keeps.
func Held(m *Memory) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.locks)
}
