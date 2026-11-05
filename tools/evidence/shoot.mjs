// Evidence screenshots. Run through scripts/shoot.sh, which starts this in
// the pinned Playwright image with host networking.
//
//   node shoot.mjs dashboard <uid> <out.png> [from=now-15m]
//   node shoot.mjs trace     <traceId> <out.png> [spanName] [logs] [logLineRegex]
//   node shoot.mjs loki      <logql> <out.png> [from=now-15m] [expandLineRegex] [follow]
//   node shoot.mjs page      <url> <out.png> [waitText]
//   node shoot.mjs text      <file.txt> <out.png> [title]   (terminal output as a PNG)
//
// Prints one JSON line describing what was captured. A screenshot whose page
// shows "No data", an error, a login page or a spinner is flagged with
// "problems" and the process exits 2, so nobody marks it PASS by accident.
import { chromium } from "playwright";

const GRAFANA = process.env.GRAFANA_URL || "http://127.0.0.1:3000";
const [cmd, ...args] = process.argv.slice(2);

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: 1600, height: 900 },
  deviceScaleFactor: 1,
  timezoneId: "Africa/Lagos",
  locale: "en-GB",
});
// Keep the side menu out of the way, as on a projector.
await ctx.addInitScript(() => localStorage.setItem("grafana.navigation.docked", "false"));
const page = await ctx.newPage();
const result = { cmd, args, problems: [] };

function explore(panes) {
  return `${GRAFANA}/explore?schemaVersion=1&orgId=1&panes=${encodeURIComponent(JSON.stringify(panes))}`;
}

async function settle(ms = 1500) {
  await page.waitForLoadState("networkidle", { timeout: 30000 }).catch(() => {});
  // Grafana shows loading bars while queries run; wait for them to go.
  await page
    .waitForFunction(() => !document.querySelector('[aria-label="Panel loading bar"], [data-testid="Spinner"], .fa-spinner'), null, { timeout: 30000 })
    .catch(() => {});
  await page.waitForTimeout(ms);
}

async function audit() {
  const text = await page.evaluate(() => document.body.innerText);
  const noData = (text.match(/\bNo data\b/g) || []).length;
  if (noData) result.problems.push(`${noData}x "No data"`);
  if (/Welcome to Grafana|Log in to Grafana|Email or username/.test(text)) result.problems.push("login page");
  for (const m of text.matchAll(/(Bad Gateway|Internal Server Error|Query error[^\n]*|Failed to [^\n]{0,80})/g))
    result.problems.push(m[1]);
  result.title = await page.title();
}

// graphsDrawn flags time-series panels whose canvas is still blank, the
// failure that "No data" text alone does not catch.
async function graphsDrawn() {
  const blank = await page.evaluate(() => {
    let n = 0;
    for (const c of document.querySelectorAll("canvas")) {
      if (c.width < 50 || c.height < 50) continue;
      const ctx = c.getContext("2d");
      if (!ctx) continue;
      const d = ctx.getImageData(0, 0, c.width, c.height).data;
      let painted = 0;
      for (let i = 3; i < d.length; i += 4 * 97) if (d[i] > 0) painted++;
      if (painted === 0) n++;
    }
    return n;
  });
  if (blank) result.problems.push(`${blank} blank graph canvas(es)`);
}

async function clickText(re, opts = {}) {
  const el = page.getByText(re, opts).first();
  await el.waitFor({ timeout: 20000 });
  await el.click();
}

try {
  if (cmd === "dashboard") {
    const [uid, out, from = "now-15m", height = "1900"] = args;
    // A tall viewport, not fullPage: a full-page capture resizes the page,
    // Grafana re-lays out and clears its canvases mid-capture (empty graphs).
    await page.setViewportSize({ width: 1600, height: Number(height) });
    await page.goto(`${GRAFANA}/d/${uid}?orgId=1&kiosk&from=${from}&to=now&refresh=`, { waitUntil: "domcontentloaded" });
    await settle(4000);
    await audit();
    await graphsDrawn();
    await page.screenshot({ path: out });
  } else if (cmd === "trace") {
    const [traceId, out, spanName, logs, logLine] = args;
    await page.setViewportSize(logs ? { width: 1800, height: 1100 } : { width: 1600, height: 1100 });
    await page.goto(
      explore({
        a: {
          datasource: "tempo",
          queries: [{ refId: "A", datasource: { type: "tempo", uid: "tempo" }, queryType: "traceql", query: traceId, limit: 20 }],
          range: { from: "now-1h", to: "now" },
        },
      }),
      { waitUntil: "domcontentloaded" },
    );
    await settle(2000);
    await page.getByText(/^Trace ID/).first().waitFor({ timeout: 30000 });
    if (spanName) {
      // Expand the named span to show its attributes.
      const re = new RegExp(`^${spanName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`);
      const row = page.getByText(re).first();
      await row.scrollIntoViewIfNeeded();
      await row.click();
      await settle(1000);
      result.expanded = spanName;
    }
    if (logs) {
      // Grafana 13 calls "logs for this span" Related logs; it opens Loki in a split pane.
      await page.getByRole("button", { name: /Related logs|Logs for this span/i }).first().click();
      await page.getByText(/trace_id/).first().waitFor({ timeout: 30000 }).catch(() => {});
      await settle(3000);
      result.logs = true;
      if (logLine) {
        // Show the numbers that explain it next to each line: tick those
        // fields in the Loki "Fields" sidebar, as the presenter does on stage.
        await page.getByText(new RegExp(logLine)).last().waitFor({ timeout: 20000 });
        const shown = [];
        for (const field of logLine.split("|")[1]?.split(",") || []) {
          const box = page.getByRole("checkbox", { name: new RegExp(`^${field}$`) }).last();
          if (await box.count()) {
            await box.check({ force: true }).catch(() => {});
            shown.push(field);
          } else {
            const label = page.getByText(new RegExp(`^${field}$`)).last();
            if (await label.count()) { await label.click(); shown.push(field); }
          }
          await page.waitForTimeout(400);
        }
        await settle(1500);
        result.fields = shown;
      }
    }
    // Frame the trace: its header at the top of the visible area.
    await page.getByText(/^Trace ID/).first().evaluate((el) => el.closest("section, div").scrollIntoView({ block: "start" }));
    await page.waitForTimeout(500);
    await audit();
    await page.screenshot({ path: out });
  } else if (cmd === "loki") {
    if (args[4]) await page.setViewportSize({ width: 1800, height: 1100 });
    const [query, out, from = "now-15m", expandText, follow] = args;
    await page.goto(
      explore({
        a: {
          datasource: "loki",
          queries: [{ refId: "A", datasource: { type: "loki", uid: "loki" }, expr: query, queryType: "range" }],
          range: { from, to: "now" },
        },
      }),
      { waitUntil: "domcontentloaded" },
    );
    await settle(2500);
    if (expandText) {
      await page.getByText(new RegExp(expandText)).last().click();
      await settle(1000);
      result.expanded = expandText;
    }
    if (follow) {
      // The trace_id derived field: from a log line back to its trace.
      await page.getByRole("button", { name: /View trace/i }).first().click();
      await page.getByText(/^Trace ID/).first().waitFor({ timeout: 30000 });
      await settle(2500);
      result.followed = "View trace";
    }
    await audit();
    await page.screenshot({ path: out });
  } else if (cmd === "text") {
    const [file, out, title = file] = args;
    const { readFileSync } = await import("node:fs");
    const esc = (t) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;");
    // Strip ANSI colour codes, but keep ticks and crosses readable.
    const body = esc(readFileSync(file, "utf8").replace(/\x1b\[[0-9;]*m/g, ""))
      .replace(/✓/g, '<b style="color:#3fb950">✓</b>')
      .replace(/✗/g, '<b style="color:#f85149">✗</b>');
    await page.setViewportSize({ width: 1400, height: 400 });
    await page.setContent(`<html><body style="margin:0;background:#0d1117;color:#e6edf3;font:18px/1.45 'DejaVu Sans Mono',monospace">
      <div style="background:#161b22;color:#8b949e;padding:10px 20px;font-size:15px">$ ${esc(title)}</div>
      <pre style="margin:0;padding:16px 20px;white-space:pre-wrap">${body}</pre></body></html>`);
    await page.screenshot({ path: out, fullPage: true });
  } else if (cmd === "page") {
    const [url, out, waitText] = args;
    await page.goto(url, { waitUntil: "domcontentloaded" });
    if (waitText) await page.getByText(waitText).first().waitFor({ timeout: 30000 });
    await settle(1500);
    await audit();
    await page.screenshot({ path: out });
  } else {
    throw new Error(`unknown command ${cmd}`);
  }
} catch (err) {
  result.problems.push(`error: ${err.message.split("\n")[0]}`);
  await page.screenshot({ path: args[1] || "/tmp/failed.png" }).catch(() => {});
}

await browser.close();
console.log(JSON.stringify(result));
process.exit(result.problems.length ? 2 : 0);
