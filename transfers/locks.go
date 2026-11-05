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
// BUG C lives here. The lock only needs to cover the few microseconds of
// limit bookkeeping. With holdAcrossPayout on, Reserve keeps the account
// locked until the whole transfer is finished, including the network calls
// to the ledger and the payout provider. Every other transfer from the same
// account then waits in line.
type accountLocks struct {
	limit int64 // kobo per account per day

	mu       sync.Mutex
	accounts map[string]*accountLock

	holdAcrossPayout atomic.Bool // bug C toggle
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

// Step reports what the holder is doing, e.g. "payout".
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

// reservation is a granted payout reservation.
type reservation struct {
	lock   *accountLock
	holder *lockHolder
	amount int64
	held   bool // the account lock is still held (bug C)
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
	w.Waited = time.Since(start)

	h := &lockHolder{TransferID: transferID}
	h.step.Store("velocity_check")
	a.holder.Store(h)

	today := time.Now().UTC().Format(time.DateOnly)
	if a.day != today {
		a.day, a.spent = today, 0
	}
	if a.spent+amount > l.limit {
		a.holder.Store(nil)
		a.mu.Unlock()
		return nil, w, errVelocityLimit
	}
	a.spent += amount

	res := &reservation{lock: a, holder: h, amount: amount, held: true}
	if !l.holdAcrossPayout.Load() {
		res.unlock() // correct: the critical section ends here
	}
	return res, w, nil
}

// Step records what the transfer is doing while it may hold the lock.
func (r *reservation) Step(step string) { r.holder.step.Store(step) }

// Done finishes the reservation. A failed transfer gives its amount back.
func (r *reservation) Done(failed bool) {
	if failed {
		if !r.held {
			r.lock.mu.Lock()
			defer r.lock.mu.Unlock()
		}
		r.lock.spent -= r.amount
	}
	if r.held {
		r.unlock()
	}
}

func (r *reservation) unlock() {
	r.held = false
	r.lock.holder.Store(nil)
	r.lock.mu.Unlock()
}
