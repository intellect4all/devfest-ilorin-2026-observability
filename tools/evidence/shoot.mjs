// Evidence screenshots. Run through scripts/shoot.sh, which starts this in
// the pinned Playwright image with host networking.
//
//   node shoot.mjs dashboard <uid> <out.png> [from=now-15m]
//   node shoot.mjs trace     <traceId> <out.png> [spanName] [logs]
//   node shoot.mjs loki      <logql> <out.png> [from=now-15m] [expandText]
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

async function clickText(re, opts = {}) {
  const el = page.getByText(re, opts).first();
  await el.waitFor({ timeout: 20000 });
  await el.click();
}

try {
  if (cmd === "dashboard") {
    const [uid, out, from = "now-15m"] = args;
    await page.goto(`${GRAFANA}/d/${uid}?orgId=1&kiosk&from=${from}&to=now&refresh=`, { waitUntil: "domcontentloaded" });
    await settle(3000);
    await audit();
    await page.screenshot({ path: out, fullPage: true });
  } else if (cmd === "trace") {
    const [traceId, out, spanName, logs] = args;
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
    if (spanName) {
      // Expand the named span to show its attributes.
      await clickText(new RegExp(`^${spanName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
      await settle(1000);
      result.expanded = spanName;
    }
    if (logs) {
      await page.getByRole("button", { name: /Logs for this span/i }).first().click();
      await settle(3000);
      result.logs = true;
    }
    await audit();
    await page.screenshot({ path: out });
  } else if (cmd === "loki") {
    const [query, out, from = "now-15m", expandText] = args;
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
      await clickText(expandText);
      await settle(1000);
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
