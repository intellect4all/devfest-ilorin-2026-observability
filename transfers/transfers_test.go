package main

import (
	"fmt"
	"testing"
	"time"
)

func newTransfer(key string, age time.Duration) *transfer {
	return &transfer{ID: "trf_" + key, Key: key, From: "acct-1", To: "acct-2", Amount: 100,
		Status: statusProcessing, CreatedAt: time.Now().Add(-age)}
}

func TestClaimFindsReplay(t *testing.T) {
	s := newRecentTransfers()
	for i := range 100 {
		s.Load([]*transfer{newTransfer(fmt.Sprint("k", i), time.Minute)})
	}
	first := newTransfer("new-key", 0)
	if prev, _ := s.Claim(first); prev != nil {
		t.Fatalf("first claim found %v, want nothing", prev.ID)
	}
	s.Complete(first, statusCompleted, "PRV-1")

	prev, scanned := s.Claim(newTransfer("new-key", 0))
	if prev == nil || prev.ID != first.ID || prev.Status != statusCompleted {
		t.Fatalf("replay got %+v, want the completed first transfer", prev)
	}
	if scanned != 1 {
		t.Errorf("lookup compared %d records, want 1 (the fix for bug A)", scanned)
	}
}

// The TTL: a sweep forgets keys older than the window.
func TestSweepForgetsExpiredKeys(t *testing.T) {
	s := newRecentTransfers()
	s.Claim(newTransfer("fresh", time.Hour))
	s.Claim(newTransfer("stale", 23*time.Hour))
	s.sweep(time.Now().Add(2 * time.Hour)) // two hours later "stale" is 25 h old
	if got := s.Len(); got != 1 {
		t.Fatalf("remembered %d keys after the sweep, want 1", got)
	}
}

func TestClaimIgnoresKeyCase(t *testing.T) {
	s := newRecentTransfers()
	s.Claim(newTransfer("8F14E45F-CEEA-467A-9575-0000000000AB", 0))
	if prev, _ := s.Claim(newTransfer(" 8f14e45f-ceea-467a-9575-0000000000ab", 0)); prev == nil {
		t.Error("lower-cased retry was not recognised")
	}
}

func TestOldTransfersAreForgotten(t *testing.T) {
	s := newRecentTransfers()
	s.Load([]*transfer{newTransfer("old", 25*time.Hour), newTransfer("fresh", time.Hour)})
	if got := s.Len(); got != 1 {
		t.Fatalf("remembered %d transfers, want 1 (the 25h-old one must be dropped)", got)
	}
	if prev, _ := s.Claim(newTransfer("old", 0)); prev != nil {
		t.Fatal("a key older than the window was still remembered")
	}
}

func TestForgetReleasesKey(t *testing.T) {
	s := newRecentTransfers()
	tr := newTransfer("k", 0)
	s.Claim(tr)
	s.Forget(tr)
	if prev, _ := s.Claim(newTransfer("k", 0)); prev != nil {
		t.Fatal("key still claimed after Forget")
	}
}

func TestVelocityLimit(t *testing.T) {
	l := newAccountLocks(250)
	r1, _, err := l.Reserve("acct-1", "t1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Reserve("acct-1", "t2", 100); err != errVelocityLimit {
		t.Fatalf("second reserve err = %v, want errVelocityLimit", err)
	}
	r1.Done(true) // failed transfer gives its amount back
	if _, _, err := l.Reserve("acct-1", "t3", 100); err != nil {
		t.Fatalf("after refund err = %v, want nil", err)
	}
}

// The fix for bug C: a second transfer from the same account never waits
// for the first one to finish its payout.
func TestLockNeverSpansPayout(t *testing.T) {
	l := newAccountLocks(1 << 40)
	r1, _, _ := l.Reserve("acct-1", "t1", 1)
	defer r1.Done(false)

	done := make(chan lockWait)
	go func() {
		r2, w, _ := l.Reserve("acct-1", "t2", 1)
		r2.Done(false)
		done <- w
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second reserve blocked while the first transfer was still in flight")
	}
}

// BenchmarkClaim: with the fix, a lookup costs the same at any size.
func BenchmarkClaim(b *testing.B) {
	for _, n := range []int{1_000, 100_000, 300_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			s := newRecentTransfers()
			ts := make([]*transfer, n)
			for i := range ts {
				ts[i] = newTransfer(fmt.Sprintf("8F14E45F-CEEA-467A-9575-%012X", i), time.Hour)
			}
			s.Load(ts)
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				s.Claim(newTransfer(fmt.Sprintf("bench-%d", i), 0))
			}
		})
	}
}
