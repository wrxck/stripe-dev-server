// Package store provides an in-memory ring buffer of captured HTTP
// request/response pairs that flowed through the proxy. Safe for
// concurrent use.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Capture is a single round-trip through the proxy.
type Capture struct {
	ID              string            `json:"id"`
	Timestamp       time.Time         `json:"timestamp"`
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	Query           string            `json:"query,omitempty"`
	RequestHeaders  map[string]string `json:"requestHeaders,omitempty"`
	RequestBody     string            `json:"requestBody,omitempty"`
	Status          int               `json:"status"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	ResponseBody    string            `json:"responseBody,omitempty"`
	DurationMs      int64             `json:"durationMs"`
}

// Store is a bounded ring buffer.
type Store struct {
	mu       sync.RWMutex
	captures []*Capture
	max      int
}

// New returns a Store retaining up to max captures (oldest evicted on overflow).
// max <= 0 defaults to 1000.
func New(max int) *Store {
	if max <= 0 {
		max = 1000
	}
	return &Store{max: max}
}

// Add stores a capture and returns the assigned ID.
func (s *Store) Add(c *Capture) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = newID()
	}
	if c.Timestamp.IsZero() {
		c.Timestamp = time.Now().UTC()
	}
	s.captures = append([]*Capture{c}, s.captures...)
	if len(s.captures) > s.max {
		s.captures = s.captures[:s.max]
	}
	return c.ID
}

// All returns a snapshot copy of captures (newest first), optionally filtered
// by path substring (case-insensitive). limit <= 0 means no limit.
func (s *Store) All(filterPath string, limit int) []*Capture {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Capture, 0, len(s.captures))
	needle := strings.ToLower(filterPath)
	for _, c := range s.captures {
		if needle != "" && !strings.Contains(strings.ToLower(c.Path), needle) {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ByID returns the capture with the given ID, or nil if not found.
func (s *Store) ByID(id string) *Capture {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.captures {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Clear drops all captures.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captures = nil
}

// Count returns the current capture count.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.captures)
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
