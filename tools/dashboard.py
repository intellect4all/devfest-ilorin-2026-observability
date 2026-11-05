#!/usr/bin/env python3
"""Generates grafana/dashboards/payments.json, the "Payments — RED & USE" dashboard.

    python3 tools/dashboard.py [--traces] [--alerts] > grafana/dashboards/payments.json

--traces adds a "slowest traces" table (02-traces on), --alerts an alert-state
panel (04-alerts on). Edit this file, not the JSON.
"""
import json
import sys

PROM = {"type": "prometheus", "uid": "prometheus"}
TEMPO = {"type": "tempo", "uid": "tempo"}
W = "30s"  # rate window: short so the demo reacts within seconds
HTTP = "http_server_request_duration_seconds"
SLO = 0.3  # seconds; the alert threshold in 04-alerts

panels = []
y = 0


def target(expr, legend="", ref="A", exemplar=False, instant=False):
    t = {"datasource": PROM, "expr": expr, "legendFormat": legend, "refId": ref, "range": not instant, "instant": instant}
    if exemplar:
        t["exemplar"] = True
    return t


def thresholds(*steps):
    """steps: (value, colour) pairs after the base colour, e.g. (0.3, 'red')."""
    s = [{"color": "green", "value": None}] + [{"color": c, "value": v} for v, c in steps]
    return {"mode": "absolute", "steps": s}


def add(panel, w, h, x):
    panel["id"] = len(panels) + 1
    panel["gridPos"] = {"x": x, "y": y, "w": w, "h": h}
    panels.append(panel)


def row(title):
    global y
    add({"type": "row", "title": title, "collapsed": False, "panels": []}, 24, 1, 0)
    y += 1


def stat(title, expr, unit, steps, desc="", decimals=None):
    p = {
        "type": "stat",
        "title": title,
        "description": desc,
        "datasource": PROM,
        "targets": [target(expr, instant=False)],
        "fieldConfig": {"defaults": {"unit": unit, "thresholds": thresholds(*steps), "color": {"mode": "thresholds"}}, "overrides": []},
        "options": {
            "colorMode": "background",
            "graphMode": "area",
            "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "textMode": "value",
            "justifyMode": "center",
        },
    }
    if decimals is not None:
        p["fieldConfig"]["defaults"]["decimals"] = decimals
    return p


def ts(title, targets, unit, desc="", steps=(), stack=False, min0=True, legend_calcs=("lastNotNull", "max")):
    custom = {"lineWidth": 2, "fillOpacity": 10, "showPoints": "never", "spanNulls": True}
    if steps:
        custom["thresholdsStyle"] = {"mode": "dashed"}
    if stack:
        custom["stacking"] = {"mode": "normal", "group": "A"}
        custom["fillOpacity"] = 40
    defaults = {"unit": unit, "custom": custom, "color": {"mode": "palette-classic"}}
    if steps:
        defaults["thresholds"] = thresholds(*steps)
    if min0:
        defaults["min"] = 0
    return {
        "type": "timeseries",
        "title": title,
        "description": desc,
        "datasource": PROM,
        "targets": targets,
        "fieldConfig": {"defaults": defaults, "overrides": []},
        "options": {
            "legend": {"displayMode": "table", "placement": "right", "calcs": list(legend_calcs), "showLegend": True},
            "tooltip": {"mode": "multi", "sort": "desc"},
        },
    }


def quantile(q, selector, by=""):
    by_clause = f"le, {by}" if by else "le"
    return f"histogram_quantile({q}, sum by ({by_clause}) (rate({HTTP}_bucket{{{selector}}}[{W}])))"


def build(traces=False, alerts=False):
    global y
    tr = 'job="transfers"'
    rate_all = f"sum(rate({HTTP}_count{{{tr}}}[{W}]))"
    err_pct = f'100 * (sum(rate({HTTP}_count{{{tr}, http_response_status_code=~"5.."}}[{W}])) or vector(0)) / {rate_all}'

    # --- Headline: is something wrong? -------------------------------------------------
    row("Is something wrong?  RED for the transfers API")
    add(stat("Requests / s", rate_all, "reqps", (), "Rate: completed requests per second.", 1), 6, 4, 0)
    add(stat("p99 latency", quantile(0.99, tr), "s", ((SLO, "red"),), f"Duration: 99th percentile. Red above the {int(SLO*1000)} ms SLO.", 2), 6, 4, 6)
    add(stat("5xx errors", err_pct, "percent", ((1, "orange"), (5, "red")), "Errors: share of requests answered with 5xx.", 1), 6, 4, 12)
    add(
        stat(
            "Goroutines (transfers)",
            'sum(go_goroutine_count{job="transfers"})',
            "short",
            ((200, "orange"), (1000, "red")),
            "Saturation: requests waiting inside the process.",
            0,
        ),
        6,
        4,
        18,
    )
    y += 4

    add(
        ts(
            "Latency: p50 / p95 / p99" + ("  (diamonds are exemplars: click one for its trace)" if traces else ""),
            [
                target(quantile(0.50, tr), "p50", "A"),
                target(quantile(0.95, tr), "p95", "B"),
                target(quantile(0.99, tr), "p99", "C", exemplar=True),
            ],
            "s",
            "Histogram quantiles, never averages. The dashed line is the SLO.",
            steps=((SLO, "red"),),
        ),
        12,
        9,
        0,
    )
    add(
        ts(
            "Rate by HTTP status",
            [target(f"sum by (http_response_status_code) (rate({HTTP}_count{{{tr}}}[{W}]))", "{{http_response_status_code}}")],
            "reqps",
            "Throughput the service actually completes, split by status code.",
            stack=True,
        ),
        12,
        9,
        12,
    )
    y += 9

    # --- Where is it slow? -------------------------------------------------------------
    row("Where?  latency per service and per step")
    add(
        ts(
            "p99 by service (server side)",
            [target(quantile(0.99, "", "job"), "{{job}}")],
            "s",
            "Each service's own view of how long it takes to answer.",
            steps=((SLO, "red"),),
        ),
        8,
        8,
        0,
    )
    add(
        ts(
            "Payout call duration, as transfers sees it",
            [
                target("histogram_quantile(0.50, sum by (le) (rate(payout_duration_seconds_bucket[%s])))" % W, "p50", "A"),
                target("histogram_quantile(0.99, sum by (le) (rate(payout_duration_seconds_bucket[%s])))" % W, "p99", "B"),
            ],
            "s",
            "Custom histogram around the provider call (payout.duration).",
        ),
        8,
        8,
        8,
    )
    add(
        ts(
            "Transfers by outcome",
            [target(f"sum by (status) (rate(transfers_processed_total[{W}]))", "{{status}}")],
            "reqps",
            "Business metric: completed, failed, insufficient_funds, replayed.",
            stack=True,
        ),
        8,
        8,
        16,
    )
    y += 8

    # --- USE: is anything saturated? ---------------------------------------------------
    row("USE: is a resource saturated?")
    add(
        ts("Goroutines", [target("sum by (job) (go_goroutine_count)", "{{job}}")], "short", "Goroutines climb when requests wait (locks, queues)."),
        6,
        8,
        0,
    )
    add(
        ts(
            "CPU (cores used)",
            [target(f"sum by (job) (rate(process_cpu_time_seconds_total[{W}]))", "{{job}}")],
            "short",
            "Utilisation: 1.0 = one full core.",
        ),
        6,
        8,
        6,
    )
    add(
        ts(
            "DB pool: connections in use",
            [
                target(
                    'sum by (db_client_connection_pool_name) (db_client_connection_count{db_client_connection_state="used"})',
                    "{{db_client_connection_pool_name}} used",
                    "A",
                ),
                target("sum by (db_client_connection_pool_name) (db_client_connection_max)", "{{db_client_connection_pool_name}} max", "B"),
            ],
            "short",
            "Saturation of the database pools: used = max means queries queue.",
        ),
        6,
        8,
        12,
    )
    add(
        ts(
            "Memory allocated / s",
            [target(f"sum by (job) (rate(go_memory_allocated_bytes_total[{W}]))", "{{job}}")],
            "Bps",
            "Allocation rate drives GC work.",
        ),
        6,
        8,
        18,
    )
    y += 8

    if traces:
        row("Slowest recent transfers (Tempo)")
        add(
            {
                "type": "table",
                "title": "Slowest transfers in the last 5 minutes: click a trace ID",
                "datasource": TEMPO,
                "targets": [
                    {
                        "datasource": TEMPO,
                        "refId": "A",
                        "queryType": "traceql",
                        "query": '{resource.service.name="transfers" && name="POST /transfers"} | duration > 250ms',
                        "limit": 10,
                        "tableType": "traces",
                    }
                ],
                "fieldConfig": {"defaults": {}, "overrides": []},
                "options": {"showHeader": True, "sortBy": [{"displayName": "Duration", "desc": True}]},
            },
            24,
            8,
            0,
        )
        y += 8

    if alerts:
        row("Alerts")
        add(
            {
                "type": "alertlist",
                "title": "Alert rules (symptoms, not causes)",
                "options": {
                    "viewMode": "list",
                    "groupMode": "default",
                    "showInactiveAlerts": True,
                    "stateFilter": {"firing": True, "pending": True, "normal": True, "noData": True, "error": True},
                    "sortOrder": 1,
                    "maxItems": 10,
                    "alertInstanceLabelFilter": "",
                    "folder": None,
                },
            },
            24,
            5,
            0,
        )
        y += 5

    return {
        "uid": "payments",
        "title": "Payments — RED & USE",
        "tags": ["devfest"],
        "timezone": "browser",
        "editable": False,
        "refresh": "5s",
        "time": {"from": "now-10m", "to": "now"},
        "timepicker": {"refresh_intervals": ["5s", "10s", "30s", "1m"]},
        "schemaVersion": 39,
        "graphTooltip": 1,
        "panels": panels,
    }


if __name__ == "__main__":
    print(json.dumps(build(traces="--traces" in sys.argv, alerts="--alerts" in sys.argv), indent=2))
