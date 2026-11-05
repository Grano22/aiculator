// Flow analytics: Sankey diagrams of states and of failure reasons, the retry funnel and a
// cumulative flow diagram, with filters. Shared by the project page and the realms page.
//
// Task IDs, labels and reasons can come from agents, so every piece of text goes in
// through textContent or an SVG <title>, and nothing is ever parsed as HTML.
"use strict";

(function () {
  const SVG = "http://www.w3.org/2000/svg";
  const PIPELINE = ["draft", "ready", "claimed", "running", "review", "merging", "done"];
  const EXITS = ["failed", "parked", "canceled"];
  // Bottom to top, done at the bottom, as a cumulative flow diagram is drawn.
  const STACK = ["done", "canceled", "failed", "parked", "merging", "review", "running", "claimed", "ready", "draft"];
  const WINDOWS = [["1h", "Last hour"], ["6h", "Last 6 hours"], ["24h", "Last 24 hours"], ["7d", "Last 7 days"], ["30d", "Last 30 days"], ["all", "Everything kept"]];

  function el(tag, text, cls) {
    const e = document.createElement(tag);
    if (text !== undefined && text !== null) e.textContent = String(text);
    if (cls) e.className = cls;
    return e;
  }
  function svgEl(tag, attrs) {
    const e = document.createElementNS(SVG, tag);
    for (const k in attrs || {}) e.setAttribute(k, attrs[k]);
    return e;
  }
  function title(node, text) {
    const t = svgEl("title");
    t.textContent = text;
    node.appendChild(t);
    return node;
  }
  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); return node; }
  // A class name for a state or reason. Only known words become one; anything else is "other".
  function tone(name) {
    if (PIPELINE.includes(name) || EXITS.includes(name)) return "s-" + name;
    if (name === "new") return "s-new";
    return "s-reason";
  }
  const plural = (n, one, many) => n + " " + (n === 1 ? one : many);

  // sankey draws a layered Sankey diagram of links {from, to, count}. Layers come from the
  // longest path to each node, so it works for any diagram without loops, which both of
  // ours are by construction.
  function sankey(host, links, opts) {
    opts = opts || {};
    clear(host);
    if (!links || !links.length) { host.appendChild(el("p", opts.empty || "Nothing to show yet.", "empty")); return; }
    const nodes = new Map();
    const node = (name) => {
      if (!nodes.has(name)) nodes.set(name, { name, in: 0, out: 0, layer: 0, ins: [], outs: [] });
      return nodes.get(name);
    };
    for (const l of links) {
      const a = node(l.from), b = node(l.to);
      a.out += l.count; b.in += l.count;
      a.outs.push(l); b.ins.push(l);
    }
    // Longest path from a source, by relaxing until nothing changes; bounded, so a loop
    // in bad data cannot hang the page.
    for (let pass = 0; pass < nodes.size; pass++) {
      let moved = false;
      for (const l of links) {
        const a = nodes.get(l.from), b = nodes.get(l.to);
        if (b.layer < a.layer + 1) { b.layer = a.layer + 1; moved = true; }
      }
      if (!moved) break;
    }
    const layers = [];
    for (const n of nodes.values()) {
      n.value = Math.max(n.in, n.out);
      (layers[n.layer] = layers[n.layer] || []).push(n);
    }
    const rank = (n) => {
      const i = PIPELINE.indexOf(n.name);
      if (i >= 0) return i;
      const j = EXITS.indexOf(n.name);
      return j >= 0 ? 100 + j : 200;
    };
    for (const col of layers) if (col) col.sort((a, b) => rank(a) - rank(b) || b.value - a.value || (a.name < b.name ? -1 : 1));

    const W = 900, H = opts.height || 300, nodeW = 12, pad = 10, labelRoom = 120;
    const cols = layers.length;
    const usable = H - 20;
    const scale = Math.min(...layers.filter(Boolean).map((col) => {
      const total = col.reduce((s, n) => s + n.value, 0);
      return (usable - pad * (col.length - 1)) / Math.max(total, 1);
    }));
    const xOf = (layer) => cols === 1 ? 0 : layer * ((W - labelRoom - nodeW) / (cols - 1));
    for (const col of layers) {
      if (!col) continue;
      let y = 10;
      for (const n of col) {
        n.x = xOf(n.layer); n.y = y; n.h = Math.max(2, n.value * scale);
        n.outY = n.y; n.inY = n.y;
        y += n.h + pad;
      }
    }
    const svg = svgEl("svg", { viewBox: "0 0 " + W + " " + H, class: "chart sankey", role: "img" });
    const total = links.reduce((s, l) => s + l.count, 0);
    svg.setAttribute("aria-label", (opts.label || "Flow") + ": " + links.map((l) => l.from + " to " + l.to + " " + l.count).join(", "));
    const bands = svgEl("g", { class: "bands" });
    const ordered = links.slice().sort((p, q) => rank(nodes.get(p.from)) - rank(nodes.get(q.from)) || rank(nodes.get(p.to)) - rank(nodes.get(q.to)));
    for (const l of ordered) {
      const a = nodes.get(l.from), b = nodes.get(l.to);
      const w = Math.max(1, l.count * scale);
      const y0 = a.outY + w / 2, y1 = b.inY + w / 2;
      a.outY += w; b.inY += w;
      const x0 = a.x + nodeW, x1 = b.x, mx = (x0 + x1) / 2;
      const p = svgEl("path", { d: "M" + x0 + " " + y0 + " C" + mx + " " + y0 + " " + mx + " " + y1 + " " + x1 + " " + y1, class: "band " + tone(l.to), "stroke-width": w });
      title(p, l.from + " → " + l.to + ": " + plural(l.count, opts.unit || "task", opts.units || "tasks") + " (" + Math.round(100 * l.count / Math.max(total, 1)) + "%)");
      bands.appendChild(p);
    }
    svg.appendChild(bands);
    for (const n of nodes.values()) {
      const g = svgEl("g", { class: "node" });
      g.appendChild(title(svgEl("rect", { x: n.x, y: n.y, width: nodeW, height: n.h, class: tone(n.name) }),
        n.name + ": " + n.value + (n.in && n.out && n.in > n.out ? ", " + (n.in - n.out) + " still here" : "")));
      const t = svgEl("text", { x: n.x + nodeW + 6, y: n.y + n.h / 2, class: "lbl" });
      t.textContent = n.name + " " + n.value;
      g.appendChild(t);
      svg.appendChild(g);
    }
    host.appendChild(svg);
  }

  // funnel draws how many tasks reached each try, and how many finished on it.
  function funnel(host, steps) {
    clear(host);
    if (!steps || !steps.length) { host.appendChild(el("p", "No task has been tried yet.", "empty")); return; }
    const peak = Math.max(1, ...steps.map((s) => s.reached));
    const list = el("ol", null, "funnel");
    for (const s of steps) {
      const li = el("li");
      const top = el("div", null, "row");
      top.appendChild(el("span", "Try " + s.attempt));
      top.appendChild(el("span", s.reached + " reached, " + s.done + " done here" + (s.reached ? " (" + Math.round(100 * s.done / s.reached) + "%)" : ""), "muted"));
      li.appendChild(top);
      const bar = el("div", null, "fbar");
      bar.setAttribute("role", "img");
      bar.setAttribute("aria-label", "try " + s.attempt + ": " + s.reached + " reached, " + s.done + " finished");
      const reached = el("span", null, "reached");
      reached.style.setProperty("width", (100 * s.reached / peak) + "%");
      const done = el("span", null, "done");
      done.style.setProperty("width", (100 * s.done / peak) + "%");
      bar.appendChild(reached);
      bar.appendChild(done);
      li.appendChild(bar);
      list.appendChild(li);
    }
    host.appendChild(list);
  }

  function causes(host, list) {
    clear(host);
    if (!list || !list.length) { host.appendChild(el("p", "No retries.", "empty")); return; }
    const peak = Math.max(1, ...list.map((c) => c.count));
    const ul = el("ul", null, "edges");
    for (const c of list.slice(0, 10)) {
      const li = el("li");
      const top = el("div", null, "row");
      top.appendChild(el("span", c.name));
      top.appendChild(el("span", c.count, "muted"));
      li.appendChild(top);
      const b = el("div", null, "bar warn");
      const f = el("span");
      f.style.setProperty("width", (100 * c.count / peak) + "%");
      b.appendChild(f);
      li.appendChild(b);
      ul.appendChild(li);
    }
    host.appendChild(ul);
  }

  function when(t, spanMs) {
    const d = new Date(t);
    if (spanMs <= 36 * 3600e3) return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    return d.toLocaleDateString([], { month: "short", day: "numeric" });
  }

  // cfd draws a stacked area of how many tasks were in each state at each moment.
  function cfd(host, data) {
    clear(host);
    const times = (data && data.times) || [];
    if (times.length < 2) { host.appendChild(el("p", "Not enough history yet.", "empty")); return; }
    const W = 900, H = 260, left = 40, bottom = 22, top = 8;
    const n = times.length;
    const states = STACK.filter((s) => (data.series[s] || []).some((v) => v > 0));
    const totals = times.map((_, k) => states.reduce((sum, s) => sum + data.series[s][k], 0));
    const peak = Math.max(1, ...totals);
    const x = (k) => left + (W - left - 4) * k / (n - 1);
    const y = (v) => H - bottom - (H - bottom - top) * v / peak;
    const svg = svgEl("svg", { viewBox: "0 0 " + W + " " + H, class: "chart cfd", role: "img" });
    const last = n - 1;
    svg.setAttribute("aria-label", "Tasks in each state over time. Now: " + states.map((s) => s + " " + data.series[s][last]).join(", "));
    const base = new Array(n).fill(0);
    for (const s of states) {
      const vals = data.series[s];
      let d = "";
      for (let k = 0; k < n; k++) d += (k ? " L" : "M") + x(k).toFixed(1) + " " + y(base[k] + vals[k]).toFixed(1);
      for (let k = n - 1; k >= 0; k--) d += " L" + x(k).toFixed(1) + " " + y(base[k]).toFixed(1);
      const path = svgEl("path", { d: d + " Z", class: "area " + tone(s) });
      title(path, s + ": " + vals[last] + " now, at most " + Math.max(...vals));
      svg.appendChild(path);
      for (let k = 0; k < n; k++) base[k] += vals[k];
    }
    const axis = svgEl("g", { class: "axis" });
    axis.appendChild(svgEl("line", { x1: left, y1: H - bottom, x2: W - 4, y2: H - bottom }));
    const spanMs = new Date(times[last]) - new Date(times[0]);
    for (const k of [0, Math.floor(last / 2), last]) {
      const t = svgEl("text", { x: x(k), y: H - 6, "text-anchor": k === 0 ? "start" : k === last ? "end" : "middle" });
      t.textContent = when(times[k], spanMs);
      axis.appendChild(t);
    }
    for (const v of [0, peak]) {
      const t = svgEl("text", { x: left - 6, y: y(v) + 4, "text-anchor": "end" });
      t.textContent = v;
      axis.appendChild(t);
    }
    svg.appendChild(axis);
    host.appendChild(svg);
    const legend = el("ul", null, "legend");
    for (const s of states.slice().reverse()) {
      const li = el("li");
      li.appendChild(el("span", null, "swatch " + tone(s)));
      li.appendChild(el("span", s + " " + data.series[s][last]));
      legend.appendChild(li);
    }
    host.appendChild(legend);
  }

  function select(label, options, value) {
    const wrap = el("label", null, "filter");
    wrap.appendChild(el("span", label, "muted small"));
    const s = el("select");
    for (const [v, text] of options) {
      const o = el("option", text);
      o.value = v;
      if (v === value) o.selected = true;
      s.appendChild(o);
    }
    wrap.appendChild(s);
    return [wrap, s];
  }

  // mount builds the analytics card in host. fetchFn(params) returns the analytics for a
  // URLSearchParams, and realms, when given, adds a realm filter.
  function mount(host, fetchFn, opts) {
    opts = opts || {};
    clear(host);
    const state = { window: "24h", tier: "", label: "", realm: "" };
    try { Object.assign(state, JSON.parse(sessionStorage.getItem("aiculator-flow") || "{}")); } catch (_) { /* Private mode. */ }
    const head = el("div", null, "flow-head");
    head.appendChild(el("h2", "Flow analytics"));
    const filters = el("div", null, "filters");
    head.appendChild(filters);
    host.appendChild(head);
    const note = el("p", null, "muted small");
    host.appendChild(note);

    const grid = el("div", null, "flow-grid");
    const panel = (name, sub, cls) => {
      const p = el("div", null, "panel " + (cls || ""));
      p.appendChild(el("h3", name));
      if (sub) p.appendChild(el("p", sub, "muted small"));
      const body = el("div");
      p.appendChild(body);
      grid.appendChild(p);
      return body;
    };
    const states = panel("States", "The path each task took, and where it left it.", "wide");
    const failures = panel("Why tasks fall out", "Each time a task failed or was parked: from where, and why.", "wide");
    const tries = panel("Tries", "How many tries tasks took, and how many finished on each.");
    const why = panel("What caused retries", "What ended the try before each new one.");
    const flow = panel("Cumulative flow", "Tasks in each state over time. A band that keeps growing is a bottleneck.", "wide");
    host.appendChild(grid);

    let realmSel = null, tierSel, labelSel;
    function rebuildFilters(a) {
      clear(filters);
      if (opts.realms) {
        const names = opts.realms();
        if (!state.realm || !names.includes(state.realm)) state.realm = names[0] || "";
        const r = select("Realm", names.map((n) => [n, n]), state.realm);
        realmSel = r[1];
        filters.appendChild(r[0]);
      }
      const w = select("Window", WINDOWS, state.window);
      filters.appendChild(w[0]);
      const tiers = [["", "All tiers"]].concat(((a && a.tiers) || []).map((t) => [String(t), "Tier " + t]));
      const t = select("Tier", tiers, state.tier);
      tierSel = t[1];
      filters.appendChild(t[0]);
      const labels = [["", "All labels"]].concat(((a && a.labels) || []).map((l) => [l, l]));
      if (state.label && !labels.some((l) => l[0] === state.label)) labels.push([state.label, state.label]);
      const l = select("Label", labels, state.label);
      labelSel = l[1];
      filters.appendChild(l[0]);
      for (const [sel, key] of [[w[1], "window"], [tierSel, "tier"], [labelSel, "label"], [realmSel, "realm"]]) {
        if (!sel) continue;
        sel.addEventListener("change", () => {
          state[key] = sel.value;
          try { sessionStorage.setItem("aiculator-flow", JSON.stringify(state)); } catch (_) { /* Private mode. */ }
          refresh();
        });
      }
    }

    let busy = false, built = false;
    async function refresh() {
      if (busy) return;
      busy = true;
      try {
        const p = new URLSearchParams();
        p.set("window", state.window);
        if (state.tier) p.set("tier", state.tier);
        if (state.label) p.set("label", state.label);
        if (opts.realms && state.realm) p.set("realm", state.realm);
        if (opts.realms && !state.realm) { note.textContent = "No realm is running."; return; }
        const a = await fetchFn(p);
        if (!built || filters.querySelector("select") === null || document.activeElement === null || !filters.contains(document.activeElement)) {
          rebuildFilters(a);
          built = true;
        }
        note.textContent = (a.ready ? "" : "Still reading the event log, so these numbers are incomplete. ") +
          plural(a.tasks, "task", "tasks") + " changed in this window. From the durable event log, so a restart loses nothing.";
        sankey(states, a.state_flow, { label: "State flow", empty: "No task moved in this window." });
        sankey(failures, a.failure_flow, { label: "Failure reasons", unit: "time", units: "times", height: 240, empty: "No task failed or was parked in this window." });
        funnel(tries, a.attempts);
        causes(why, a.retry_causes);
        cfd(flow, a.cumulative_flow);
      } catch (e) {
        note.textContent = "Could not load the analytics: " + (e && e.message ? e.message : e);
      } finally {
        busy = false;
      }
    }
    rebuildFilters(null);
    refresh();
    return { refresh };
  }

  window.AiculatorCharts = { sankey, funnel, cfd, mount };
})();
