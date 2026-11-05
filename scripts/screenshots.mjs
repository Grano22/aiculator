// Takes the documentation's screenshots from a running server, using Chrome's debugging
// protocol. Terminal frames are real aiculator top output, drawn on a terminal-like page.
import { spawn, execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";

const [link, out, tmp] = process.argv.slice(2);
mkdirSync(out, { recursive: true });
const CHROME = process.env.CHROME || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const chrome = spawn(CHROME, ["--headless=new", "--disable-gpu", "--no-first-run", "--hide-scrollbars", "--remote-debugging-port=9334", `--user-data-dir=${tmp}/shots-profile`, "about:blank"], { stdio: "ignore" });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let target;
for (let i = 0; i < 60 && !target; i++) {
  await sleep(250);
  try {
    target = (await (await fetch("http://127.0.0.1:9334/json")).json()).find((t) => t.type === "page");
  } catch { /* Not up yet. */ }
}
if (!target) throw new Error("Chrome did not start");
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));
let id = 0;
const waiting = new Map();
ws.onmessage = (m) => {
  const d = JSON.parse(m.data);
  if (d.id) { waiting.get(d.id)?.(d.result ?? d.error); waiting.delete(d.id); }
};
const send = (method, params = {}) => new Promise((r) => { const n = ++id; waiting.set(n, r); ws.send(JSON.stringify({ id: n, method, params })); });
await send("Page.enable");

async function capture(name, width, height, scheme, scale = 2, maxHeight = 1700) {
  await send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: width < 500 });
  await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }] });
  await sleep(1500);
  const metrics = await send("Page.getLayoutMetrics");
  const h = Math.min(Math.ceil(metrics.cssContentSize.height), maxHeight);
  const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true, clip: { x: 0, y: 0, width, height: h, scale: 1 } });
  writeFileSync(`${out}/${name}.png`, Buffer.from(shot.data, "base64"));
}

await send("Runtime.enable");
const evaluate = async (expr) => (await send("Runtime.evaluate", { expression: expr, returnByValue: true })).result.value;

await send("Page.navigate", { url: link });
await sleep(2500);

// The README's animation: the top of the dashboard, live, one frame every half second.
mkdirSync(`${tmp}/frames`, { recursive: true });
await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 860, deviceScaleFactor: 1, mobile: false });
await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "dark" }] });
await sleep(1500);
for (let i = 0; i < 28; i++) {
  const shot = await send("Page.captureScreenshot", { format: "png", clip: { x: 0, y: 0, width: 1280, height: 860, scale: 1 } });
  writeFileSync(`${tmp}/frames/frame-${String(i).padStart(3, "0")}.png`, Buffer.from(shot.data, "base64"));
  await sleep(500);
}

// The flow analytics on their own, in both colour schemes.
async function element(name, selector, scheme) {
  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1.5, mobile: false });
  await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }] });
  await sleep(1200);
  const r = await evaluate(`(() => { const b = document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect(); return { x: b.left + scrollX, y: b.top + scrollY, w: b.width, h: b.height }; })()`);
  const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true, clip: { x: r.x - 8, y: r.y - 8, width: r.w + 16, height: r.h + 16, scale: 1 } });
  writeFileSync(`${out}/${name}.png`, Buffer.from(shot.data, "base64"));
}
await element("web-flow-dark", "#flow-analytics", "dark");
await element("web-flow-light", "#flow-analytics", "light");

await capture("web-dark", 1280, 900, "dark", 1.5, 2600);
await capture("web-light", 1280, 900, "light", 1.5, 2600);
await capture("web-mobile", 390, 844, "dark", 2, 1500);

const COLORS = { 31: "#ff7b72", 32: "#3fb950", 33: "#d29922", 36: "#58a6ff" };
const escapeHTML = (s) => s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
function toHTML(text) {
  let open = false;
  const parts = escapeHTML(text).split(/(\u001b\[[0-9;]*m)/);
  let html = "";
  for (const p of parts) {
    const m = /^\u001b\[([0-9;]*)m$/.exec(p);
    if (!m) { html += p; continue; }
    if (open) { html += "</span>"; open = false; }
    if (m[1] === "0" || m[1] === "") continue;
    const style = m[1] === "1" ? "font-weight:700;color:#fff" : m[1] === "2" ? "color:#7d8590" : `color:${COLORS[m[1]] || "inherit"}`;
    html += `<span style="${style}">`;
    open = true;
  }
  return html + (open ? "</span>" : "");
}
async function terminal(name, view, cols = 118) {
  const frame = execFileSync("aiculator", ["top", "--once", "--color", "--width", String(cols), "--view", view], { cwd: tmp, encoding: "utf8" });
  const page = `<!doctype html><meta charset="utf-8"><style>
    html,body{margin:0;background:#0d1117}
    .win{margin:0;border-radius:0;background:#0d1117;color:#e6edf3}
    .bar{height:30px;background:#161b22;display:flex;align-items:center;gap:7px;padding:0 12px}
    .bar i{width:11px;height:11px;border-radius:50%;background:#30363d;display:block}
    .bar b{font:12px -apple-system,sans-serif;color:#7d8590;margin-left:10px;font-weight:400}
    pre{margin:0;padding:14px 16px 18px;font:13px/1.42 Menlo,Monaco,monospace;white-space:pre}
  </style><div class="win"><div class="bar"><i></i><i></i><i></i><b>aiculator top</b></div><pre>${toHTML(frame)}</pre></div>`;
  writeFileSync(`${tmp}/${name}.html`, page);
  await send("Page.navigate", { url: `file://${tmp}/${name}.html` });
  await sleep(300);
  await capture(name, Math.ceil(cols * 7.83) + 34, 500, "dark", 1.5, 1100);
}
await terminal("tui-overview", "overview");
await terminal("tui-tasks", "tasks");
await terminal("tui-flow", "flow");
await terminal("tui-usage", "usage");

ws.close();
chrome.kill();
process.exit(0);
