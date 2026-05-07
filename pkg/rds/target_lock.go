package rds

import "sync"

type targetLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newTargetLockManager() *targetLockManager {
	return &targetLockManager{locks: make(map[string]*sync.Mutex)}
}

func (m *targetLockManager) lock(target string) func() {
	if m == nil || target == "" {
		return func() {}
	}

	m.mu.Lock()
	lock, ok := m.locks[target]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[target] = lock
	}
	m.mu.Unlock()

	lock.Lock()
	return func() {
		lock.Unlock()
	}
}
