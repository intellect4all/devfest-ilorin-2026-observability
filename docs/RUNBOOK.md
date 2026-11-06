# Runbook: DevFest Ilorin 2026

**Observability for Distributed Systems: Metrics, Traces, and Logs That Actually Find Bugs**
Abdul-Jemeel Odewole · 40 minutes, 12:00–12:40 (to be confirmed) · your MacBook, projector, venue Wi-Fi that may fail.

Every demo is `make stage STAGE=<branch>` (about 3–4 seconds, no compiling) and then one or two `make`
commands. Bug C, the lock held across a network call, is **the incident**: you show it three times,
once through each signal, and a fourth time when it pages the room.

Screenshots of what every step should look like are in `evidence/latest/` (the newest verification run).
Every check and its numbers are in `evidence/SUMMARY.md`.

---

## 1. Pre-event checklist

### One week before

- [ ] Send attendees the setup note: "Install Docker Desktop and the ntfy app; optionally clone
  `<repo>` and run the README quick start." Under 10 minutes on good internet.
- [ ] On **your MacBook** (not the homelab: it's x86, your Mac is ARM):
  ```bash
  cp .env.example .env            # set NTFY_TOPIC to something unguessable, e.g. devfest-ilorin-<6 random chars>
  make up && make seed && make check
  make prebuild                   # images for every branch, about 5 min
  make calibrate                  # about 5 min, see "Calibration" below; re-seed if it says so
  make offline-kit                # offline-kit/ (about 2.5 GB): copy it to two USB sticks
  ```
- [ ] Timed rehearsal, with a stopwatch, using the run of show below. Note where you run long.
- [ ] Put the ntfy topic on a sticky note. You'll spell it out on stage.

### The day before

- [ ] `git pull` (if anything changed), then `make prebuild` and `make preflight`. Everything must be GO.
- [ ] Charge the laptop, phone and clicker. Pack a USB-C to HDMI adapter **and** a spare, both USB sticks, and the phone cable.
- [ ] Install the ntfy app on your phone and subscribe to the topic. `make stage STAGE=04-alerts && make alert-test` should make it buzz.
- [ ] Turn on your phone hotspot once to check it works with the laptop.

### The morning of

- [ ] `make preflight`: about 6 minutes. Read the GO / NO-GO summary at the bottom. Any NO-GO: see the recovery playbook.
- [ ] Leave the stack running. Finish with:
  ```bash
  make stage STAGE=01-metrics && make bugs-off && make load
  ```
- [ ] Browser tabs, in order:
  1. Grafana *Payments — RED & USE* (`http://localhost:3000/d/payments`), last 10 minutes, refresh 5 s.
  2. Grafana Explore (empty).
  3. `https://ntfy.sh/<topic>`, the web view of the topic, as a backup if phones are slow.

### At the venue (15 minutes before)

- [ ] Projector: mirror the display, 1920×1080. Check the dashboard is readable from the back row.
- [ ] Terminal: font size **24 pt** or larger, dark theme, two panes: commands on the left, `make load-logs` on the right (optional).
- [ ] Browser zoom **125%** for Grafana. Turn on kiosk mode for the dashboard with `?kiosk` if the top bar distracts.
- [ ] **Do Not Disturb** on the Mac (Focus). Quit Slack, Mail and anything else that notifies. Keep your phone's ntfy notifications *on*.
- [ ] Wi-Fi: open `https://ntfy.sh` in the browser. If it fails, switch to the phone hotspot; if that fails, go to Plan B (offline ntfy).
- [ ] Run `make check`: all ticks. Run `make bugs`: all off.

---

## 2. Run of show (40 minutes)

Times assume a 12:00 start. **Hunt rule:** at the end of Demo 3, if it is later than **12:25**, skip the hunt (point at `HUNT.md` as take-home) and go straight to slide 16.

| Start | Min | Slides | Segment | Exact commands | On screen | What I say (short) |
|---|---|---|---|---|---|---|
| 12:00 | 2 | 1 | Opening poll | none | Cover slide | Three questions, hands up (script below) |
| 12:02 | 5 | 2–5 | Why this matters | none | Slides | "Distributed everything means more places to break." |
| 12:07 | 3 | 6–9 | What to measure: RED/USE, rules of thumb | none | Slides | "Is something wrong? Where? Why? Three questions, three signals." |
| 12:10 | 4 | after 9 | **DEMO 1: metrics** | `make bug-c-on` … `make bug-c-off` | Dashboard green, then red | "We know something is wrong. We don't know where." |
| 12:14 | 2 | 10–12 | Traces: anatomy, rules of thumb | `make stage STAGE=02-traces` during slide 10 | Slides | "A trace is the request's receipt." |
| 12:16 | 4 | after 12 | **DEMO 2: traces** | `make bug-c-on` … `make bug-c-off` | Exemplar → trace waterfall | "We know where. We don't know why." |
| 12:20 | 2 | 13–15 | Logs: structured, what and when to log | `make stage STAGE=03-logs` during slide 13 | Slides | "Logs tell you why, if they carry the trace ID." |
| 12:22 | 4 | after 15 | **DEMO 3: logs** | `make bug-c-on` … `make bug-c-off` | Trace + Related logs, the WARN line | "It waited 9 seconds because transfer trf_… held the lock during payout." |
| 12:26 | 8 | (after 15) | **Audience hunt** (optional) | `make stage STAGE=hunt && make mystery`, `make hint` every 2 min, `make reveal` | Dashboard, attendees drive | Rules from `HUNT.md` |
| 12:34 | 1 | 16–17 | Monitoring and alerting; symptoms, not causes | `make stage STAGE=04-alerts` during slide 16 | Slides | "Page on what users feel." |
| 12:35 | 3 | after 17 | **DEMO 4: alert to phones** | subscribe, then `make bug-c-on`, wait about 35 s, then `make bug-c-off`, wait about 50 s | ntfy on phones and on screen | "Get your phones out." (script below) |
| 12:38 | 1 | 18–19 | OpenTelemetry pipeline, auto-instrumentation | optional: `git diff main 01-metrics -- transfers/main.go` | Slide 18 / the diff | "One wrapper, `otelhttp`, gave us the whole RED row." |
| 12:39 | 1+ | 20 | Thank you, questions | `make bugs-off` | Slide 20 | |

**If you skip the hunt** you gain 8 minutes: do the slide 19 code peek (60 s) and take questions for the rest.

**Running 5 minutes over? Cut in this order:**
1. the hunt (−8 min);
2. the slide 19 code peek (−1 min);
3. in Demo 2, skip the exemplar and open the trace from *Slowest transfers* (−1 min);
4. in Demo 4, don't wait for the resolve. Say "it will tell you when it's over, too" and show `evidence/latest/04-alerts/*ntfy-resolved*.png` later (−1 min);
5. slides 2–5 to one minute each (−2 min).

---

## 3. Speaker script

Short, speakable lines. Don't read them; know them.

### Slide 1: opening poll (12:00)

- "Quick poll, hands up. Who has heard the word *observability* and could explain it to a friend?"
- "Keep them up if you debug with `console.log` or `print`." *(Smile.)* "Me too. That's where we start today."
- "Last one: who has been paged, or called at night, because something broke in production?"
- "By the end of this talk, your phone is going to get paged. By my laptop. Install the **ntfy** app now if you want to play along; I'll give you the topic later."

### Slides 2–5

- Slide 2: "Anything that can go wrong, will go wrong. In a distributed system it goes wrong somewhere you aren't looking."
- Slide 4: "One request here touches three services and a database. A `print` statement in one of them sees a quarter of the story."
- Slide 5: "Observability is being able to ask a new question of your system *without shipping new code*."

### Slides 6–9

- Slide 6: "Three questions. Is something wrong? Where? Why? Metrics answer the first, traces the second, logs the third. Same incident, three lenses. You'll see it three times."
- Slide 8: "RED for services: Rate, Errors, Duration. USE for resources: Utilisation, Saturation, Errors."
- Slide 9: "Histograms, not averages. An average hides the one customer waiting ten seconds. And keep labels low-cardinality: status codes yes, account IDs never."

### Demo 1: metrics (12:10)

- "This is a payment system: a transfers API, a ledger, and a payout provider, like the switch behind your bank app. There's traffic on it right now, 20 transfers a second."
- "Green. p99 under 100 milliseconds. No errors."
- *(Run `make bug-c-on`.)* "I've just deployed something. Watch."
- *(After 15–20 s.)* "p99 is red: two seconds, three, still climbing. Errors? Zero. CPU? Flat. Database pool? Flat. But look: **goroutines are climbing**. Requests are piling up inside the service, waiting for something."
- **Hand-off:** "**We know something is wrong. We don't know where.**"
- *(Run `make bug-c-off`.)*

### Slides 10–12 (stage `02-traces` while you talk)

- "A trace is one request's journey: a span per hop, a span per step, all sharing one trace ID that travels in a header called `traceparent`."
- Slide 12: "Instrument the edges first, every HTTP and database call. Then add manual spans for the business steps. Put IDs in attributes, and pass `ctx` everywhere: in Go, the context *is* the trace."

### Demo 2: traces (12:16)

- *(Run `make bug-c-on`, then wait 20 s.)* "Same incident, new eyes."
- "These little diamonds on the latency line are *exemplars*: real requests, sampled. Click a slow one." *(Click → View trace.)*
- "Here's one transfer: transfers, ledger, provider, the database. And this bar…" *(point at `account_lock.wait`)* "…is nine seconds of the request doing **nothing**. Waiting for a lock. Before any real work. The ledger call takes milliseconds. The payout takes 50 milliseconds."
- **Hand-off:** "**We know where. We don't know why.** Why is it waiting? Who has the lock?"
- *(Run `make bug-c-off`.)*

### Slides 13–15 (stage `03-logs` while you talk)

- Slide 14: "A structured log line: JSON, with a `trace_id`. That one field is what joins logs to traces."
- Slide 15: "INFO once per business outcome. WARN when something is degraded, *and say why, with numbers*. ERROR when a customer was let down. Not every function call."

### Demo 3: logs (12:22)

- *(Run `make bug-c-on`, then wait 20 s, then open a slow trace and the `account_lock.wait` span.)* "Same trace as before. Now: *Related logs*."
- *(Tick `wait_ms`, `held_by_transfer`, `held_by_step`, then `Log line`.)* "This request waited **9,800 milliseconds** for the account lock. The lock was held by **this other transfer**, and that transfer was in the middle of its **payout**: a network call to an external provider."
  - *(About 1 line in 5 says `held_by_step=post_entries` instead: the holder had not reached its payout yet. If you get one, say "this time it was still writing to the ledger; open the next one", and go back to the trace and pick the next slow one. Measured on n2: payout is about 55 ms of a 65–70 ms hold.)*
- "Somebody is holding a lock across a network call. Every payout from the settlement wallet waits for the previous one to come back from the provider. That's the bug."
- **Hand-off:** "Metrics told us something was wrong. Traces told us where. Logs told us why. **Each one answered a question the others couldn't.**"
- *(Run `make bug-c-off`.)* "And you'd never have found that with `print`. The line exists only because a trace ID connects it to the slow request."

### Audience hunt (optional, 12:26)

- "Your turn. I'm going to break production, and I won't tell you how. There are three possible bugs. Find out what, where and why, using only the signals."
- *(Run `make stage STAGE=hunt && make mystery`.)* "Shout when you've got all three. Point at the panel, the span and the log line."
- Hints: run `make hint` every 2 minutes (see the hunt guide). End with `make reveal`.

### Slides 16–17 (stage `04-alerts` while you talk)

- Slide 17: "Alert on symptoms, not causes. Users don't feel a missing index; they feel a slow transfer. Page on the slow transfer, then go and find the index with what we just did."

### Demo 4: the ntfy moment (12:35)

- "Phones out. Open the ntfy app. Tap **plus**, subscribe to a topic." Speak slowly, then spell it:
  > "The server is **ntfy dot sh**, that's n-t-f-y dot s-h. The topic is: **d-e-v-f-e-s-t, dash, i-l-o-r-i-n, dash** …" *(read the rest from your sticky note, one character at a time)*
- Also type it big in the terminal: `make subscribe` shows the server and topic.
- "Thumbs up when you've subscribed." *(Wait 30–45 s. Then run `make alert-test`.)* "Did that buzz? That's a test."
- *(Run `make bug-c-on`.)* "Now the real thing. Same bug. Nobody's watching the dashboard. About thirty seconds…"
- *(Phones buzz.)* "*Transfers p99 latency above 300 milliseconds*, with the value and the time. Not a wall of JSON: something you can act on at 3 a.m."
- *(Run `make bug-c-off`.)* "And when it's fixed, it tells you that too." *(About 50 s later.)* "Resolved."

### Slides 18–20

- Slide 18: "Everything you saw went service → OTLP → Collector → backends. Swap the backend and the code doesn't change."
- Slide 19 (optional, 60 s): *(`git diff main 01-metrics -- transfers/main.go`.)* "This one wrapper, `otelhttp.NewHandler`, gave us request rate, errors by status code and the latency histogram, for free."
- Slide 20: "The repo, with every branch and the bugs, is on the slide. `make up` and break it yourself. Thank you."

---

## 4. Demo cue cards

### Demo 1: metrics (`01-metrics`)

- **Starting state:** `make stage STAGE=01-metrics` done, `make load` running for at least 2 minutes, `make bugs` all off, dashboard tab open on the last 10 minutes.
- **Commands:** `make bug-c-on`, wait 20–30 s, talk, then `make bug-c-off`.
- **Click path:** none. Point at:
  1. the *p99 latency* tile (red);
  2. the *Latency* panel;
  3. the *Goroutines* panel in the USE row (climbing);
  4. the *5xx errors* tile (0%, still green);
  5. *CPU* and *DB pool* (flat).
- **Should look like:** healthy is `evidence/latest/01-metrics/*_dashboard-healthy.png`; bug on is `evidence/latest/01-metrics/*_bug-c-on_dashboard.png`.
- **Reset:** `make bug-c-off`, then start staging `02-traces` while you talk to slide 10.

### Demo 2: traces (`02-traces`)

- **Starting state:** `make stage STAGE=02-traces` (switch during slide 10), load running, bugs off.
- **Commands:** `make bug-c-on`, wait 20 s, then `make bug-c-off` at the end.
- **Click path:**
  1. Dashboard → *Latency* panel → hover a diamond high on the p99 line → **View trace** (opens Explore/Tempo).
  2. Fallback: scroll to *Slowest transfers* → click a trace ID.
  3. In the trace, click **account_lock.wait** to show it is almost the whole request.
  4. Point at the short `post_entries` and `payout` under it.
- **Should look like:** `evidence/latest/02-traces/*_dashboard-exemplars.png`, `*_exemplar-opens-trace.png` and `*_bug-c-trace.png`.
- **Reset:** `make bug-c-off`.

### Demo 3: logs (`03-logs`)

- **Starting state:** `make stage STAGE=03-logs` (switch during slide 13), load running, bugs off.
- **Commands:** `make bug-c-on`, wait 20 s, then `make bug-c-off` at the end.
- **Click path:**
  1. Open a slow trace, as in Demo 2.
  2. Click **account_lock.wait**.
  3. Click **Related logs**. Loki opens on the right with this trace's lines.
  4. In the Loki **Fields** list, tick **wait_ms**, **held_by_transfer** and **held_by_step**, then **Log line** (it only appears once another field is selected).
  5. Read the WARN line out loud.
  6. Optional: expand the line → **View trace** (log back to trace).
- **Should look like:** `evidence/latest/03-logs/*_bug-c-trace-to-logs.png`; log → trace is `*_log-line-to-trace.png`.
- **Reset:** `make bug-c-off`.

### Hunt (`hunt`)

- **Starting state:** `make stage STAGE=hunt`, load running, bugs off.
- **Commands:**
  1. `make mystery`. It picks A, B or C; for A it also starts a 15-minute traffic rush.
  2. `make hint` at 2, 4 and 6 minutes.
  3. `make reveal` at the end (it also turns the bug off and stops the rush).
- **Click path:** let the audience drive. The answer paths are in the hunt guide below.
- **Should look like:** `evidence/latest/hunt/*_hunt-*`, a three-screenshot trail per bug.
- **Reset:** `make reveal`, then `make bugs-off`.

### Demo 4: alerts (`04-alerts`)

- **Starting state:** `make stage STAGE=04-alerts` (switch during slide 16), load running, bugs off, your phone subscribed, ntfy web tab open.
- **Commands:**
  1. `make subscribe` (topic on screen).
  2. `make alert-test`.
  3. `make bug-c-on`, then wait about 35 s for the buzz.
  4. `make bug-c-off`, then wait about 50 s for *Resolved*.
- **Click path:** the ntfy web tab as a backup view. In Grafana, *Alerting → Alert rules* shows *Transfers p99 latency above 300 ms* going Pending → Firing.
- **Should look like:** `evidence/latest/04-alerts/*_ntfy-firing.png` and `*_ntfy-resolved.png`.
- **Reset:** `make bugs-off`.

---

## 5. Recovery playbook (under 60 seconds each)

| Problem | Do this |
|---|---|
| **`make stage` fails** | Read the last line. *"uncommitted changes"*: `git stash`, then rerun. *"not built"*: switch to the previous stage (it's built) and carry on with slides. Any other error: `git checkout <branch> && make up` (about 20 s, no build if the image exists). |
| **Grafana shows "No data"** | Is traffic on? `make load`. Wait 15 s (panels use 30-second windows). Still nothing: `make check`; a ✗ on prometheus or collector means `docker compose restart lgtm` (about 20 s, data is kept). Meanwhile, show the evidence screenshot for that demo. |
| **A demo shows nothing after `make bug-c-on`** | The toggle reset on a stage switch. Run `make bugs` and turn it on again. |
| **ntfy doesn't deliver** | Show the ntfy web tab on the projector: it is the same message. Then check: `make alert-test`. If ntfy.sh is unreachable, go to Plan B. |
| **Wi-Fi is down** | Nothing in the demos needs the internet except ntfy.sh. Plan B: <br>1. phone hotspot on; <br>2. in `.env`, set `NTFY_URL=http://ntfy`; <br>3. run `make ntfy-local` and note the IP it prints; <br>4. run `make stage STAGE=04-alerts` again (the relay picks up the new URL); <br>5. phones on the hotspot subscribe to `http://<that ip>:2586/<topic>` (ntfy app → *Use another server*). |
| **Projector fails** | Keep talking from the slides on a second device, or turn the laptop around for the front rows. The demos are pictures in `evidence/latest/`; describe them. |
| **Docker itself is broken** | Restart Docker Desktop (about 60 s), then `make up`. If images are gone: `docker load -i offline-kit/images.tar` (about 2 min) from the USB stick. |
| **Running 5 minutes over** | See the cut list under the run of show. |

---

## 6. Hunt guide

**Rules** (on screen from `HUNT.md`):
1. Signals only.
2. Say what, where and why.
3. Show your evidence.
4. Hints every two minutes.

**Hint ladder** (`make hint` prints each one):

| Time | Hint |
|---|---|
| 2:00 | 1. Which RED tile went red? Which USE panel moved with it, and which did not? |
| 4:00 | 2. Click a diamond (exemplar) on the p99 line, or a trace ID in *Slowest transfers*. |
| 6:00 | 3. In the trace, find the span with the most time of its own: wide, with nothing under it. |
| 7:00 | 4. On that span, open *Related logs*. Read the WARN line; every number in it matters. |
| 8:00 | `make reveal` |

**Answer paths:**

- **Bug A** (comes with a traffic rush):
  - Metrics: request rate jumps, then p99 explodes; transfers CPU and *Memory allocated / s* climb with it.
  - Trace: `duplicate_check` is wide and has no children.
  - Log: `slow duplicate check` with `entries_scanned` around 80,000+.
  - Why: the duplicate check walks every recent transfer under a lock, so it costs more the more traffic there is.
- **Bug B:**
  - Metrics: p99 goes up about six-fold while p50 does not move; *p99 by service* shows ledger rising; 5xx stays at 0.
  - Trace: `SELECT entries` (ledger) is the widest, and `db.query.text` shows the balance query.
  - Log: `slow query`, `duration_ms` in the hundreds.
  - Why: a sequential scan, because the index is gone. Run `make explain` to show the Seq Scan.
- **Bug C:**
  - Metrics: p99 climbs and goroutines climb; errors, CPU and pool stay flat.
  - Trace: `account_lock.wait` is the whole request.
  - Log: `waited for account lock`, `held_by_step=payout` (sometimes `post_entries`: the lock is held for the whole transfer).
  - Why: a lock held across a network call.

---

## 7. Calibration notes

Measured on **n2** (homelab: x86_64, 8 cores, Docker 29.5, shared with another team's load test) with
`make calibrate` (client-side latency, median of 5-second windows):

| Setting | Value | Where | Why |
|---|---|---|---|
| `LOAD_RATE` | 20 req/s | `.env` / Makefile | readable graphs, room for bugs to stand out |
| `LOAD_HOT` | 0.9 | `.env` | 90% of payouts leave the settlement wallet, so bug C contends and bug B touches only the tail |
| Provider latency | 35 ms + up to 35 ms of jitter | `compose.yaml` `PROVIDER_LATENCY_MS` | sets bug C's lock hold time (ρ ≈ 1.06 on the hot account) |
| `ROWS` | 7,000,000 | `make seed ROWS=` | bug B p99 about 630 ms; p50 unchanged |
| `HISTORY` | 80,000 | `make seed HISTORY=` | bug A: p99 about 175 ms at steady load, over 10 s with a surge |
| `SURGE_RATE` / `SURGE_SECONDS` | +80 req/s for 90 s | `.env` | saturates bug A's lock |
| Alert | p99 > 300 ms for 20 s, evaluated every 10 s | `grafana/provisioning/alerting/alerts.yaml` | about 35 s from bug-on to phone; about 50 s to resolve |
| Postgres | 3 CPUs, 1 GB shared buffers | `compose.yaml` | stable bug B even with other load on the host |

Healthy: p50 about 60 ms, p99 under 100 ms. Bug C: p99 over 1 s within 30 s and still rising. Bug B:
p99 about 0.6 s. Bug A plus surge: p99 at the 10 s client timeout.

### Re-tune on your laptop

1. `make reset && make up && make seed && make calibrate` (about 6 minutes). It prints one line per phase and advice.
2. **Bug B too mild** (under 450 ms): `make seed ROWS=<15% more>`. **Too strong**, or it says 10000 ms: `ROWS=<10% fewer>`. It is a cliff near saturation, so take small steps. Re-run `PHASES=B make calibrate`.
3. **Bug A too mild with surge:** `make seed HISTORY=<1.5×>`. **Hurts already at steady load:** `HISTORY=<⅔×>`. Note that the list grows by about 72k per hour of `make load`, so seed on the morning of the talk.
4. **Bug C** barely depends on the machine (it is the provider's sleep). If it is too slow to rise, lower `PROVIDER_LATENCY_MS` to 30.
5. Record your numbers here, and commit.

A faster laptop (Apple M-series) usually scans faster, so expect to need *more* `ROWS` for bug B and
more `HISTORY` for bug A than n2.
