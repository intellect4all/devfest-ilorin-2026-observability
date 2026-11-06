# The three bugs

All three bugs exist on every branch. They are switched on and off at runtime through admin endpoints
that bind only to localhost and are never instrumented, so turning one on leaves no trace in any signal:

```bash
make bug-a-on / bug-a-off      # transfers: O(n) duplicate check
make bug-b-on / bug-b-off      # ledger: the balance query loses its index
make bug-c-on / bug-c-off      # transfers: account lock held across the payout call
make bugs-off                  # all off, provider healthy
make bugs                      # show the current state
```

Bugs A and C live in memory, so a stage switch (which restarts the services) turns them off. Bug B
lives in Postgres and survives restarts until `make bug-b-off`, `make bugs-off` or `make seed`.

On the `solution` branch all three are fixed in the code. The toggles still answer there, but they
change nothing.

The numbers below were measured on n2 under `make load` (20 req/s, 90% from the settlement wallet).
Running `make verify` writes the full results to `evidence/SUMMARY.md` (kept out of git).

---

## Bug C: a lock held across a network call (the main incident)

**Where:** `transfers/locks.go`, `accountLocks.Reserve` / `reservation.Done`.

**Cause.** Each account has a mutex that protects its daily payout total. The lock only needs to cover
the few microseconds of bookkeeping: check the limit, add the amount. With the bug on, the lock is kept
until the whole transfer is finished, so it is held across the ledger call and the payout provider's
network call. 90% of transfers leave the settlement wallet, so every one of them queues behind the
previous transfer's payout.

| Signal | Fingerprint |
|---|---|
| Metrics | p99 goes red within 20 s and keeps climbing in steps; goroutines climb (requests waiting inside the process: USE *saturation*); 5xx stays at 0%; CPU, DB pool and memory stay flat |
| Trace | a long `account_lock.wait` span before any real work, taking nearly the whole request; `post_entries` and `payout` are short |
| Log | `WARN waited for account lock` with `wait_ms` (seconds), `held_by_transfer` (the transfer that had the lock) and `held_by_step` (what it was doing, `payout` about 14 times in 15 (measured: 1,955 of 2,089), otherwise `post_entries`): the lock is being held for the whole transfer, payout included |
| Alert | *Transfers p99 latency above 300 ms* reaches ntfy in about 35 s |

**Fix (on `solution`).** The critical section shrinks to the bookkeeping only. `Reserve` takes the
lock, checks and updates the total, and releases it with `defer` before returning. A failed transfer
takes the lock again for a moment to give its amount back. No I/O ever runs while an account lock is
held. Rule of thumb: **never hold a lock across a network call**.

---

## Bug B: the missing index

**Where:** Postgres, behind `ledger/main.go`'s balance query
`SELECT COALESCE(SUM(amount), 0)::bigint FROM entries WHERE account_id = $1`.

**Cause.** The index on `entries(account_id)` is dropped, as a bad migration might do. Every balance
check becomes a sequential scan of the whole table (7 million rows, about 1.1 GB). Only ordinary
accounts run the check (the settlement wallet is pre-funded), so about 10% of transfers are affected.

| Signal | Fingerprint |
|---|---|
| Metrics | p99 rises to about 0.6 s while **p50 stays flat** (only the tail is hit); 5xx stays at 0%; in *p99 by service*, ledger rises; the ledger DB pool is busier |
| Trace | the ledger's `SELECT entries` span is the widest, with `db.query.text` showing the query |
| Log | ledger `WARN slow query` with `duration_ms` and `db.query.text` |
| Proof | `make explain` shows `Seq Scan on entries`; after `make bug-b-off` it shows an index scan |

**Fix (on `solution`).** The ledger ensures, on every start, an index the balance query depends on:
`idx_entries_account_balance ON entries (account_id) INCLUDE (amount)`. It is a covering index, so the
`SUM` is answered by an *index-only scan*. Because the service re-checks the index at every start, a
single lost migration cannot silently bring the sequential scan back. On `solution`, `make bug-b-on`
still drops the original index, and latency does not move.

**Calibration.** With bug B on, p99 grows with the row count. 7M rows gave p99 about 630 ms on n2
(client side, median of 5 s windows over 120 s). Re-tune with `make calibrate` and `make seed ROWS=…`.
Near saturation the behaviour is a cliff, so change the row count in small steps.

---

## Bug A: O(n) work that only hurts under traffic

**Where:** `transfers/recent.go`, `recentTransfers.Claim`.

**Cause.** The duplicate check looks for the request's Idempotency-Key among every transfer of the last
24 hours. With the bug on, it walks the whole list, lower-casing every stored key on the way
(retries can arrive lower-cased), while holding the one mutex that guards the list:
- At 80k remembered transfers, each request spends about 10 ms in the walk.
- That is fine at 20 req/s, which is why it passed review.
- During `make surge` (+80 req/s) the walks queue behind each other and the service saturates.

The list also grows with traffic, by about 72k entries per hour of `make load`.

| Signal | Fingerprint |
|---|---|
| Metrics | at steady load p99 barely moves; during `make surge`, p99 explodes and transfers CPU and allocation rate (`Memory allocated / s`) climb with the request rate |
| Trace | the `duplicate_check` span widens and has **no child spans**: the time is spent inside the service, not waiting on anyone |
| Log | `WARN slow duplicate check` with `entries_scanned` (tens of thousands) and `duration_ms` |

**Fix (on `solution`).** The map keyed by the normalised Idempotency-Key is the only index. A lookup is
O(1) at any size and allocates nothing per stored record. A background sweep removes keys older than
24 hours (the TTL), so the map cannot grow forever.

---

## The provider (not a bug, a knob)

`make provider-flaky` makes the mock payout switch reject 20% of payouts, and `make provider-normal`
restores it. It drives the *Transfers 5xx above 5%* alert: a symptom alert for failures rather than
slowness.
