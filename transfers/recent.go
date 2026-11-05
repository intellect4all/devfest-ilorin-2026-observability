package main

import (
	"strings"
	"sync"
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
// FIX for bug A: the map, keyed by the normalised key, is the only index.
// A lookup is O(1) however many transfers there are, nothing allocates per
// stored record, and a background sweep drops keys older than the window
// (the TTL), so the map cannot grow forever.
type recentTransfers struct {
	mu    sync.Mutex
	byKey map[string]*transfer

	statusMu sync.Mutex // guards Status and ProviderRef of remembered transfers
}

func newRecentTransfers() *recentTransfers {
	s := &recentTransfers{byKey: make(map[string]*transfer)}
	go s.sweepEvery(time.Minute)
	return s
}

// Claim returns the transfer already recorded under t.Key, or records t as
// in flight and returns nil. scanned is how many records were compared.
func (s *recentTransfers) Claim(t *transfer) (existing *transfer, scanned int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := normalizeKey(t.Key)
	if e, ok := s.byKey[key]; ok && !expired(e, time.Now()) {
		return s.copyOf(e), 1
	}
	s.byKey[key] = t
	return nil, 1
}

// Complete records how an in-flight transfer ended. It needs only statusMu,
// so finishing a transfer never waits behind a duplicate check.
func (s *recentTransfers) Complete(t *transfer, status, providerRef string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	t.Status, t.ProviderRef = status, providerRef
}

func (s *recentTransfers) copyOf(t *transfer) *transfer {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return t.snapshot()
}

// Forget drops an in-flight claim whose transfer never started (for
// example, a velocity-limit rejection), so the client may retry the key.
func (s *recentTransfers) Forget(t *transfer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key := normalizeKey(t.Key); s.byKey[key] == t {
		delete(s.byKey, key)
	}
}

// Load adds transfers read from the database at start-up.
func (s *recentTransfers) Load(ts []*transfer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, t := range ts {
		if !expired(t, now) {
			s.byKey[normalizeKey(t.Key)] = t
		}
	}
}

// Len reports how many transfers are remembered.
func (s *recentTransfers) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byKey)
}

// sweep forgets every key older than the window.
func (s *recentTransfers) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.byKey {
		if expired(t, now) {
			delete(s.byKey, k)
		}
	}
}

func (s *recentTransfers) sweepEvery(d time.Duration) {
	for now := range time.Tick(d) {
		s.sweep(now)
	}
}

func expired(t *transfer, now time.Time) bool { return t.CreatedAt.Before(now.Add(-recentWindow)) }

// normalizeKey makes Idempotency-Key comparison case-insensitive.
func normalizeKey(k string) string { return strings.ToLower(strings.TrimSpace(k)) }
