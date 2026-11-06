# Observability for Distributed Systems: Metrics, Traces, and Logs That Actually Find Bugs

Demo repository for the DevFest Ilorin 2026 workshop by Abdul-Jemeel Odewole.

[![Open In Colab](https://colab.research.google.com/assets/colab-badge.svg)](https://colab.research.google.com/github/intellect4all/devfest-ilorin-2026-observability/blob/main/colab/devfest_observability.ipynb)
**No Docker? Follow along in your browser:** the Colab notebook runs the whole stack, Grafana included, on Google's free
machine. Open it, then run the cells in order. Setup takes 5 to 7 minutes.

A small payment system in Go, three services and Postgres, with three bugs you can switch on and
off. Each branch adds one signal, so you can watch the same incident through metrics (*is something
wrong?*), then traces (*where?*), then logs (*why?*).

```
loadgen ──> transfers ──> ledger ──> Postgres
                │
                └──────> provider (mock payout switch)

all services ──OTLP──> Collector ──> Prometheus · Tempo · Loki ──> Grafana   (grafana/otel-lgtm)
```

## Quick start (under 10 minutes on good internet)

You need **Docker** with Compose v2 (Docker Desktop on macOS or Windows), **git** and **make**. Give
Docker at least 6 GB of memory and 8 GB of disk. You don't need Go.

```bash
git clone <this repo> devfest26 && cd devfest26
cp .env.example .env        # then set NTFY_TOPIC to something only you know
make up                     # first run: pulls about 2 GB and builds the services (about 5 min)
make seed                   # 7 million ledger entries (about 1 min)
make check                  # every line should be a ✓
make load                   # steady traffic in the background
```

Open **http://localhost:3000**, then go to *Dashboards → Payments — RED & USE*. There's no login.

On `main` the dashboard is empty on purpose: `main` is the print-debugging baseline. Move to a branch
with telemetry:

```bash
git checkout 01-metrics && make up   # rebuilds for this branch, keeps your data
```

Then break something and watch:

```bash
make bug-c-on      # a lock held across a network call
make bug-c-off
make bugs-off      # everything off
```

## The branches

Each branch builds on the one before it. `git diff 01-metrics 02-traces` shows exactly what one step
of instrumentation costs.

| Branch | Adds | Logs |
|---|---|---|
| `main` | the services, compose, loadgen, seed data, bug toggles. No telemetry | `log.Printf` |
| `01-metrics` | RED metrics (`otelhttp`), Go runtime and CPU (USE), DB pool gauge, business counter, payout histogram, the dashboard | plain |
| `02-traces` | a trace across all three services: server and client spans, DB spans, business-step spans, exemplars | plain |
| `03-logs` | `slog` JSON with `trace_id` on every line, shipped to Loki; trace ↔ log links | structured |
| `04-alerts` | symptom alerts (p99 SLO, 5xx rate) to your phone through ntfy | structured |
| `hunt` | the audience exercise: `make mystery`, then find the bug ([HUNT.md](HUNT.md)) | structured |
| `solution` | all three bugs fixed ([docs/BUGS.md](docs/BUGS.md)) | structured |

To switch between branches in seconds, without compiling, build every branch's image once with
`make prebuild`, then use `make stage STAGE=02-traces`.

## Useful targets

`make help` lists everything. These are the ones you'll use most:

| | |
|---|---|
| `make up` / `make down` / `make reset` | start, stop (keeps data), stop and delete this project's data |
| `make seed ROWS=…` | reload the ledger (smaller `ROWS` for a lighter laptop; bug B gets milder) |
| `make load` / `make load-stop` / `make surge` | steady traffic, stop it, add a 90-second rush |
| `make tx` / `make tx KEY=…` | one transfer; repeat the key to see the idempotent replay |
| `make bug-a-on` … `make bugs-off` | the faults ([docs/BUGS.md](docs/BUGS.md)) |
| `make explain` | the balance query's plan (index scan, or seq scan when bug B is on) |
| `make alert-test` | send a test notification to your ntfy topic (`04-alerts` and later) |
| `make calibrate` | measure how hard each bug hits on your machine |

## Alerts on your phone (04-alerts)

1. Install the **ntfy** app (Android or iOS) and subscribe to your `NTFY_TOPIC` on `https://ntfy.sh`.
   `make subscribe` shows the topic and a QR code.
2. Run `make stage STAGE=04-alerts`, or `git checkout 04-alerts && make up`.
3. Run `make alert-test` and your phone buzzes. Then `make bug-c-on`: about 35 seconds later the
   *Transfers p99 latency above 300 ms* alert arrives. After `make bugs-off`, the resolve follows.

There's no internet at the venue? `make ntfy-local` runs an ntfy server on your laptop. Set
`NTFY_URL=http://ntfy` in `.env`, then run `make subscribe`: it shows phones on the same Wi-Fi or
hotspot your laptop's address and a QR code. Without internet, iPhones don't get pushes; keep the
page open in Safari instead.

## Troubleshooting

- **A port is taken:** change it in `.env` (`GRAFANA_PORT`, `PROM_PORT`, …) and run `make up`.
- **`make check` shows ✗ for loki or tempo right after `make up`:** they need about 20 seconds; run it again.
- **The dashboard says "No data":** start traffic with `make load`, and make sure you're on
  `01-metrics` or later.
- **Everything is slow:** give Docker more memory and CPU, or run `make seed ROWS=2000000`.
- **Start from scratch:** `make reset && make up && make seed`.

## Repository layout

```
transfers/ ledger/ provider/   the three services
loadgen/                       open-loop traffic generator
alertrelay/                    Grafana webhook → readable ntfy notification (04-alerts)
internal/telemetry/            all the OpenTelemetry setup (grows branch by branch)
grafana/                       datasources, dashboard, alert rules: provisioned from code
scripts/                       make targets: check, stage, prebuild, calibrate, verify, offline kit
tools/                         dashboard generator, Playwright evidence tooling
docs/                          PLAN, RUNBOOK (speaker), BUGS, DECISIONS
colab/                         the Colab notebook and the helper it uses (no Docker needed)
evidence/                      (not in git) make verify writes screenshots and logs here (SUMMARY.md)
```
