# Exercise 01 — Transfers got slow

This is the bug from the live session.

## Set up

```
make up && make seed            # the default 3,000,000 rows
make dev                        # terminal 2
make load                       # terminal 3
make bug-on                     # someone else should run this, so you don't know what changed
```

## Your job

Customers say transfers are slow. Every request still returns 2xx. Find the root cause using
only Grafana, Jaeger, `make logs` and `make explain`. Write down the path you took.

## Hints (read one at a time)

1. Is it slow, or is it broken? Compare the latency and error panels.
2. In Jaeger, sort traces by Longest First.
3. Which span is widest? Read its attributes.

## Questions

- Why did no log line ever show an error?
- Which alert would have caught this, and which alert would not have?

`make bug-off` restores normal service.
