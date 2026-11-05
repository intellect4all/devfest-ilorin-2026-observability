# Decisions

When I wasn't sure, I picked the simpler option and wrote the reason down here. Numbers marked
"measured on n2" come from the homelab test host (x86_64, 8 cores, 31 GB, Docker 29.5, shared with
other workloads). Re-measure them on the presenting laptop with `make calibrate`.

## Environment

**Test runs happen on n2, not the MacBook.** The MacBook's Docker engine was not answering when the
build started, and the presenter asked for n2 to be used. Everything runs from a copy of this repo on
n2 under the compose project `devfest26`, with `devfest26/*` image names. Nothing daemon-wide (no
prune) is ever run, because n2 is shared.

Consequences:
- n2 is amd64; the presenter's laptop is arm64. Images built on n2 are the wrong architecture for the
  talk. `make prebuild` and `make offline-kit` must be run once on the laptop itself.
- Another team ran load tests on n2 during this work (load average up to 16), which affected the
  calibration of bug B (see below).
- Port 9090 is taken on n2, so `.env` there sets `PROM_PORT=9091`. All host ports can be changed in `.env`.

**Playwright runs inside `mcr.microsoft.com/playwright:v1.63.0-noble`.** Playwright's own Chromium
download failed on n2 (Fedora 43, "Download failure, code=1"), and Playwright's `install-deps` only
supports apt. The npm package is still installed in the repo (`tools/evidence/node_modules`, with a
committed lockfile), and the browsers come from the pinned image. Nothing is installed globally. The
same image works on a Mac.

## Stack

**`grafana/otel-lgtm:0.35.0`, all in one.** It contains the OpenTelemetry Collector, Prometheus, Tempo,
Loki and Grafana, which matches slide 18 in a single container. I checked it in a spike before
building anything:
- `otelhttp` sets `http_route` from the Go 1.22 mux pattern.
- The bundled Prometheus runs with `--enable-feature=exemplar-storage`, and `query_exemplars`
  returned `trace_id`.
- OTLP logs arrive in Loki with `trace_id` as structured metadata.
- Grafana 13.2.2 can reload provisioning through its API.

No separate containers were needed.

**Version pins.** Every pin is at least a few weeks old (versions and dates are in `PLAN.md`):
- OTel Go v1.46.0 / contrib v0.71.0 (Aug 2026 release train)
- pgx v5.11.0 (v5.12.0 was five days old)
- otel-lgtm 0.35.0 (2 Oct)
- Postgres 17.10
- ntfy v2.29.0
- docker 29.8.1-dind for the offline test

**`GF_PLUGINS_PREINSTALL_DISABLED=true`.** The image preinstalls `grafana-llm-app` from the internet
at start-up. Without this setting, the offline kit would stall or fail.

**Grafana provisioning mounts the whole `grafana/` folder** and sets `GF_PATHS_PROVISIONING` to it.
The first design mounted each sub-folder separately. When the stage switch to `04-alerts` ran, git
deleted and recreated `grafana/provisioning/alerting`, and the container kept the old, deleted
directory, so the alert rules never loaded. Mounting the parent folder avoids this.

**Two listeners per service.** The admin listener (bug toggles) is a separate `http.Server`, not a
route that is filtered out. It is never wrapped in instrumentation, so a toggle cannot leak into
metrics, traces or logs even by mistake. It is published only on `127.0.0.1`.

**One image per branch containing every binary.** That makes one build per branch and seven app
images in the offline kit, instead of 28. Compose selects the binary with `command:`.

**Every request is traced (no sampling).** That's right for a demo; the code comment says production
would sample.

**DB spans come from a 60-line pgx `QueryTracer` in `internal/telemetry`, not a library.**
- It controls the attribute names (`db.system.name`, `db.query.text`, `db.operation.name`,
  `db.collection.name`).
- It refuses to start a span when there is no parent, so start-up queries (migrations, the history
  load) cannot create orphan traces.

**Trace lookup by transfer ID uses `=~`.** In this Tempo build,
`{span.transfer.id="trf_…"}` returns nothing, while `{span.transfer.id=~"trf_…"}`, which is anchored
so it is effectively the same, finds the trace. Other attributes (`account.from`) match with `=`. The
runbook uses the regex form when searching by ID.

**CPU metric.** The runtime instrumentation has goroutines, memory and GC, but no CPU, so
`internal/telemetry` adds a `process.cpu.time` observable counter from `getrusage(2)`.

**Logs go to both stdout (JSON) and OTLP**, through `slog.NewMultiHandler` (new in Go 1.26). It's a
one-line change on slide 14, and `docker compose logs` stays useful when Loki is not.

**The alert relay is a separate small service.** Grafana's webhook contact point sends raw JSON. A
50-line relay turns it into an ntfy message with a title, a one-line summary carrying the value, a
priority (5 when firing, 3 when resolved) and an icon. The relay is deliberately not instrumented: it
is plumbing, and its traces would only clutter Tempo. Grafana also has templated webhook payloads, but
the relay is easier to test (`alertrelay/main_test.go`) and works identically against ntfy.sh and the
offline container.

**Alert timings are tuned for the stage.** Rules are evaluated every 10 s with `for: 20s`. The policy
uses `group_wait: 5s` and `group_interval: 10s`; Grafana's defaults (30 s and 5 min) would delay the
resolve notification by minutes. Measured on n2: about 35 s from bug-on to the phone, and about 50 s
from bug-off to the resolve.

## The bugs and the traffic

**Traffic is skewed: 90% of transfers leave one settlement wallet (`acct-00001`).** That is how payout
platforms really look. It is also what makes a per-account lock (bug C) contend. With uniform traffic
over 1,000 accounts, bug C showed nothing.

**The settlement wallet is pre-funded, so its payouts skip the balance check** (`PREFUNDED_ACCOUNTS`,
which fails closed: no account skips the check unless it is configured). Only about 2 req/s run the
balance query. I made this change after measuring bug B:
- When every transfer ran the balance query, bug B was a cliff. 1.2M rows gave p99 190 ms, while
  1.6M rows saturated Postgres and p99 reached 10 s.
- With 30% of transfers checking, it was bistable: 2.1M rows collapsed, 2.2M did not.
- With 10% checking, a slow query gives a stable tail instead.

This also gives bug B its own fingerprint: p99 rises while p50 barely moves.

**Money movement is detached from the client's cancellation** (`context.WithoutCancel` once the money
starts moving). Under load, client timeouts were leaving ledger entries without a saved transfer.

**Bug A compares keys case-insensitively by lower-casing every stored key on every lookup.** A plain
string comparison over 300k records took about 1 ms, which is invisible on a dashboard. Lower-casing
the stored keys, so retries still match after a proxy lower-cases the header, is a believable mistake
and costs about 0.12 µs plus one allocation per record. Measured on n2: 100k records take 12 ms per
request and 300k take 38 ms, while the map takes 0.6 µs. The allocation churn also shows on the USE
row.

**Bug A's history is warmed from the database at start-up.** Without that, a stage switch (which
restarts the service) would empty the list, and the bug would vanish for minutes.

**Bug B persists in Postgres; bugs A and C are in memory.** A stage switch therefore resets A and C to
off but leaves B as it was. `make bugs-off` is part of every reset step in the runbook.

**Bug B's toggle drops every index on `entries(account_id …)`.** On `solution`, the ledger ensures a
covering index at start-up. If that index survived a switch back to an earlier branch, bug B would
silently stop working there. Dropping all account indexes prevents that. On `solution`, the toggle
drops only the original index, replaying the original incident, and the fix holds.

**The fix for bug B is an index the service ensures on every start**, not a one-off numbered
migration. The incident was "the index got lost", so the fix makes it impossible to lose quietly. The
index also covers `amount`, so the balance query becomes an index-only scan.

**Bug C is the main incident.** I chose it from the measured fingerprints:

| Signal | A (needs a surge) | B | C |
|---|---|---|---|
| Metrics say *something* | p99 up only under surge, CPU and allocations up | p99 up, p50 flat, ledger pool busy | p99 red within 20 s; goroutines climb in steps |
| Traces say *where* | wide `duplicate_check`, no children | wide `SELECT entries` | `account_lock.wait` is almost the whole request |
| Logs say *why* | "slow duplicate check", entries_scanned 80k+ | "slow query", duration_ms | "waited for account lock", wait_ms, **held_by_transfer, held_by_step=payout** |
| Alert | only during a surge | borderline (p99 about 0.6 s) | fires every time, in about 35 s |

C's log line answers "why" in a way the other two signals cannot: it names the other transfer and
what it was doing (`payout`) while holding the lock. It needs no extra command (A needs `make surge`),
and it fires the latency alert reliably, so the same incident can carry all four demos.

**Bug C saturates on purpose (ρ ≈ 1.06 on the hot account).** The lock hold time is mostly the
provider's simulated latency (35 ms plus up to 35 ms of jitter), which does not depend on CPU speed.
So C behaves the same on the laptop as on n2. Latency grows the longer it stays on, by about 1 s every
15 s, and it drains within seconds of `make bug-c-off`.

**Postgres gets 3 CPUs and 1 GB of shared buffers.**
- Postgres had 2 CPUs in the first verification run. Bug B collapsed (p99 10 s) while the other
  team's load test was running.
- The table is 1.1 GB at 7M rows. Scans are only predictable when most of it stays cached.
- The fixed CPU budget keeps bug B's cost similar from one machine to the next.

## Stage switching and evidence

**`make stage` recreates only the app containers.** Postgres and otel-lgtm keep running with all their
data; Grafana re-reads its provisioning through the reload API. Measured on n2: 3.0–3.6 s per switch.

**`make verify` works in a scratch clone and runs from a copy of the scripts.** Bash reads scripts as
it goes, so checking out a branch that changes `verify.sh` mid-run would corrupt the run. The scratch
clone keeps branch switches away from the working tree.

**Evidence is written on the test host and committed at the end.** During development the evidence
lives outside the repo (`~/devfest26-evidence` on n2). This rule came from a mistake: an early
`git add -A` swept a local copy of the evidence into the `solution` commit, and I had to remove it.

**Screenshots use a tall viewport, not `fullPage`.** A full-page capture resizes the page, Grafana
re-lays out, and the canvases are blank at the moment of capture. The first evidence run looked fine
to the automated audit but had empty graphs. The audit now also checks every graph canvas for paint.

**The automated audit is necessary but not sufficient.** Every run's screenshots are reviewed by eye.
The review is appended to `evidence/LOG.md` as its own entry, and it overturns any PASS the picture
does not support.

**The chain of branches is maintained by `scripts/rebase-chain.sh`.** Shared tooling lives on `main`.
After any change, every later branch is replayed with `git rebase --onto`, and the dashboard JSON is
regenerated on each branch from `tools/dashboard.py`. The JSON is a generated file, so a conflict
there is settled by regenerating it.

## Things I did not do

- **No multi-arch images.** The laptop builds its own with `make prebuild`. A multi-platform
  `docker save` would double the USB kit.
- **No ledger-side overdraft constraint.** The pre-funded rule fails closed, and the demo never
  exhausts balances. A real ledger would enforce it in the database.
- **No authentication on the local ntfy server.** It only runs on the presenter's hotspot during the
  talk. The public ntfy.sh topic is unguessable and lives in `.env`.
