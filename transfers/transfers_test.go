package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTransfer(key string, age time.Duration) *transfer {
	return &transfer{ID: "trf_" + key, Key: key, From: "acct-1", To: "acct-2", Amount: 100,
		Status: statusProcessing, CreatedAt: time.Now().Add(-age)}
}

func TestClaimFindsReplayWithBothLookups(t *testing.T) {
	for _, linear := range []bool{false, true} {
		t.Run(fmt.Sprintf("linear=%v", linear), func(t *testing.T) {
			s := newRecentTransfers()
			s.linearScan.Store(linear)
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
			if linear && scanned != 101 {
				t.Errorf("linear scan compared %d records, want 101", scanned)
			}
			if !linear && scanned != 1 {
				t.Errorf("map lookup compared %d records, want 1", scanned)
			}
		})
	}
}

func TestClaimIgnoresKeyCase(t *testing.T) {
	for _, linear := range []bool{false, true} {
		s := newRecentTransfers()
		s.linearScan.Store(linear)
		s.Claim(newTransfer("8F14E45F-CEEA-467A-9575-0000000000AB", 0))
		if prev, _ := s.Claim(newTransfer(" 8f14e45f-ceea-467a-9575-0000000000ab", 0)); prev == nil {
			t.Errorf("linear=%v: lower-cased retry was not recognised", linear)
		}
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

// With bug C off, a second transfer from the same account must not wait
// for the first one to finish its payout.
func TestLockIsShortWithoutBugC(t *testing.T) {
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

// With bug C on, the second transfer waits until the first calls Done, and
// learns who held the lock and what it was doing.
func TestBugCHoldsLockAcrossPayout(t *testing.T) {
	l := newAccountLocks(1 << 40)
	l.holdAcrossPayout.Store(true)
	r1, _, _ := l.Reserve("acct-1", "t1", 1)
	r1.Step("payout")

	var wg sync.WaitGroup
	var w lockWait
	wg.Add(1)
	go func() {
		defer wg.Done()
		var r2 *reservation
		r2, w, _ = l.Reserve("acct-1", "t2", 1)
		r2.Done(false)
	}()
	time.Sleep(50 * time.Millisecond)
	r1.Done(false)
	wg.Wait()

	if w.Waited < 40*time.Millisecond {
		t.Errorf("waited %s, want about 50ms", w.Waited)
	}
	if w.HeldBy != "t1" || w.HeldByStep != "payout" {
		t.Errorf("held by %q during %q, want t1 during payout", w.HeldBy, w.HeldByStep)
	}
}

// BenchmarkClaim shows why bug A only hurts at scale.
func BenchmarkClaim(b *testing.B) {
	for _, n := range []int{1_000, 100_000, 300_000} {
		for _, linear := range []bool{false, true} {
			b.Run(fmt.Sprintf("n=%d/linear=%v", n, linear), func(b *testing.B) {
				s := newRecentTransfers()
				ts := make([]*transfer, n)
				for i := range ts {
					ts[i] = newTransfer(fmt.Sprintf("8F14E45F-CEEA-467A-9575-%012X", i), time.Hour)
				}
				s.Load(ts)
				s.linearScan.Store(linear)
				b.ResetTimer()
				for i := 0; b.Loop(); i++ {
					s.Claim(newTransfer(fmt.Sprintf("bench-%d", i), 0))
				}
			})
		}
	}
}
