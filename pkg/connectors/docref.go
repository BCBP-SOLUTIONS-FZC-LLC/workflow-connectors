package connectors

import "sync"

type docRefEntry struct {
	content     []byte
	contentType string
}

type docRefStore struct {
	mu      sync.Mutex
	entries map[string]docRefEntry
}

func newDocRefStore() *docRefStore {
	return &docRefStore{entries: make(map[string]docRefEntry)}
}

func (s *docRefStore) register(ref string, content []byte, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[ref] = docRefEntry{content: content, contentType: contentType}
}

func (s *docRefStore) resolve(ref string) (content []byte, contentType string, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ref]
	return e.content, e.contentType, ok
}
