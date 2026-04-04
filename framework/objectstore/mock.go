package objectstore

import (
	"context"
	"fmt"
	"maps"
	"sync"
)

// InMemoryObjectStore is an in-memory ObjectStore implementation for testing.
type InMemoryObjectStore struct {
	mu      sync.RWMutex
	objects map[string][]byte
	tags    map[string]map[string]string

	// PutErr, if set, is returned by Put for simulating failures.
	PutErr error
	// GetErr, if set, is returned by Get for simulating failures.
	GetErr error
}

// NewInMemoryObjectStore creates a new in-memory object store.
func NewInMemoryObjectStore() *InMemoryObjectStore {
	return &InMemoryObjectStore{
		objects: make(map[string][]byte),
		tags:    make(map[string]map[string]string),
	}
}

func (m *InMemoryObjectStore) Put(_ context.Context, key string, data []byte, tags map[string]string) error {
	if m.PutErr != nil {
		return m.PutErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Store a copy to avoid mutation.
	cp := make([]byte, len(data))
	copy(cp, data)
	m.objects[key] = cp
	if len(tags) > 0 {
		tagsCp := make(map[string]string, len(tags))
		maps.Copy(tagsCp, tags)
		m.tags[key] = tagsCp
	} else {
		delete(m.tags, key)
	}
	return nil
}

func (m *InMemoryObjectStore) Get(_ context.Context, key string) ([]byte, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("objectstore: object not found: %s", key)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *InMemoryObjectStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	delete(m.tags, key)
	return nil
}

func (m *InMemoryObjectStore) DeleteBatch(_ context.Context, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		delete(m.objects, key)
		delete(m.tags, key)
	}
	return nil
}

func (m *InMemoryObjectStore) Ping(_ context.Context) error {
	return nil
}

func (m *InMemoryObjectStore) Close() error {
	return nil
}

// GetTags returns the tags stored for a given key. For testing assertions.
func (m *InMemoryObjectStore) GetTags(key string) map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tags, ok := m.tags[key]
	if !ok {
		return nil
	}
	cp := make(map[string]string, len(tags))
	maps.Copy(cp, tags)
	return cp
}

// Len returns the number of stored objects. For testing assertions.
func (m *InMemoryObjectStore) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.objects)
}

// Keys returns all stored keys. For testing assertions.
func (m *InMemoryObjectStore) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		keys = append(keys, k)
	}
	return keys
}
