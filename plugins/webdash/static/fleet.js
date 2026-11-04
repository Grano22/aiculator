// Everything shown here may come from an agent, so all of it goes in through textContent.
// Nothing is ever parsed as HTML. A test fails if that changes.
"use strict";

const $ = (id) => document.getElementById(id);
const INTERVAL_MS = 2000;
const KEY = "aiculator-fleet-token";

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
function row(cells) {
  const tr = el("tr");
  for (const c of cells) tr.append(c);
  return tr;
}
function num(n) { return el("td", n, "n"); }

// Only a plain local address can become a link, whatever a server answered.
function safeLink(link) {
  try {
    const u = new URL(link);
    return u.protocol === "http:" && u.hostname === "127.0.0.1" ? u.href : "";
  } catch (_) { return ""; }
}

function stateCell(v) {
  if (v.missing) return el("span", "Folder missing", "pill warn");
  if (!v.live) return el("span", "Stopped", "pill");
  return v.snapshot && v.snapshot.ready ? el("span", "Running", "pill ok") : el("span", "Unwell", "pill bad");
}

function realmCell(v) {
  const td = el("td", null, "realm-name");
  const href = v.link ? safeLink(v.link) : "";
  if (href) {
    const a = el("a", v.realm.name);
    a.href = href;
    a.rel = "noopener noreferrer";
    a.target = "_blank";
    td.append(a);
  } else {
    td.textContent = v.realm.name;
  }
  return td;
}

function render(views) {
  const totals = { running: 0, ready: 0, work: 0, agents: 0, done: 0, parked: 0 };
  const realms = clear($("realms").tBodies[0]);
  const tasks = clear($("tasks").tBodies[0]);
  const agents = clear($("agents").tBodies[0]);
  let taskCount = 0;
  let agentCount = 0;
  for (const v of views) {
    const s = v.snapshot || {};
    const st = s.states || {};
    const list = s.agents || [];
    const working = list.filter((a) => a.state === "working").length;
    const active = (st.claimed || 0) + (st.running || 0);
    if (v.live) {
      totals.running++;
      totals.ready += st.ready || 0;
      totals.work += active;
      totals.agents += working;
      totals.done += st.done || 0;
      totals.parked += st.parked || 0;
    }
    const state = el("td");
    state.append(stateCell(v));
    realms.append(row([
      realmCell(v), state,
      num(v.live ? st.ready || 0 : "-"), num(v.live ? active : "-"),
      num(v.live ? (st.review || 0) + (st.merging || 0) : "-"), num(v.live ? st.done || 0 : "-"),
      num(v.live ? st.parked || 0 : "-"), num(v.live ? working + "/" + list.length : "-"),
      el("td", v.realm.path, "path"),
    ]));
    for (const t of s.tasks || []) {
      taskCount++;
      tasks.append(row([el("td", v.realm.name, "realm-name"), el("td", t.id), el("td", t.state), num(t.tier), el("td", t.owner || ""), el("td", t.title, "title")]));
    }
    for (const a of list) {
      agentCount++;
      agents.append(row([el("td", v.realm.name, "realm-name"), el("td", a.id), num(a.tier), el("td", a.state), el("td", a.task || "")]));
    }
  }
  $("summary").textContent = views.length + (views.length === 1 ? " realm" : " realms");
  $("k-running").textContent = totals.running;
  $("k-ready").textContent = totals.ready;
  $("k-work").textContent = totals.work;
  $("k-agents").textContent = totals.agents;
  $("k-done").textContent = totals.done;
  $("k-parked").textContent = totals.parked;
  $("task-count").textContent = taskCount ? "(" + taskCount + ")" : "";
  $("realms-empty").hidden = views.length > 0;
  $("tasks-empty").hidden = taskCount > 0;
  $("agents-empty").hidden = agentCount > 0;
}

async function tick() {
  if (paused) return;
  const banner = $("banner");
  try {
    const r = await fetch("api/fleet", { headers: { Authorization: "Bearer " + token } });
    if (!r.ok) throw new Error(r.status === 401 ? "This page needs the link printed by aiculator web --all." : "Status " + r.status);
    render((await r.json()) || []);
    banner.hidden = true;
  } catch (e) {
    banner.textContent = "Not updating: " + e.message + " Is aiculator web --all still running?";
    banner.hidden = false;
  }
}

$("pause").addEventListener("click", () => {
  paused = !paused;
  $("pause").textContent = paused ? "Resume" : "Pause";
});
tick();
setInterval(tick, INTERVAL_MS);
