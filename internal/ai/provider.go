// Package ai contains the provider-agnostic LLM layer: a common Provider interface
// (implemented by adapters in subpackages), the triage prompt, JSON schema and result parsing.
package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Request is a provider-agnostic structured completion request.
type Request struct {
	Model     string
	System    string
	User      string
	Schema    map[string]any // JSON Schema the answer must conform to
	MaxTokens int
}

// Response is a provider-agnostic completion result.
type Response struct {
	Text         string // JSON document
	Model        string
	InputTokens  int
	OutputTokens int
}

// Provider is an LLM adapter.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Error is a normalized provider error.
type Error struct {
	Provider  string
	Status    int
	Message   string
	Retryable bool
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s: http %d: %s", e.Provider, e.Status, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}

// IsRetryable reports whether a request may be retried.
func IsRetryable(err error) bool {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	// network errors and timeouts are retryable, cancellation is not
	return !errors.Is(err, context.Canceled)
}

// RetryableStatus returns true for HTTP statuses worth retrying.
func RetryableStatus(code int) bool {
	switch code {
	case 408, 409, 425, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// Registry holds configured providers; the active one is chosen at call time (hot switch).
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }

func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Names returns sorted names of available providers.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
