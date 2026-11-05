package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// recentWindow is how long an Idempotency-Key is remembered.
const recentWindow = 24 * time.Hour

// recentTransfers remembers every transfer of the last 24 hours so that a
// retried request with the same Idempotency-Key gets the original transfer
// back instead of moving the money twice.
//
// Keys are compared case-insensitively: mobile SDKs send upper-case UUIDs,
// some retries arrive lower-cased by a proxy.
//
// BUG A lives here. Claim can find a key two ways: through the byKey map
// (O(1)), or by walking the whole list and normalising every stored key on
// the way (O(n), one allocation per record). The walk is cheap with a few
// hundred transfers, which is why it passed review and every test. It holds
// the mutex for the whole walk, though, so under real traffic every request
// queues behind every other request's walk.
type recentTransfers struct {
	mu    sync.Mutex
	list  []*transfer          // oldest first
	byKey map[string]*transfer // same records, indexed by normalised key

	linearScan atomic.Bool // bug A toggle
}

func newRecentTransfers() *recentTransfers {
	return &recentTransfers{byKey: make(map[string]*transfer)}
}

// Claim returns the transfer already recorded under t.Key, or records t as
// in flight and returns nil. scanned is how many records were compared.
func (s *recentTransfers) Claim(t *transfer) (existing *transfer, scanned int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := normalizeKey(t.Key)
	if s.linearScan.Load() {
		for _, r := range s.list {
			scanned++
			if normalizeKey(r.Key) == key {
				existing = r
				break
			}
		}
	} else {
		existing = s.byKey[key]
		scanned = 1
	}
	if existing != nil {
		return existing.snapshot(), scanned
	}

	s.add(t)
	return nil, scanned
}

// Complete records how an in-flight transfer ended.
func (s *recentTransfers) Complete(t *transfer, status, providerRef string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Status, t.ProviderRef = status, providerRef
}

// Forget drops an in-flight claim whose transfer never started (for
// example, a velocity-limit rejection), so the client may retry the key.
func (s *recentTransfers) Forget(t *transfer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key := normalizeKey(t.Key); s.byKey[key] == t {
		delete(s.byKey, key)
	}
	for i := len(s.list) - 1; i >= 0; i-- {
		if s.list[i] == t {
			s.list = append(s.list[:i], s.list[i+1:]...)
			break
		}
	}
}

// Load adds transfers read from the database at start-up.
func (s *recentTransfers) Load(ts []*transfer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range ts {
		s.add(t)
	}
}

// Len reports how many transfers are remembered.
func (s *recentTransfers) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list)
}

// add records t and forgets transfers older than the window. Callers hold mu.
func (s *recentTransfers) add(t *transfer) {
	s.list = append(s.list, t)
	s.byKey[normalizeKey(t.Key)] = t

	cutoff := time.Now().Add(-recentWindow)
	n := 0
	for n < len(s.list) && s.list[n].CreatedAt.Before(cutoff) {
		delete(s.byKey, normalizeKey(s.list[n].Key))
		n++
	}
	if n > 0 {
		s.list = s.list[n:]
	}
}

// normalizeKey makes Idempotency-Key comparison case-insensitive.
func normalizeKey(k string) string { return strings.ToLower(strings.TrimSpace(k)) }
