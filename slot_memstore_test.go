package gdata

import (
	"errors"
	"strings"
	"sync"
)

// memStore is an in-memory slotStore used by the fault-injection tests.
// It mirrors the filesystem semantics the engine relies on:
//   - SlotWrite replaces a value atomically (values are fully replaced);
//   - paths are forward-slash strings organized like directories;
//   - SlotList enumerates direct children of a directory;
//   - SlotRemoveAll removes a whole subtree;
//   - locks are advisory and can be forced to fail.
type memStore struct {
	mu       sync.Mutex
	files    map[string][]byte
	locks    map[string]bool
	lockBusy map[string]bool // scopes that must reject every lock attempt

	// Failures are injected by relative-path prefix.
	failWritePath string // SlotWrite returns failErr for this prefix
	failErr       error
}

func newMemStore() *memStore {
	return &memStore{
		files:    make(map[string][]byte),
		locks:    make(map[string]bool),
		lockBusy: make(map[string]bool),
	}
}

func (s *memStore) SlotWrite(relPath string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWritePath != "" && strings.HasPrefix(relPath, s.failWritePath) {
		return s.failErr
	}
	copied := make([]byte, len(data))
	copy(copied, data)
	s.files[relPath] = copied
	return nil
}

func (s *memStore) SlotRead(relPath string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[relPath]
	if !ok {
		return nil, nil
	}
	copied := make([]byte, len(data))
	copy(copied, data)
	return copied, nil
}

func (s *memStore) SlotRemove(relPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, relPath)
	return nil
}

func (s *memStore) SlotRemoveAll(relDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := relDir + "/"
	for k := range s.files {
		if strings.HasPrefix(k, prefix) || k == relDir {
			delete(s.files, k)
		}
	}
	delete(s.locks, relDir)
	return nil
}

func (s *memStore) SlotList(relDir string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := relDir + "/"
	seen := map[string]bool{}
	var names []string
	for k := range s.files {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		name := rest
		if idx := strings.Index(rest, "/"); idx >= 0 {
			name = rest[:idx]
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

func (s *memStore) SlotLock(relPath string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockBusy[relPath] {
		return nil, ErrBusy
	}
	if s.locks[relPath] {
		return nil, ErrBusy
	}
	s.locks[relPath] = true
	return func() {
		s.mu.Lock()
		delete(s.locks, relPath)
		s.mu.Unlock()
	}, nil
}

// errInjected is an arbitrary storage failure used to prove the engine
// never loses committed data on unexpected backend errors.
var errInjected = errors.New("injected storage failure")

func newTestEngine(store slotStore, count int) *slotEngine {
	return &slotEngine{store: store, slotCount: count}
}
