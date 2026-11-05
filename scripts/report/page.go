package main

// page is the report, one file with nothing loaded from elsewhere, so it works from a CI
// artifact, from GitHub Pages and offline alike.
const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{--bg:#f7f7f5;--panel:#fff;--ink:#1d1d1b;--muted:#6b6b66;--line:#e4e3de;--accent:#3b5bdb;
--good:#2b8a3e;--good-bg:#e6f4ea;--warn:#a86400;--warn-bg:#fff3d6;--bad:#c92a2a;--bad-bg:#fde8e8;--bar:#dcdcd6;
--mono:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
@media (prefers-color-scheme:dark){:root{--bg:#141413;--panel:#1d1d1b;--ink:#ecebe6;--muted:#9a9993;--line:#33332f;
--accent:#91a7ff;--good:#69db7c;--good-bg:#1f3325;--warn:#ffc078;--warn-bg:#3a2e17;--bad:#ff8787;--bad-bg:#3d1f1f;--bar:#3a3a36}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
header{padding:24px 16px 8px;max-width:1200px;margin:0 auto}
h1{font-size:20px;margin:0 0 4px}
h2{font-size:16px;margin:24px 0 8px}
.meta{color:var(--muted);font-size:13px}
.meta code{font-family:var(--mono)}
main{max-width:1200px;margin:0 auto;padding:0 16px 48px}
nav{display:flex;gap:4px;flex-wrap:wrap;border-bottom:1px solid var(--line);margin:16px 0}
nav button{background:none;border:0;border-bottom:2px solid transparent;padding:8px 12px;color:var(--muted);font:inherit;cursor:pointer}
nav button[aria-selected=true]{color:var(--ink);border-color:var(--accent);font-weight:600}
section[hidden]{display:none}
.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:12px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:12px 14px}
.card .k{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.04em}
.card .v{font-size:24px;font-weight:600;font-variant-numeric:tabular-nums}
.status{display:inline-block;padding:2px 10px;border-radius:999px;font-weight:600;font-size:13px}
.ok{background:var(--good-bg);color:var(--good)}.fail{background:var(--bad-bg);color:var(--bad)}.flaky{background:var(--warn-bg);color:var(--warn)}
.problems{background:var(--bad-bg);color:var(--bad);border-radius:8px;padding:8px 16px 8px 32px;margin:12px 0}
.wrap{overflow-x:auto;background:var(--panel);border:1px solid var(--line);border-radius:8px}
table{border-collapse:collapse;width:100%;font-variant-numeric:tabular-nums}
th,td{text-align:left;padding:6px 10px;border-bottom:1px solid var(--line);white-space:nowrap}
th{font-size:12px;color:var(--muted);font-weight:600;position:sticky;top:0;background:var(--panel)}
td.n,th.n{text-align:right}
tr:last-child td{border-bottom:0}
.name{font-family:var(--mono);font-size:13px;white-space:normal;word-break:break-all}
.bar{position:relative;height:8px;width:120px;background:var(--bar);border-radius:4px;overflow:hidden;display:inline-block;vertical-align:middle;margin-right:6px}
.bar i{position:absolute;inset:0 auto 0 0;border-radius:4px}
.bar.good i{background:var(--good)}.bar.warn i{background:var(--warn)}.bar.bad i{background:var(--bad)}
details{background:var(--panel);border:1px solid var(--line);border-radius:8px;margin:8px 0}
details summary{cursor:pointer;padding:8px 12px}
pre{margin:0;padding:12px;overflow:auto;max-height:420px;font:12px/1.45 var(--mono);border-top:1px solid var(--line);white-space:pre-wrap}
input[type=search]{width:100%;max-width:420px;padding:8px 10px;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--ink);font:inherit;margin:8px 0}
.faster{color:var(--good)}.slower{color:var(--bad)}.same{color:var(--muted)}
.charts{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:12px}
.chart{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.chart svg{width:100%;height:80px;display:block}
.chart path{fill:none;stroke:var(--accent);stroke-width:1.5}
.chart circle{fill:var(--accent)}
.chart .lbl{display:flex;justify-content:space-between;color:var(--muted);font-size:12px}
.muted{color:var(--muted)}
.pill{font-size:11px;padding:1px 6px;border-radius:4px}
</style>
</head>
<body>
<header>
<h1>{{.Title}} <span class="status {{if .OK}}ok{{else}}fail{{end}}">{{if .OK}}passed{{else}}{{len .Problems}} problems{{end}}</span></h1>
<div class="meta">{{if .SHA}}commit <code>{{short .SHA}}</code> · {{end}}{{if .Ref}}{{.Ref}} · {{end}}generated {{.Generated.Format "2006-01-02 15:04 UTC"}}</div>
</header>
<main>
{{if .Problems}}<ul class="problems">{{range .Problems}}<li>{{.}}</li>{{end}}</ul>{{end}}
<nav role="tablist">
<button role="tab" aria-selected="true" data-tab="overview">Overview</button>
{{if .Suites}}<button role="tab" aria-selected="false" data-tab="tests">Tests</button>{{end}}
{{if .Coverage}}<button role="tab" aria-selected="false" data-tab="coverage">Coverage</button>{{end}}
{{if .Perf}}<button role="tab" aria-selected="false" data-tab="perf">Performance</button>{{end}}
</nav>

<section id="overview">
<div class="cards">
{{range .Suites}}<div class="card"><div class="k">{{.Name}}</div><div class="v">{{.Passed}}<span class="muted" style="font-size:14px"> passed</span></div>
<div class="muted">{{if .Failed}}<span class="status fail">{{.Failed}} failed</span> {{end}}{{if .Flaky}}<span class="status flaky">{{.Flaky}} flaky</span> {{end}}{{.Skipped}} skipped · {{secs .Seconds}}</div></div>{{end}}
{{with .Coverage}}<div class="card"><div class="k">Coverage</div><div class="v">{{printf "%.1f" .Total}}%</div><div class="bar {{band .Total}}" style="width:100%"><i style="width:{{pctw .Total}}%"></i></div></div>{{end}}
{{with .Perf}}<div class="card"><div class="k">Benchmarks</div><div class="v">{{len .Rows}}</div><div class="muted">{{if .Base}}compared with base on the same runner{{else}}no base to compare with{{end}}</div></div>{{end}}
</div>
{{range .Suites}}{{if .Failures}}<h2>Failures in {{.Name}}</h2>{{range .Failures}}
<details open><summary><span class="status {{if eq .Result "flaky"}}flaky{{else}}fail{{end}}">{{.Result}}</span> <span class="name">{{.Name}}</span> <span class="muted">{{.Package}} · {{secs .Seconds}}</span></summary><pre>{{.Output}}</pre></details>{{end}}{{end}}{{end}}
</section>

{{if .Suites}}<section id="tests" hidden>
<input type="search" placeholder="Filter tests by name or package" data-filter="tests-table">
{{range .Suites}}<h2>{{.Name}}</h2>
<details><summary>Slowest {{len .Slowest}} tests</summary><div class="wrap"><table><tr><th>Test</th><th>Package</th><th class="n">Time</th></tr>
{{range .Slowest}}<tr><td class="name">{{.Name}}</td><td class="name">{{.Package}}</td><td class="n">{{secs .Seconds}}</td></tr>{{end}}</table></div></details>
{{range .Packages}}<details class="pkg"><summary><span class="status {{if eq .Result "fail"}}fail{{else}}ok{{end}}">{{if .Result}}{{.Result}}{{else}}?{{end}}</span> <span class="name">{{.Name}}</span> <span class="muted">{{.Passed}} passed{{if .Failed}}, {{.Failed}} failed{{end}}{{if .Skipped}}, {{.Skipped}} skipped{{end}} · {{secs .Seconds}}</span></summary>
{{if .Output}}<pre>{{.Output}}</pre>{{end}}
<div class="wrap"><table class="tests-table"><tr><th>Test</th><th>Result</th><th class="n">Time</th></tr>
{{range .Tests}}<tr data-k="{{.Name}} {{.Package}}"><td class="name">{{.Name}}</td><td><span class="pill {{if eq .Result "pass"}}ok{{else if eq .Result "skip"}}same{{else if eq .Result "flaky"}}flaky{{else}}fail{{end}}">{{.Result}}</span></td><td class="n">{{secs .Seconds}}</td></tr>{{end}}
</table></div></details>{{end}}{{end}}
</section>{{end}}

{{with .Coverage}}<section id="coverage" hidden>
<p class="muted">Statements reached by unit tests and by the binaries the end-to-end tests start, merged. {{if .HTML}}<a href="{{.HTML}}">Line by line</a>.{{end}}</p>
<input type="search" placeholder="Filter packages or files" data-filter="cover-table">
<div class="wrap"><table class="cover-table"><tr><th>Package / file</th><th>Coverage</th><th class="n">Statements</th></tr>
{{range .Packages}}<tr data-k="{{.Name}}"><td class="name"><b>{{.Name}}</b></td><td><span class="bar {{band .Percent}}"><i style="width:{{pctw .Percent}}%"></i></span>{{printf "%.1f" .Percent}}%</td><td class="n">{{.Covered}} / {{.Stmts}}</td></tr>
{{$p := .Name}}{{range .Files}}<tr data-k="{{$p}}/{{.Name}}"><td class="name" style="padding-left:28px">{{.Name}}</td><td><span class="bar {{band .Percent}}"><i style="width:{{pctw .Percent}}%"></i></span>{{printf "%.1f" .Percent}}%</td><td class="n">{{.Covered}} / {{.Stmts}}</td></tr>{{end}}{{end}}
</table></div>
</section>{{end}}

{{with .Perf}}<section id="perf" hidden>
<p class="muted">{{if .Base}}Base and head were built from their commits and run interleaved on the same runner, so the machine's speed cancels out. A change counts only when the Mann-Whitney U test gives p &lt; 0.05; "~" means no significant change.{{else}}No base results, so only this run's numbers are shown.{{end}}</p>
<div class="wrap"><table><tr><th>Benchmark</th><th>Unit</th><th class="n">Base</th><th class="n">Head</th><th class="n">Change</th><th class="n">p</th><th class="n">Budget</th><th>Runs</th></tr>
{{range .Rows}}<tr><td class="name">{{.Name}}{{range .Problems}}<div class="slower" style="font-family:system-ui">{{.}}</div>{{end}}</td><td>{{.Unit}}</td>
<td class="n">{{with .Base}}{{num .Median}} <span class="muted">±{{.Spread}}%</span>{{end}}</td>
<td class="n">{{if ne .Verdict "missing"}}{{num .Head.Median}} <span class="muted">±{{.Head.Spread}}%</span>{{end}}</td>
<td class="n {{.Verdict}}">{{if .Base}}{{if eq .Verdict "same"}}~{{else}}{{signed .Delta}}{{end}}{{else}}{{.Verdict}}{{end}}</td>
<td class="n">{{if .Base}}{{p3 .P}}{{end}}</td><td class="n">{{if .Budget}}{{num .Budget}}{{end}}</td>
<td class="muted" style="font-size:12px">{{vals .Head.Values}}</td></tr>{{end}}
</table></div>
{{if .Charts}}<h2>Time per operation on master</h2><div class="charts">
{{range .Charts}}<div class="chart"><div class="name">{{.Name}}</div>
<svg viewBox="0 0 300 80" preserveAspectRatio="none" role="img" aria-label="{{.Name}} over time"><path d="{{.Path}}"/>{{range .Dots}}<circle cx="{{.X}}" cy="{{.Y}}" r="2"><title>{{.Label}}</title></circle>{{end}}</svg>
<div class="lbl"><span>{{.Min}} – {{.Max}} ns/op</span><span>latest {{.Latest}}</span></div></div>{{end}}
</div>{{end}}
</section>{{end}}
</main>
<script>
const tabs=document.querySelectorAll('nav button');
function show(id){tabs.forEach(b=>b.setAttribute('aria-selected',b.dataset.tab===id));
document.querySelectorAll('main>section').forEach(s=>s.hidden=s.id!==id);
try{history.replaceState(null,'','#'+id)}catch(e){}}
tabs.forEach(b=>b.addEventListener('click',()=>show(b.dataset.tab)));
if(location.hash&&document.getElementById(location.hash.slice(1)))show(location.hash.slice(1));
document.querySelectorAll('input[data-filter]').forEach(inp=>inp.addEventListener('input',()=>{
const q=inp.value.toLowerCase();
document.querySelectorAll('table.'+inp.dataset.filter+' tr[data-k]').forEach(r=>{r.hidden=q&&!r.dataset.k.toLowerCase().includes(q)});
if(inp.dataset.filter==='tests-table')document.querySelectorAll('details.pkg').forEach(d=>{
const hit=[...d.querySelectorAll('tr[data-k]')].some(r=>!r.hidden);d.hidden=q&&!hit;if(q&&hit)d.open=true})}));
</script>
</body>
</html>
`
