# Observability for Distributed Systems — workshop repo

DevFest Ilorin 2026 · Abdul-Jemeel Odewole (Bold MFB)

A small payment system in Go with three services, so you can practise finding
real bugs with metrics, traces and logs.

```
client ──> transfers :8080 ──> ledger :8081 ──> Postgres
              │
              └──────────────> mockprovider :8082
```

| Service | What it does | Observability |
| --- | --- | --- |
| `transfers/` | Public API: `POST /transfers` | Logs + metrics done. **You add tracing.** |
| `ledger/` | Balance check and double-entry postings | Fully instrumented |
| `mockprovider/` | Fake payout provider with tunable latency and failures | Fully instrumented |
| `loadgen/` | Steady traffic so there is always something to look at | — |

## Before the workshop (10 minutes, on good internet)

1. Install Go 1.23+ and Docker Desktop (or Docker Engine with Compose v2).
2. `make pull` — downloads Go modules and every image.
3. `make up` — starts Postgres, ledger, mockprovider, Jaeger, Prometheus and Grafana.
4. `make seed ROWS=200000` — a small seed is enough for your own machine.
5. In a second terminal: `make dev` — runs the transfers service.
6. `make check` — every line should be a ✓.
7. Open Jaeger (http://localhost:16686) and Grafana (http://localhost:3000).

## During the workshop

| Command | What it does |
| --- | --- |
| `make dev` | Run transfers locally; Ctrl-C and re-run after each edit |
| `make tx` | Send one transfer |
| `make load` | Steady traffic until Ctrl-C |
| `make logs ID=<trace_id>` | Every log line, from every service, for one trace |
| `make explain` | Query plan for the ledger's balance lookup |

Behind? `git checkout checkpoint-1` (tracer + server handler done) or `git checkout solution`.

## The three signals, joined

- **Metrics:** `http_server_request_duration_seconds` histogram on every route gives Rate, Errors and Duration (the Grafana "Payments — RED" dashboard).
- **Traces:** OpenTelemetry → OTLP → Jaeger. W3C `traceparent` carries context between services.
- **Logs:** JSON via `log/slog`; any line logged with a request context carries `trace_id` and `span_id`.

## After the workshop

- `exercises/01-slow-balance` — the bug from the session, if you missed it.
- `exercises/02-flaky-provider` — a harder one: retries that charge customers twice.

## For the presenter

- `make branches` builds `main` (starter), `checkpoint-1` and `solution` from the marked-up
  source (`// >>> STEPn` blocks in `transfers/`). Run it from a clean tree; push all three.
- Commit `go.sum` after the first `make tidy` so attendees resolve exactly the same versions.
- `make bug-on` / `make bug-off` toggle the workshop fault (excluded from traces and metrics).
- Seed size: on a 1-CPU test box, 3,000,000 rows made each scan ~320 ms and p50 under
  `make load` ~1.1 s. Calibrate on the demo laptop until p99 sits at 600–1,000 ms.
- Offline kit: `make offline` vendors modules and saves every image to `devfest-images.tar`.
  Attendees: `docker load -i devfest-images.tar`, then `make up`.
