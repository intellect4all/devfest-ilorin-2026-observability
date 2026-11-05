# The bug hunt

Production is broken. One bug is live in the payment system. Your job is to say
**what** is wrong, **where** it is, and **why**, using only the three signals.

## Rules

1. **Signals only.** Use Grafana: the *Payments — RED & USE* dashboard, Explore
   (Tempo traces, Loki logs). No reading the source, `git diff`, or `docker compose logs`.
2. **Say all three:** what (which symptom), where (which service and which step), why (the cause, in one sentence).
3. **First correct answer with evidence wins.** Point at the panel, the span and the log line.
4. Hints come every two minutes. The presenter types `make hint`.

## How to look

| Question | Signal | Where in Grafana |
|---|---|---|
| Is something wrong? | metrics | *Payments — RED & USE*: the red tile, then the USE row: which panels moved, which did not? |
| Where? | traces | a diamond (exemplar) on the p99 line, or a trace ID in *Slowest transfers*. In the trace, find the span with the most time *of its own* |
| Why? | logs | on that span, **Logs for this span**. Read the WARN line: every number in it is a clue |

## Clues for the three possible bugs (one of them is live)

- One of them only gets bad when **traffic rises**.
- One of them makes the **tail** slow while the median hardly moves.
- One of them makes requests **queue for each other**, and goroutines pile up.

## For the presenter

```bash
make stage STAGE=hunt   # this branch
make mystery            # one random bug on (BUG=a|b|c to choose), answer kept secret in .hunt/
make hint               # next hint (4 in total)
make reveal             # which bug it was; turns it off
```

The timed hint ladder and the answer path for each bug are in `docs/RUNBOOK.md` under "Hunt guide".
