package shared

import "sync"

type docRefEntry struct {
	content     []byte
	contentType string
}

type DocRefStore struct {
	mu      sync.Mutex
	entries map[string]docRefEntry
}

func NewDocRefStore() *DocRefStore {
	return &DocRefStore{entries: make(map[string]docRefEntry)}
}

func (s *DocRefStore) Register(ref string, content []byte, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[ref] = docRefEntry{content: content, contentType: contentType}
}

func (s *DocRefStore) Resolve(ref string) (content []byte, contentType string, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[ref]
	return e.content, e.contentType, ok
}
