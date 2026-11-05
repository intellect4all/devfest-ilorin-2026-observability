// Trace facts for evidence checks, straight from Tempo's API.
//
//   node trace.mjs search '<traceql>' [limit=20] [sinceSeconds=300]
//       -> [{traceID, durationMs, rootServiceName, rootTraceName}] slowest first
//   node trace.mjs analyze <traceId>
//       -> {traceId, services, roots, spans:[{name, service, durMs, selfMs, attrs}], widest}
//
// "widest" is the span with the most self time (its duration minus its
// children's), i.e. where the time was actually spent.
const TEMPO = process.env.TEMPO_URL || "http://127.0.0.1:3200";
const [cmd, ...args] = process.argv.slice(2);

const b64hex = (s) => (s ? Buffer.from(s, "base64").toString("hex") : "");
const val = (v) => v.stringValue ?? v.intValue ?? v.doubleValue ?? v.boolValue ?? "";

async function get(path) {
  const r = await fetch(TEMPO + path, { headers: { Accept: "application/json" } });
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status}`);
  return r.json();
}

if (cmd === "search") {
  const [q, limit = "20", since = "300"] = args;
  const end = Math.floor(Date.now() / 1000);
  const res = await get(`/api/search?q=${encodeURIComponent(q)}&limit=${limit}&start=${end - Number(since)}&end=${end}`);
  const traces = (res.traces || [])
    .map((t) => ({ traceID: t.traceID.padStart(32, "0"), durationMs: t.durationMs ?? 0, rootServiceName: t.rootServiceName, rootTraceName: t.rootTraceName }))
    .sort((a, b) => b.durationMs - a.durationMs);
  console.log(JSON.stringify(traces));
} else if (cmd === "analyze") {
  const [id] = args;
  const res = await get(`/api/traces/${id}`);
  const spans = [];
  for (const batch of res.batches || res.resourceSpans || []) {
    const svc = val((batch.resource.attributes || []).find((a) => a.key === "service.name")?.value || {});
    for (const ss of batch.scopeSpans || []) {
      for (const s of ss.spans || []) {
        const start = BigInt(s.startTimeUnixNano), end = BigInt(s.endTimeUnixNano);
        spans.push({
          id: b64hex(s.spanId),
          parent: b64hex(s.parentSpanId),
          name: s.name,
          service: svc,
          startNs: start,
          durMs: Number(end - start) / 1e6,
          attrs: Object.fromEntries((s.attributes || []).map((a) => [a.key, val(a.value)])),
        });
      }
    }
  }
  const byId = new Map(spans.map((s) => [s.id, s]));
  for (const s of spans) s.childMs = 0;
  for (const s of spans) if (byId.has(s.parent)) byId.get(s.parent).childMs += s.durMs;
  for (const s of spans) s.selfMs = Math.max(0, s.durMs - s.childMs);
  const roots = spans.filter((s) => !byId.has(s.parent));
  spans.sort((a, b) => (a.startNs < b.startNs ? -1 : 1));
  const widest = [...spans].sort((a, b) => b.selfMs - a.selfMs)[0];
  const round = (n) => Math.round(n * 10) / 10;
  const out = (s) => ({ name: s.name, service: s.service, durMs: round(s.durMs), selfMs: round(s.selfMs), attrs: s.attrs });
  console.log(
    JSON.stringify({
      traceId: id,
      services: [...new Set(spans.map((s) => s.service))].sort(),
      roots: roots.map((s) => `${s.service}: ${s.name}`),
      spanCount: spans.length,
      spans: spans.map(out),
      widest: widest && out(widest),
    }),
  );
} else {
  console.error("usage: trace.mjs search <traceql> | analyze <traceId>");
  process.exit(1);
}
