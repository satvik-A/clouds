// Package provider provides the provider registry.
package provider

import (
	"fmt"
	"sync"
)

// DefaultRegistry is the default provider registry implementation.
type DefaultRegistry struct {
	providers map[string]Provider
	primary   string
	mu        sync.RWMutex
}

// NewRegistry creates a new provider registry.
func NewRegistry() *DefaultRegistry {
	return &DefaultRegistry{
		providers: make(map[string]Provider),
	}
}

// Register adds a new provider.
func (r *DefaultRegistry) Register(p Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[p.ID()]; exists {
		return fmt.Errorf("provider with ID '%s' already registered", p.ID())
	}

	r.providers[p.ID()] = p

	// First provider becomes primary by default
	if r.primary == "" {
		r.primary = p.ID()
	}

	return nil
}

// Get returns provider by ID.
func (r *DefaultRegistry) Get(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[id]
	return p, ok
}

// All returns all registered providers.
func (r *DefaultRegistry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}

// Primary returns the primary provider.
func (r *DefaultRegistry) Primary() Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.primary == "" {
		return nil
	}
	return r.providers[r.primary]
}

// SetPrimary sets the primary provider.
func (r *DefaultRegistry) SetPrimary(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[id]; !exists {
		return fmt.Errorf("provider '%s' not found", id)
	}

	r.primary = id
	return nil
}

// Remove removes a provider by ID.
// Should only be called after verifying no data depends on this provider.
func (r *DefaultRegistry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[id]; !exists {
		return fmt.Errorf("provider '%s' not found", id)
	}

	delete(r.providers, id)

	// Clear primary if removing primary provider
	if r.primary == id {
		r.primary = ""
		// Set new primary if other providers exist
		for newID := range r.providers {
			r.primary = newID
			break
		}
	}

	return nil
}
