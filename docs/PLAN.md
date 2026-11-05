# Plan

Workshop: **Observability for Distributed Systems: Metrics, Traces, and Logs That Actually Find Bugs**,
DevFest Ilorin 2026, presented by Abdul-Jemeel Odewole.

This plan was written after a spike that confirmed the risky integrations really work with the
pinned versions (see "Spike results"). Design choices and their reasons are in `DECISIONS.md`.

## Architecture

```
loadgen ──HTTP──> transfers ──HTTP──> ledger ──SQL──> Postgres
                      │  └──SQL──> Postgres (transfers table)
                      └──HTTP──> provider (mock payout switch)

every service ──OTLP/HTTP──> otel-lgtm [Collector → Prometheus | Tempo | Loki → Grafana]
Grafana alert rules ──webhook──> alertrelay ──HTTP──> ntfy (ntfy.sh, or a local container offline)
```

* One Go module, one Docker image per branch (`devfest26/app:<branch>`) that contains every binary
  (`transfers`, `ledger`, `provider`, `loadgen`, `alertrelay`). Compose picks the binary with `command:`.
* Each service has **two listeners**: the public API (instrumented) and an admin listener (bug toggles,
  never instrumented, published on `127.0.0.1` only).
* Postgres holds `entries` (ledger) and `transfers` (transfers service). Seed data is generated in SQL.
* `grafana/otel-lgtm` runs the Collector, Prometheus, Tempo, Loki and Grafana in one container. The
  provisioning in this repo is bind-mounted over the image's provisioning directories.

## Pinned versions

| Thing | Version | Released | Why |
|---|---|---|---|
| Go (build image) | `golang:1.26.8-alpine` | 2026 | `slog.NewMultiHandler` (Go 1.26) keeps the logging diff to one line |
| `go.opentelemetry.io/otel`, `sdk`, `sdk/metric`, OTLP HTTP exporters | v1.46.0 | 2026-08-25 | one coordinated release train, 10 weeks old |
| `otel/sdk/log`, `otel/log`, `otlploghttp` | v0.22.0 | 2026-08-25 | same train |
| `contrib/.../otelhttp`, `contrib/.../runtime` | v0.71.0 | 2026-08-26 | same train |
| `contrib/bridges/otelslog` | v0.20.1 | 2026-08-26 | same train |
| `github.com/jackc/pgx/v5` | v5.11.0 | 2026-09-07 | v5.12.0 is only days old |
| `grafana/otel-lgtm` | 0.35.0 (Grafana 13.2.2) | 2026-10-02 | five weeks old; amd64 and arm64 |
| `postgres` | 17.10-alpine | 2026-08 | |
| `binwiederhier/ntfy` | v2.29.0 | 2026-10-07 | offline Plan B server |
| `gcr.io/distroless/static-debian12` | `nonroot` | | runtime base |
| Playwright | 1.63.0, image `mcr.microsoft.com/playwright:v1.63.0-noble` | 2026-09-04 | evidence screenshots |

## Ports (host side, all overridable in `.env`)

| Port | What | Bound to |
|---|---|---|
| 8080 / 8081 / 8082 | transfers / ledger / provider API | 127.0.0.1 |
| 7080 / 7081 / 7082 | transfers / ledger / provider admin (bug toggles) | 127.0.0.1 |
| 3000 | Grafana | 127.0.0.1 |
| 9090 / 3200 / 3100 / 4318 | Prometheus / Tempo / Loki / OTLP HTTP | 127.0.0.1 |
| 8090 | alertrelay | 127.0.0.1 |
| 2586 | local ntfy (offline Plan B, `--profile offline`) | 0.0.0.0, so phones on the hotspot can reach it |
| 5432 | Postgres | 127.0.0.1 |

## Branch plan

Branches are cumulative: `main → 01-metrics → 02-traces → 03-logs → 04-alerts → {hunt, solution}`.
`hunt` and `solution` are siblings, both based on `04-alerts`. Shared tooling (Makefile, scripts,
docs, evidence) lives on `main` and reaches the other branches when the chain is replayed
(`scripts/rebase-chain.sh`).

| Branch | Adds |
|---|---|
| `main` | three services, loadgen, admin toggles for bugs A/B/C, compose, Makefile, seed, plain `log.Printf` logs, no telemetry |
| `01-metrics` | `internal/telemetry` with a MeterProvider; `otelhttp` handlers (RED), runtime metrics, process CPU, DB pool gauge, `transfers.processed` counter, `payout.duration` histogram; dashboard "Payments — RED & USE" |
| `02-traces` | TracerProvider plus W3C propagation; `otelhttp` client transport; pgx query tracer (DB spans with `db.query.text`); manual spans `duplicate_check`, `account_lock.wait`, `balance_check`, `post_entries`, `payout` |
| `03-logs` | `slog` JSON to stdout plus the OTLP slog bridge to Loki; `trace_id`/`span_id` on every line; the "why" log lines for each bug; trace↔log links |
| `04-alerts` | Grafana alert rules (p99 SLO, 5xx rate), notification policy, webhook contact point → `alertrelay` → ntfy |
| `hunt` | `make mystery` / `make reveal` / hints, `HUNT.md`; toggles leave no trace in any signal |
| `solution` | real fixes for all three bugs, `docs/BUGS.md` explains each |

## The three bugs

| Bug | Where | Toggle | Mechanism |
|---|---|---|---|
| A | transfers, `duplicate_check` | in memory | idempotency check scans a slice of every recent transfer (warmed from the DB at start-up) under one mutex, instead of a map lookup |
| B | ledger, balance query | Postgres (persists!) | `DROP INDEX CONCURRENTLY idx_entries_account_id` so the balance `SUM` becomes a sequential scan |
| C | transfers, per-account velocity-limit lock | in memory | the account lock is held for the whole transfer, including the provider payout, instead of just the limit bookkeeping |

The loadgen sends a configurable share of traffic from one hot "settlement wallet" account
(`acct-00001`), which is how payout fintechs really work. That skew is what makes the per-account lock
(bug C) contend.

The main incident is chosen from measured fingerprints (recorded in `DECISIONS.md`).

## Stage switching

`make prebuild` builds `devfest26/app:<branch>` for every branch from a temporary `git worktree`.
`make stage STAGE=<branch>` runs `git checkout`, then `docker compose up -d --no-build` for the app
services (only containers whose image changed are recreated), then calls Grafana's provisioning reload
APIs (dashboards, datasources, alerting) and waits for health. Postgres and the otel-lgtm container (with
its `/data` volume) are never recreated. Target time: under 15 s.

## Test plan (what `make verify` does)

`make verify` clones the repo into a scratch directory, so that switching branches never touches the
working tree. It writes evidence into `evidence/<UTC timestamp>_<sha>/` of the primary tree, appends
to `evidence/LOG.md` and rewrites `evidence/SUMMARY.md`.

1. **Per branch** (main, 01, 02, 03, 04, hunt, solution): `go build`, `go vet` and `go test` inside the pinned
   Go image; cold start `make reset up seed` (timed); `make check`; a golden-path transfer plus an
   idempotent replay.
2. **main:** screenshot of plain logs; prove no telemetry reaches Prometheus, Tempo or Loki.
3. **01-metrics:** healthy dashboard under load; each bug on gives a dashboard change with before/after
   p50/p99 from Prometheus; USE panels populated.
4. **02-traces:** one trace spans 3 services with no orphan root spans; for each bug, the slowest trace's
   widest span is the expected one.
5. **03-logs:** `trace_id` on every log line of every service; span → logs jump; for each bug, the
   "why" line is found from its trace.
6. **04-alerts:** main incident → alert fires → ntfy (screenshot of the ntfy web app); bug off → resolved
   → ntfy; timing from bug-on to notification.
7. **hunt:** walkthrough following only `HUNT.md` for each bug.
8. **solution:** all toggles on and latency stays healthy.
9. **Stage switching:** cycle all branches in talk order, each timed, each with a dashboard screenshot.
10. **Offline kit:** load the tarball into a `docker:dind` container started with `--network none`, then run
    `make up && make check`.

Screenshots are taken with Playwright inside the pinned Playwright image and reviewed by eye before a
check is marked PASS. Every number comes from Prometheus/Tempo/Loki APIs or command output.

## Execution environment for the test runs

The presenter's MacBook (Apple Silicon) was not answering on Docker during the build, so all test runs
happen on the homelab host `n2` (x86_64, 8 cores, 31 GB). It is a shared host: compose project
`devfest26`, `devfest26/*` image names, and no daemon-wide cleanup. Calibration numbers in the runbook
are labelled "measured on n2", and the runbook gives `make calibrate` for re-tuning on the laptop.

## Spike results (2026-11-05, otel-lgtm 0.35.0, OTel Go v1.46.0)

* `otelhttp` with the Go 1.22+ mux sets `http_route` from the pattern (`/transfers/{id}`).
* Prometheus in the image runs with `--enable-feature=exemplar-storage`; `query_exemplars` returned
  exemplars carrying `trace_id` and `span_id`.
* The runtime instrumentation has goroutines, memory and GC, but **no CPU series**, so we add
  `process.cpu.time` ourselves.
* OTLP logs land in Loki with `trace_id`/`span_id` as structured metadata; `{service_name="x"} |
  trace_id="…"` matches, and Tempo has the same trace.
* Grafana 13.2.2: `POST /api/admin/provisioning/{dashboards,datasources,alerting}/reload` all work, so
  stage switches don't need a Grafana restart.
* The image preinstalls `grafana-llm-app` from the internet at start-up, so we set
  `GF_PLUGINS_PREINSTALL_DISABLED=true` for offline use.
* Playwright's Chromium download fails on n2, so screenshots run in the pinned Playwright image.
