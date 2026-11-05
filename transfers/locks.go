package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var errVelocityLimit = errors.New("daily payout limit reached")

// accountLocks enforces a per-account daily payout limit. Checking and
// updating the running total must not race, so each account has a mutex.
//
// FIX for bug C: the critical section is only the limit bookkeeping. Reserve
// takes the lock, checks and updates the total, and releases it before any
// I/O happens; a failed transfer takes it again for a moment to give its
// amount back. No network call ever runs while an account lock is held.
type accountLocks struct {
	limit int64 // kobo per account per day

	mu       sync.Mutex
	accounts map[string]*accountLock
}

type accountLock struct {
	mu     sync.Mutex
	day    string
	spent  int64
	holder atomic.Pointer[lockHolder] // who holds mu right now, if anyone
}

// lockHolder describes the transfer that holds an account lock.
type lockHolder struct {
	TransferID string
	step       atomic.Value // string: what the holder is doing right now
}

// Step reports what the holder is doing.
func (h *lockHolder) Step() string {
	s, _ := h.step.Load().(string)
	return s
}

// lockWait describes how long a transfer waited for its account lock.
type lockWait struct {
	Waited     time.Duration
	HeldBy     string // transfer that held the lock when we started waiting
	HeldByStep string // what that transfer was doing at the time
}

// reservation is a granted payout reservation. It never holds the lock.
type reservation struct {
	lock   *accountLock
	amount int64
}

func newAccountLocks(limit int64) *accountLocks {
	return &accountLocks{limit: limit, accounts: make(map[string]*accountLock)}
}

func (l *accountLocks) get(account string) *accountLock {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.accounts[account]
	if !ok {
		a = &accountLock{}
		l.accounts[account] = a
	}
	return a
}

// Reserve counts amount against the account's daily limit.
func (l *accountLocks) Reserve(account, transferID string, amount int64) (*reservation, lockWait, error) {
	a := l.get(account)

	var w lockWait
	if h := a.holder.Load(); h != nil {
		w.HeldBy, w.HeldByStep = h.TransferID, h.Step()
	}
	start := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock() // the critical section ends with this function
	w.Waited = time.Since(start)

	h := &lockHolder{TransferID: transferID}
	h.step.Store("velocity_check")
	a.holder.Store(h)
	defer a.holder.Store(nil)

	today := time.Now().UTC().Format(time.DateOnly)
	if a.day != today {
		a.day, a.spent = today, 0
	}
	if a.spent+amount > l.limit {
		return nil, w, errVelocityLimit
	}
	a.spent += amount
	return &reservation{lock: a, amount: amount}, w, nil
}

// Step is kept for the handler's call sites; with the fix nobody holds the
// lock while working, so there is nothing to report.
func (r *reservation) Step(string) {}

// Done finishes the reservation. A failed transfer gives its amount back.
func (r *reservation) Done(failed bool) {
	if !failed {
		return
	}
	r.lock.mu.Lock()
	defer r.lock.mu.Unlock()
	r.lock.spent -= r.amount
}
