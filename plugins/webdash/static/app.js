// Everything shown here may come from an agent, so all of it goes in through textContent.
// Nothing is ever parsed as HTML. A test fails if that changes.
"use strict";

const $ = (id) => document.getElementById(id);
const PIPELINE = ["draft", "ready", "claimed", "running", "review", "merging", "done"];
const INTERVAL_MS = 1000;
const KEY = "aiculator-token";

function readToken() {
  const m = /[#&]token=([^&]+)/.exec(location.hash);
  if (m) {
    // Take it out of the address bar, so it is not left in view or in screenshots.
    history.replaceState(null, "", location.pathname);
    try { sessionStorage.setItem(KEY, m[1]); } catch (_) { /* Private mode. */ }
    return m[1];
  }
  try { return sessionStorage.getItem(KEY) || ""; } catch (_) { return ""; }
}
const token = readToken();
let paused = false;

function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text !== undefined && text !== null) e.textContent = String(text);
  if (cls) e.className = cls;
  return e;
}
function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); return node; }

const count = (n) => {
  n = n || 0;
  if (n < 10000) return String(n);
  if (n < 1e6) return (n / 1e3).toFixed(1) + "k";
  if (n < 1e9) return (n / 1e6).toFixed(1) + "M";
  return (n / 1e9).toFixed(1) + "G";
};
const money = (micro) => "$" + ((micro || 0) / 1e6).toFixed(2);
const rate = (n) => (n || 0).toFixed(1);
function micros(us) {
  if (us === undefined) return "-";
  if (us < 1000) return us + "µs";
  if (us < 1e6) return (us / 1000).toFixed(1) + "ms";
  if (us < 6e7) return (us / 1e6).toFixed(1) + "s";
  return span(us / 1000);
}
function span(ms) {
  if (ms === undefined || ms === null) return "-";
  const neg = ms < 0;
  let s = Math.floor(Math.abs(ms) / 1000);
  if (neg) s = Math.max(s, 1);
  const sign = neg ? "-" : "";
  if (s < 60) return sign + s + "s";
  if (s < 3600) return sign + Math.floor(s / 60) + "m" + String(s % 60).padStart(2, "0") + "s";
  if (s < 86400) return sign + Math.floor(s / 3600) + "h" + String(Math.floor(s / 60) % 60).padStart(2, "0") + "m";
  return sign + Math.floor(s / 86400) + "d" + String(Math.floor(s / 3600) % 24).padStart(2, "0") + "h";
}

const SVG = "http://www.w3.org/2000/svg";
function spark(node, values) {
  values = values || [];
  const peak = Math.max(1, ...values);
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("viewBox", "0 0 " + Math.max(values.length, 1) + " 24");
  svg.setAttribute("preserveAspectRatio", "none");
  svg.setAttribute("aria-hidden", "true");
  values.forEach((v, i) => {
    if (v <= 0) return;
    const h = Math.max(2, (v / peak) * 24);
    const r = document.createElementNS(SVG, "rect");
    r.setAttribute("x", i);
    r.setAttribute("y", 24 - h);
    r.setAttribute("width", 0.8);
    r.setAttribute("height", h);
    r.setAttribute("fill", "currentColor");
    svg.appendChild(r);
  });
  clear(node).appendChild(svg);
}

function row(tbody, cells) {
  const tr = el("tr");
  for (const c of cells) {
    if (c instanceof Node) tr.appendChild(c);
    else if (Array.isArray(c)) tr.appendChild(el("td", c[0], c[1]));
    else tr.appendChild(el("td", c));
  }
  tbody.appendChild(tr);
}
const num = (v) => [v, "n"];

function health(h) {
  const td = el("td");
  const words = { ok: ["OK", "ok"], at_risk: ["At risk", "warn"], expired: ["Expired", "bad"], queued: ["-", ""] };
  const [word, cls] = words[h] || [h, ""];
  if (!cls) { td.textContent = word; td.className = "muted"; return td; }
  td.appendChild(el("span", word, "pill " + cls));
  return td;
}

function render(s) {
  $("version").textContent = s.version || "";
  $("uptime").textContent = "up " + span(s.uptime_ms);
  const ready = $("ready");
  ready.textContent = s.ready ? "Ready" : "Not ready";
  ready.className = "pill " + (s.ready ? "ok" : "bad");
  const planner = $("planner");
  planner.hidden = !(s.planning && (s.planning.active || s.planning.lost));
  if (!planner.hidden) {
    planner.textContent = s.planning.lost ? "Planner lost" : "Planning: " + (s.planning.phase || "");
    planner.className = "pill " + (s.planning.lost ? "bad" : "info");
  }

  const t = s.throughput || {};
  $("k-done").textContent = rate(t.done_per_min);
  $("k-grants").textContent = rate(t.grants_per_min);
  $("k-commits").textContent = rate(t.commits_per_sec);
  spark($("s-done"), t.done_history);
  spark($("s-grants"), t.grants_history);
  spark($("s-commits"), t.commits_history);
  $("k-waiting").textContent = s.waiting_agents || 0;
  $("k-oldest").textContent = s.oldest_ready_ms ? "oldest ready " + span(s.oldest_ready_ms) : "nothing queued";
  const u = s.usage || {};
  $("k-tokens").textContent = count(u.tokens);
  $("k-spent").textContent = money(u.spent) + " spent" + (u.over_budget ? ", " + u.over_budget + " over budget" : "");
  $("k-zombies").textContent = t.total_zombies || 0;
  $("k-failed").textContent = (t.total_failed || 0) + " failures, " + (t.total_done || 0) + " done";

  const states = s.states || {};
  const pipe = clear($("pipeline"));
  for (const st of PIPELINE) {
    const li = el("li", null, states[st] && st !== "done" ? "busy" : "");
    li.appendChild(el("span", states[st] || 0, "count"));
    li.appendChild(el("span", st, "name"));
    pipe.appendChild(li);
  }
  $("off-path").textContent = "Failed " + (states.failed || 0) + ", parked " + (states.parked || 0) + ", canceled " + (states.canceled || 0);

  const lat = clear($("latency").tBodies[0]);
  const rows = [["queue_wait", "Queue wait"], ["commit", "Commit"], ["api", "API call"], ["task_cycle", "Task cycle"]];
  for (const [key, label] of rows) {
    const v = (s.latency || {})[key];
    if (!v) { row(lat, [label, num("-"), num("-"), num("-"), num("-"), num("-"), num("-")]); continue; }
    row(lat, [label, num(count(v.count)), num(micros(v.p50_us)), num(micros(v.p90_us)), num(micros(v.p99_us)), num(micros(v.max_us)), num(micros(v.jitter_us))]);
  }

  const probes = clear($("probes"));
  for (const p of s.probes || []) {
    const li = el("li");
    li.appendChild(el("span", p.name));
    const right = el("span");
    right.appendChild(el("span", micros(p.latency_us) + " ", "muted small"));
    right.appendChild(el("span", p.ok ? "OK" : "Failing", "pill " + (p.ok ? "ok" : "bad")));
    li.appendChild(right);
    if (!p.ok && p.detail) li.appendChild(el("span", p.detail, "detail"));
    probes.appendChild(li);
  }

  const tasks = clear($("tasks").tBodies[0]);
  const list = s.tasks || [];
  $("task-count").textContent = "(" + (list.length + (s.tasks_not_shown || 0)) + ")";
  $("tasks-empty").hidden = list.length > 0;
  $("tasks").hidden = list.length === 0;
  for (const x of list) {
    const reason = (x.state === "parked" || x.state === "failed") && x.reason ? " [" + x.reason + "]" : "";
    row(tasks, [health(x.health), x.state, num(x.tier), x.id, x.owner || "-", num(span(x.lease_remaining_ms)), num(span(x.progress_remaining_ms)), num(x.attempt), [x.title + reason, "title"]]);
  }

  const agents = clear($("agents").tBodies[0]);
  const al = s.agents || [];
  $("agents-empty").hidden = al.length > 0;
  $("agents").hidden = al.length === 0;
  for (const a of al) {
    row(agents, [a.id, num(a.tier), a.state, a.task || "-", num(span(a.seen_ago_ms)), num(count(a.tokens)), num(a.done), num(a.failed)]);
  }

  const sessions = clear($("sessions").tBodies[0]);
  const sl = s.sessions || [];
  $("sessions-empty").hidden = sl.length > 0;
  $("sessions").hidden = sl.length === 0;
  $("session-count").textContent = "(" + sl.filter((x) => x.state !== "gone").length + " connected" + (sl.some((x) => x.state === "gone") ? ", " + sl.filter((x) => x.state === "gone").length + " gone" : "") + ")";
  for (const x of sl) {
    const st = el("td");
    const cls = { working: "ok", waiting: "info", limited: "warn", stopping: "warn", gone: "bad" }[x.state] || "";
    st.appendChild(el("span", x.state, "pill " + cls));
    if (x.limit) st.appendChild(el("span", " " + x.limit + (x.limit_until ? " until " + new Date(x.limit_until).toLocaleTimeString() : ""), "muted small"));
    const place = [x.host, x.pid ? "pid " + x.pid : "", x.parent ? "in " + x.parent : ""].filter(Boolean).join(", ");
    row(sessions, [x.agent, num(x.tier), x.kind || "-", [(x.harness || "-") + (x.model ? " · " + x.model : ""), ""], st, x.task || "-",
      num(span(x.seen_ago_ms)), num(span(x.connected_ms)), num(x.hold_ms ? span(x.hold_ms) : "-"), place || "-", [x.cwd || "-", "title"]]);
  }

  const tiers = clear($("tiers").tBodies[0]);
  for (const x of u.by_tier || []) {
    row(tiers, [x.tier, num(x.tasks), num(count(x.tokens)), num(money(x.spent)), num(x.budget ? money(x.budget) : "-")]);
  }
  const budgets = clear($("budgets"));
  const tight = u.tightest_budgets || [];
  $("budgets-empty").hidden = tight.length > 0;
  for (const b of tight) {
    const share = b.budget ? (b.spent || 0) / b.budget : 0;
    const li = el("li");
    const top = el("div", null, "row");
    top.appendChild(el("span", b.id));
    top.appendChild(el("span", money(b.spent) + " of " + money(b.budget), "muted"));
    li.appendChild(top);
    budgets.appendChild(li);
    li.appendChild(bar(share, share >= 1 ? "bad" : share >= 0.75 ? "warn" : "ok"));
  }

  const edges = clear($("edges"));
  const flow = s.flow || [];
  $("edges-empty").hidden = flow.length > 0;
  const peak = Math.max(1, ...flow.map((e) => e.count));
  for (const e of flow.slice(0, 14)) {
    const li = el("li");
    const top = el("div", null, "row");
    top.appendChild(el("span", e.from + " → " + e.to));
    top.appendChild(el("span", count(e.count), "muted"));
    li.appendChild(top);
    li.appendChild(bar(e.count / peak, ""));
    edges.appendChild(li);
  }
}

function bar(share, cls) {
  const b = el("div", null, "bar " + cls);
  b.setAttribute("role", "img");
  b.setAttribute("aria-label", Math.round(share * 100) + " percent");
  const fill = el("span");
  fill.style.setProperty("width", Math.min(share, 1) * 100 + "%");
  b.appendChild(fill);
  return b;
}

function problem(msg) {
  const b = $("banner");
  b.hidden = !msg;
  b.textContent = msg || "";
}

const NEEDS_TOKEN = "This page needs a fresh link. Run: aiculator web";

async function poll() {
  if (paused || document.hidden) return;
  try {
    const r = await fetch("../../v1/observe", { headers: { Authorization: "Bearer " + token }, cache: "no-store" });
    if (r.status === 401 || r.status === 403) { problem(NEEDS_TOKEN); return; }
    if (!r.ok) throw new Error("server answered " + r.status);
    render(await r.json());
    problem("");
  } catch (e) {
    problem("Cannot reach the server, so these numbers are out of date. Retrying.");
    $("ready").textContent = "Disconnected";
    $("ready").className = "pill bad";
  }
}

// Follows the event stream with fetch, because EventSource cannot send a token.
async function follow() {
  const feed = $("feed");
  for (;;) {
    try {
      const r = await fetch("../../v1/events", { headers: { Authorization: "Bearer " + token } });
      if (r.status === 401 || r.status === 403) return;
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, i);
          buf = buf.slice(i + 1);
          if (!line.trim()) continue;
          const t = JSON.parse(line);
          $("feed-empty").hidden = true;
          const li = el("li");
          li.appendChild(el("time", new Date().toLocaleTimeString()));
          li.appendChild(el("span", t.id + " → " + t.state + (t.owner ? " (" + t.owner + ")" : ""), "what"));
          feed.prepend(li);
          while (feed.children.length > 100) feed.removeChild(feed.lastChild);
        }
      }
    } catch (_) { /* Reconnect below. */ }
    await new Promise((res) => setTimeout(res, 2000));
  }
}

// The buttons appear only for a link made with aiculator web --control, which may use them.
async function control(path, body, ask) {
  if (ask && !window.confirm(ask)) return;
  const note = $("control-note");
  try {
    const r = await fetch("../../v1/" + path, { method: "POST", headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error("server answered " + r.status);
    const out = await r.json();
    const parts = [];
    if (out.stopped) parts.push("stopped " + out.stopped.length);
    if (out.killed) parts.push("killed " + out.killed.length);
    if (out.released && out.released.length) parts.push("handed back " + out.released.length + " tasks");
    if (out.forgotten) parts.push("removed " + out.forgotten.length);
    note.textContent = parts.join(", ") || "done";
  } catch (e) {
    note.textContent = "Could not do that: " + e.message;
  }
  note.hidden = false;
  poll();
}
async function setupControls() {
  try {
    const r = await fetch("../../v1/whoami", { headers: { Authorization: "Bearer " + token } });
    if (!r.ok) return;
    const me = await r.json();
    if (!me.control) return;
  } catch (_) { return; }
  $("controls").hidden = false;
  $("forget-gone").addEventListener("click", () => control("sessions/forget", {}, ""));
  $("stop-all").addEventListener("click", () => control("agents/stop", {}, "Tell every agent to exit? What they hold goes back to the queue."));
  $("kill-all").addEventListener("click", () => control("agents/stop", { kill: true }, "Stop every agent and end the processes aiculator started for them? Harnesses you started yourself are only told to exit."));
}

$("pause").addEventListener("click", () => {
  paused = !paused;
  $("pause").textContent = paused ? "Resume" : "Pause";
});
document.addEventListener("visibilitychange", poll);

if (!token) {
  problem(NEEDS_TOKEN);
} else {
  poll();
  setInterval(poll, INTERVAL_MS);
  follow();
  setupControls();
  const flow = window.AiculatorCharts.mount($("flow-analytics"), async (params) => {
    const r = await fetch("../../v1/analytics?" + params.toString(), { headers: { Authorization: "Bearer " + token }, cache: "no-store" });
    if (!r.ok) throw new Error("server answered " + r.status);
    return r.json();
  });
  // The analytics change slowly and cost more to work out, so they refresh less often.
  setInterval(() => { if (!paused && !document.hidden) flow.refresh(); }, 15000);
}
