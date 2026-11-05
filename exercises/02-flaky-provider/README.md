# Exercise 02 — Charged twice

Harder than the live bug, and closer to what hurts in production payments.

## Set up

```
git checkout solution           # you need full tracing for this one
make up && make dev             # terminal 2
make provider-flaky             # 30% of payouts now hang past the 1 s client timeout
make load                       # terminal 3, let it run for a minute
```

## Symptoms

- The error rate on POST /transfers rises. That part is expected: the provider is flaky.
- Finance says some customers were debited more than once.

## Your job

1. Find one trace that proves a double debit. What does its waterfall look like
   compared with a healthy transfer?
2. Confirm it in the ledger:
   `SELECT transfer_id, count(*)/2 AS times_posted FROM entries
    WHERE transfer_id LIKE 'trf_%' GROUP BY 1 HAVING count(*) > 2;`
3. Find the code that causes it.
4. Fix it so that no retry can ever post the same transfer twice, even if transfers
   itself crashes and restarts mid-request.

## Hints (read one at a time)

1. Count the `POST /entries` spans in a failed transfer's trace.
2. Retrying the provider call is reasonable. What else gets retried with it?
3. Moving one line fixes the symptom. The real fix makes the ledger itself idempotent:
   what should be unique in `entries`?

## Discussion

- A provider timeout does not mean the provider did nothing. How would you reconcile?
- Which metric would you alert on for this class of bug?

`make provider-normal` restores the provider.
