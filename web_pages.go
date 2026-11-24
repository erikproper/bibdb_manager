package main

// The templates of the web UI (web_ui.go): the page ("shell", with its style
// and script) and the fragments it fetches. Look and feel after Home Assistant
// (Erik, 2026-11-22): a coloured top bar, cards on a light (or dark)
// background, clear status colours. Plain JavaScript in the page, no build step
// (the only build stays `go install .`).

const webPages = `
{{define "shell"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Library}} — bibdb</title>
<style>
:root {
  --bg: #fafafa; --card: #ffffff; --text: #212121; --muted: #727272;
  --primary: #03a9f4; --primary-text: #ffffff; --border: #e0e0e0; --hover: #f0f7fc;
  --ok: #43a047; --warn: #ffa600; --busy: #039be5; --danger: #db4437; --chosen: #01579b;
  --shadow: 0 2px 2px rgba(0,0,0,.14), 0 1px 5px rgba(0,0,0,.12);
}
@media (prefers-color-scheme: dark) {
  :root { --bg: #111111; --card: #1c1c1c; --text: #e1e1e1; --muted: #9b9b9b; --hover: #1f2a33;
          --primary: #0288d1; --border: #2f2f2f; --shadow: 0 2px 2px rgba(0,0,0,.5); }
}
* { box-sizing: border-box; }
[hidden] { display: none !important; }
html, body { overflow: hidden; }
html, body { height: 100%; }
body { margin: 0; background: var(--bg); color: var(--text); display: flex; flex-direction: column;
       font: 15px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
header { background: var(--primary); color: var(--primary-text); padding: 0 16px; flex: none;
         display: flex; align-items: last baseline; gap: 24px; height: 56px; } /* tabs at the bottom, the title on their baseline */
header .title { font-size: 20px; font-weight: 400; white-space: nowrap; }
header .conn { align-self: center; margin-left: auto; font-size: 13px; }
.tabs { display: flex; gap: 4px; min-width: 0; overflow-x: auto; scrollbar-width: none; }
.tabs button { background: transparent; color: var(--primary-text); border: 0; border-radius: 8px 8px 0 0;
               padding: 8px 18px; font-size: 14px; cursor: pointer; opacity: .85; white-space: nowrap; flex: none; }
.tabs button:hover { opacity: 1; background: rgba(255,255,255,.15); }
.tabs button.active { background: var(--bg); color: var(--text); opacity: 1; }
main { flex: 1; min-height: 0; }
.panel { height: 100%; overflow: auto; padding: 16px; }
.panel.fill { display: flex; flex-direction: column; overflow: hidden; }
.narrow { max-width: 1100px; margin: 0 auto; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 16px; }
.card { background: var(--card); border-radius: 12px; box-shadow: var(--shadow); padding: 16px; margin-bottom: 16px; }
.card h2 { margin: 0 0 12px; font-size: 16px; font-weight: 500; }
.state { display: flex; align-items: center; gap: 12px; }
.dot { width: 14px; height: 14px; border-radius: 50%; flex: none; }
.dot.ok { background: var(--ok); } .dot.warn { background: var(--warn); } .dot.busy { background: var(--busy); }
.muted { color: var(--muted); }
table.list { width: 100%; border-collapse: collapse; }
table.list td, table.list th { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--border); vertical-align: top; }
table.list th { color: var(--muted); font-weight: 500; }
td.num { text-align: right; font-variant-numeric: tabular-nums; }
td.field { color: var(--muted); width: 160px; white-space: nowrap; }
td.year { width: 4em; color: var(--muted); }
a { color: var(--primary); }
input[type=search] { width: 100%; padding: 10px 12px; font-size: 16px; border: 1px solid var(--border);
                     border-radius: 8px; background: var(--card); color: var(--text); }
button.action { background: var(--primary); color: var(--primary-text); border: 0; border-radius: 8px;
                padding: 8px 16px; font-size: 14px; cursor: pointer; }
button.action.danger { background: var(--danger); }
button.plain { background: transparent; color: var(--primary); border: 1px solid var(--border); border-radius: 8px;
               padding: 4px 12px; font-size: 13px; cursor: pointer; }
.chip { display: inline-block; padding: 2px 10px; border-radius: 12px; border: 1px solid var(--border);
        margin: 0 6px 6px 0; font-size: 13px; }
.key { font-family: ui-monospace, Menlo, monospace; font-size: 13px; }
.ref { line-height: 1.5; }
.bib { font-family: ui-monospace, Menlo, monospace; font-size: 13px; }
.bib table { margin-left: 2ch; width: calc(100% - 2ch); border-collapse: collapse; }
.bib td { padding: 1px 4px; vertical-align: top; }
.bib td.field { width: 14ch; }
.bib td.eq { width: 1ch; color: var(--muted); }
.line { display: flex; align-items: center; gap: 8px; margin: 0 0 12px; flex-wrap: wrap; }
.line .label { color: var(--muted); min-width: 4.5em; }
button.pdf { display: inline-flex; align-items: center; gap: 6px; background: transparent; color: var(--primary);
             border: 1px solid var(--border); border-radius: 8px; padding: 4px 10px; cursor: pointer; font-size: 14px; }
a.open { text-decoration: none; font-weight: 600; padding: 0 4px; }

/* Search */
.searchbar { flex: none; display: flex; gap: 12px; align-items: center; margin-bottom: 8px; }
.searchbar input { flex: 1; }
select.groupadd { font-size: 13px; padding: 3px 6px; border: 1px solid var(--border); border-radius: 8px;
                  background: var(--card); color: var(--primary); max-width: 220px; }
.groupchip { display: inline-flex; align-items: center; gap: 4px; background: var(--chosen); color: #fff; border-radius: 14px;
             padding: 2px 4px 2px 10px; font-size: 13px; white-space: nowrap; }
.groupchip .n { opacity: .75; }
.tabs button .tabx { margin-left: 8px; opacity: .7; padding: 0 2px; border-radius: 4px; }
.tabs button .tabx:hover { opacity: 1; background: rgba(0,0,0,.15); }
.tabs button.seltab { font-style: italic; }
.selbar { flex: none; display: flex; gap: 10px; align-items: center; margin-bottom: 8px; font-size: 13px; flex-wrap: wrap; }
.selbar .selmsg { color: var(--muted); }
button.toggle { border: 1px solid var(--border); background: var(--card); color: var(--text); border-radius: 14px;
                padding: 2px 10px; font-size: 13px; cursor: pointer; white-space: nowrap; }
button.toggle.on { background: var(--chosen); border-color: var(--chosen); color: #fff; }
.tick { display: inline-block; width: 16px; height: 16px; border: 1.5px solid var(--muted); border-radius: 4px;
        vertical-align: middle; cursor: pointer; margin-right: 4px; text-align: center; line-height: 13px; font-size: 12px; color: transparent; }
.selpick { display: inline-flex; align-items: center; gap: 2px; margin-right: 14px; cursor: pointer; font-size: 14px; }
.tick.on { background: #fbc02d; border-color: #f9a825; color: #000; }
.tick.kept { background: var(--primary); border-color: var(--primary); color: #fff; }
.groupchip button { background: transparent; border: 0; color: #fff; cursor: pointer; font-size: 14px; padding: 0 4px; line-height: 1; }
.searchinfo > *:not(.hint) { white-space: nowrap; flex: none; }
.searchinfo { flex: none; display: flex; gap: 12px; align-items: center; margin-bottom: 8px; font-size: 13px; }
.gridbox { flex: 1; min-height: 0; overflow: auto; background: var(--card); border-radius: 12px; box-shadow: var(--shadow); }
table.grid { border-collapse: separate; border-spacing: 0; table-layout: fixed; font-size: 13px; }
table.grid th, table.grid td { white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
                               padding: 4px 8px; border-bottom: 1px solid var(--border); text-align: left; }
table.grid th { position: sticky; top: 0; background: var(--card); z-index: 1; color: var(--muted); font-weight: 500;
                cursor: pointer; user-select: none; border-right: 1px solid var(--border); }
table.grid th.drop { box-shadow: inset 3px 0 0 var(--primary); }
table.grid th .grip { position: absolute; top: 0; right: -3px; width: 7px; height: 100%; cursor: col-resize; z-index: 2; }
table.grid tbody tr:hover { background: var(--hover); }
table.grid tbody tr, #mbody table.list tr:has(a.open) { cursor: pointer; }
#mbody table.list tr:has(a.open):hover { background: var(--hover); }
table.grid td.inh { color: var(--muted); font-style: italic; }
table.grid td.op { padding: 2px 4px; text-align: left; }
table.grid td.op button.rowpdf { margin-left: 4px; padding: 0 4px; vertical-align: middle; }
table.grid td.op button.rowpdf svg { vertical-align: -2px; }
table.grid td.op button { background: transparent; border: 1px solid var(--border); border-radius: 6px; color: var(--primary);
                          cursor: pointer; padding: 0 6px; font-size: 13px; line-height: 18px; }
.loading { padding: 12px; }

/* Log */
.logcard { flex: 1; min-height: 0; display: flex; flex-direction: column; margin: 0; }
pre.log { flex: 1; min-height: 0; overflow: auto; margin: 0; white-space: pre-wrap; font: 12px/1.4 ui-monospace, Menlo, monospace; }

/* Pop-ups */
#overlay { position: fixed; inset: 0; background: rgba(0,0,0,.35); display: none; z-index: 10;
           align-items: flex-start; justify-content: center; padding: 40px 16px; }
#overlay.open { display: flex; }
.modal { background: var(--bg); border-radius: 12px; box-shadow: 0 8px 32px rgba(0,0,0,.35);
         width: min(980px, 100%); max-height: calc(100vh - 80px); display: flex; flex-direction: column; }
.modal-bar { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: 1px solid var(--border); flex: none; }
.modal-bar .mtitle { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.modal-bar button { background: transparent; border: 0; color: var(--text); font-size: 18px; cursor: pointer; padding: 2px 8px; border-radius: 6px; }
.modal-bar button:hover { background: var(--hover); }
.modal-body { overflow: auto; padding: 16px; }
.columns { column-width: 160px; column-gap: 16px; }
.columns label { display: block; margin: 2px 0; font-size: 14px; break-inside: avoid; white-space: nowrap; }
#banner { display: none; background: var(--warn); color: #000; padding: 8px 16px; flex: none; }
</style>
</head>
<body>
<header>
  <span class="title">{{.Library}}</span>
  <nav class="tabs" id="tabs">
    <button data-tab="status">Status</button>
    <button data-tab="search">Search</button>
    <button data-tab="stats">Statistics</button>
    <button data-tab="log">Log</button>
  </nav>
  <span class="conn" id="conn"></span>
</header>
<div id="banner"></div>
<main>
  <section class="panel" id="panel-status" hidden><div class="narrow" id="status"><p class="muted">Loading…</p></div></section>
  <section class="panel fill" id="panel-search" hidden></section>
  <section class="panel" id="panel-stats" hidden><div class="narrow"><div class="card"><h2>Library statistics <button class="plain" id="statsrefresh">Refresh</button></h2><div id="stats"><p class="muted">Loading…</p></div></div></div></section>
  <section class="panel fill" id="panel-log" hidden><div class="card logcard"><h2>Log <span class="muted">(the service and its jobs)</span>
      <button class="plain" id="logend">To the end</button></h2><pre class="log" id="log"></pre></div></section>
</main>
<template id="searchtpl">
  <div class="searchbar"><input type="search" class="q" placeholder="Filter: words from the key, alias, title, authors, editors, year, venue, DOI (empty: all entries)" autocomplete="off"></div>
  <div class="selbar" hidden><b class="selname"></b><span class="selinfo muted"></span>
    <button class="action selsave">Save</button><button class="action selsync">Sync</button><button class="plain selclose">Close</button>
    <span class="selmsg"></span></div>
  <div class="searchinfo"><span class="count muted"></span><button class="toggle selonly" hidden title="Show only the selected entries (yellow and blue)">Selection</button>
    <select class="groupadd" title="Limit the list to the entries in a group"></select><span class="groupchips" style="display:inline-flex;gap:6px"></span>
    <button class="plain choosecols">Columns…</button><button class="plain resetcols">Default layout</button>
    <span class="muted hint">Click a header to sort, drag it to move, drag its edge to resize. Grey: taken from the crossref parent.</span></div>
  <div class="gridbox"><table class="grid"><colgroup></colgroup><thead><tr></tr></thead><tbody></tbody></table><div class="loading muted gridmore"></div></div>
</template>
<div id="overlay"><div class="modal">
  <div class="modal-bar"><button id="mback" title="Back">‹</button><span class="mtitle" id="mtitle"></span><button id="mclose" title="Close (Esc)">×</button></div>
  <div class="modal-body" id="mbody"></div>
</div></div>
<script>
"use strict";
const BASE = "{{.Base}}";
const $ = (id) => document.getElementById(id);

// ---- preferences: kept by the service in web_ui.json in the local DB folder ----
let prefs = {}, prefsTimer;
async function loadPrefs() { try { prefs = await get("/api/prefs", "json") || {}; } catch (e) { prefs = {}; } }
function load(name, dflt) { return prefs[name] !== undefined ? prefs[name] : dflt; }
function save(name, v) { // written shortly after the last change
  prefs[name] = JSON.parse(JSON.stringify(v));
  clearTimeout(prefsTimer);
  prefsTimer = setTimeout(() => { fetch(BASE + "/api/prefs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(prefs) }).catch(lost); }, 500);
}

// ---- connection ----
let stopped = false;
async function get(path, kind) {
  const r = await fetch(BASE + path, { cache: "no-store" });
  if (!r.ok) throw new Error(r.status + " " + (await r.text()));
  $("conn").textContent = "";
  return kind === "json" ? r.json() : r.text();
}
function lost(e) {
  if (e instanceof TypeError) {
    $("conn").textContent = stopped ? "service stopped" : "no connection to the service";
  }
}

// ---- tabs: all panels stay in the page, so each keeps its state ----
const onShow = {}, timers = {};
let current = "";
function showTab(name) {
  if (!$("panel-" + name)) name = "search";
  current = name;
  for (const b of document.querySelectorAll(".tabs button")) b.classList.toggle("active", b.dataset.tab === name);
  for (const p of document.querySelectorAll(".panel")) p.hidden = p.id !== "panel-" + name;
  history.replaceState(null, "", "#" + name);
  if (onShow[name]) onShow[name]();
}
$("tabs").addEventListener("click", (e) => {
  const b = e.target.closest("button[data-tab]");
  if (!b) return;
  if (e.target.closest(".tabx")) { e.stopPropagation(); views[b.dataset.tab] && views[b.dataset.tab].closeSelect(); return; }
  showTab(b.dataset.tab);
});
function every(name, ms, fn) { // fn runs every ms while the tab is shown
  clearInterval(timers[name]);
  timers[name] = setInterval(() => { if (current === name && !stopped) fn(); }, ms);
}

// ---- status ----
async function loadStatus() { try { $("status").innerHTML = await get("/frag/status"); } catch (e) { lost(e); } }
onShow.status = loadStatus;
every("status", 3000, loadStatus);

// ---- statistics: counted when asked ----
let statsLoaded = false;
async function loadStats() { $("stats").innerHTML = '<p class="muted">Counting…</p>'; try { $("stats").innerHTML = await get("/frag/stats"); statsLoaded = true; } catch (e) { lost(e); } }
onShow.stats = () => { if (!statsLoaded) loadStats(); };
$("statsrefresh").onclick = loadStats;

// ---- log: new lines are fetched while the tab is shown ----
let logNext = 0, logLines = [];
async function loadLog() {
  try {
    const d = await get("/api/log?from=" + logNext, "json");
    const box = $("log");
    const atEnd = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
    if (d.next < logNext) logLines = [];
    logLines.push(...d.lines);
    if (logLines.length > 3000) logLines.splice(0, logLines.length - 3000);
    logNext = d.next;
    $("log").textContent = logLines.join("\n") + (d.current ? "\n" + d.current : "");
    if (atEnd) box.scrollTop = box.scrollHeight;
  } catch (e) { lost(e); }
}
let logShown = false;
onShow.log = async () => { await loadLog(); if (!logShown) { logShown = true; $("log").scrollTop = $("log").scrollHeight; } };
$("logend").onclick = () => { $("log").scrollTop = $("log").scrollHeight; };
every("log", 2000, loadLog);

// ---- search: a BibDesk-like table, filtered, sorted and paged by the service ----
// The Search tab and each Select tab are a SearchView; the column layout is shared.
const defaultLayout = { cols: ["year", "author", "title", "booktitle", "journal", "entrytype"], widths: {}, sort: "", desc: false };
const defaultWidth = { year: 60, author: 260, title: 420, booktitle: 240, journal: 220, citekey: 220, entrytype: 110, groups: 200 };
let layout = JSON.parse(JSON.stringify(defaultLayout)), layoutVersion = 0;
let fields = [];
const PAGE = 200;
const PDFICON = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/></svg>';
function width(c) { return layout.widths[c] || defaultWidth[c] || 160; }
function saveLayout() { layoutVersion++; save("layout", layout); }
function esc(s) { return String(s).replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[ch]); }
const views = {}; // by tab name

class SearchView {
  constructor(panel, sel) { // sel: a Select tab's info, or null
    this.panel = panel; this.sel = sel;
    panel.appendChild($("searchtpl").content.cloneNode(true));
    const q = (c) => panel.querySelector("." + c);
    this.el = { q: q("q"), count: q("count"), groupadd: q("groupadd"), groupchips: q("groupchips"), gridbox: q("gridbox"),
                grid: q("grid"), gridmore: q("gridmore"), selbar: q("selbar"), selonly: q("selonly"),
                selinfo: q("selinfo"), selmsg: q("selmsg"), selname: q("selname") };
    this.rows = []; this.total = 0; this.gen = 0; this.busy = false; this.query = ""; this.groups = new Set();
    this.only = false; this.started = false; this.seenLayout = -1;
    this.opW = sel ? 92 : 64;
    const el = this.el;
    el.gridbox.addEventListener("scroll", () => {
      if (this.rows.length < this.total && el.gridbox.scrollTop + el.gridbox.clientHeight > el.gridbox.scrollHeight - 600) this.fetchRows(false);
    });
    let typing;
    el.q.addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(() => { this.query = el.q.value.trim(); this.reload(); }, 300); });
    el.q.addEventListener("keydown", (e) => { if (e.key === "Enter") { clearTimeout(typing); this.query = el.q.value.trim(); this.reload(); } });
    // A click anywhere on a row opens the entry, but on its buttons and tick (they act
    // themselves) and when it ends selecting text.
    el.grid.addEventListener("click", (e) => {
      const tr = e.target.closest("tbody tr[data-key]");
      if (!tr || e.target.closest(".tick, button, a") || String(window.getSelection())) return;
      openEntry(tr.dataset.key);
    });
    el.groupadd.onchange = () => { const g = el.groupadd.value; if (g) { this.groups.add(g); this.reload(); } };
    q("resetcols").onclick = () => { layout = JSON.parse(JSON.stringify(defaultLayout)); saveLayout(); this.reload(); };
    q("choosecols").onclick = () => this.chooseColumns();
    if (sel) {
      el.selbar.hidden = false; el.selonly.hidden = false;
      el.selonly.onclick = () => { this.only = !this.only; el.selonly.classList.toggle("on", this.only); this.reload(); };
      el.grid.addEventListener("click", (e) => { const t = e.target.closest(".tick"); if (t) { e.stopPropagation(); this.toggle(t); } });
      q("selsave").onclick = () => this.selAction("save");
      q("selsync").onclick = () => this.selAction("sync");
      q("selclose").onclick = () => this.closeSelect();
      this.setInfo(sel);
    }
  }
  show() {
    if (!this.started || this.seenLayout !== layoutVersion) { this.started = true; this.reload(); }
    this.el.q.focus();
  }
  header() {
    this.seenLayout = layoutVersion;
    const g = this.el.grid, cg = g.querySelector("colgroup"), tr = g.querySelector("thead tr");
    cg.innerHTML = '<col style="width:' + this.opW + 'px">' + layout.cols.map((c) => '<col style="width:' + width(c) + 'px">').join("");
    // The first column: in a Select tab "Selection" (sorts on it), else unnamed (sorts on having a PDF).
    const first = this.sel ? "sel" : "pdf";
    tr.innerHTML = '<th title="' + (this.sel ? "Sort on being selected" : "Sort on having a PDF") + '">' + (this.sel ? "Selection" : "") +
      (this.sortKey() === first ? ((this.selSort ? this.selDesc : layout.desc) ? " ▼" : " ▲") : "") + "</th>";
    tr.firstChild.onclick = () => this.sortOn(first);
    layout.cols.forEach((c, i) => {
      const th = document.createElement("th");
      th.textContent = c + (this.sortKey() === c ? (layout.desc ? " ▼" : " ▲") : "");
      th.title = c;
      th.draggable = true;
      const grip = document.createElement("span");
      grip.className = "grip";
      grip.onmousedown = (ev) => this.resizeStart(ev, c, i);
      grip.onclick = (ev) => ev.stopPropagation();
      grip.draggable = false;
      th.appendChild(grip);
      th.onclick = () => this.sortOn(c);
      th.ondragstart = (ev) => { ev.dataTransfer.setData("text/plain", String(i)); ev.dataTransfer.effectAllowed = "move"; };
      th.ondragover = (ev) => { ev.preventDefault(); th.classList.add("drop"); };
      th.ondragleave = () => th.classList.remove("drop");
      th.ondrop = (ev) => { ev.preventDefault(); th.classList.remove("drop"); this.moveCol(+ev.dataTransfer.getData("text/plain"), i); };
      tr.appendChild(th);
    });
    this.fitWidth();
  }
  fitWidth() { this.el.grid.style.width = this.opW + layout.cols.reduce((s, c) => s + width(c), 0) + "px"; }
  resizeStart(ev, c, i) {
    ev.preventDefault(); ev.stopPropagation();
    const col = this.el.grid.querySelectorAll("col")[i + 1], x0 = ev.clientX, w0 = width(c);
    const move = (e) => { layout.widths[c] = Math.max(40, w0 + e.clientX - x0); col.style.width = layout.widths[c] + "px"; this.fitWidth(); };
    const up = () => { document.removeEventListener("mousemove", move); document.removeEventListener("mouseup", up); saveLayout(); this.seenLayout = layoutVersion; };
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
  }
  moveCol(from, to) {
    if (from === to || isNaN(from)) return;
    const c = layout.cols.splice(from, 1)[0];
    layout.cols.splice(to, 0, c);
    saveLayout();
    this.header();
    this.renderRows(0);
  }
  sortKey() { return this.selSort ? "sel" : layout.sort; }
  sortOn(c) {
    if (c === "sel") { // a Select tab's own sorting, not in the shared layout
      if (this.selSort) this.selDesc = !this.selDesc; else { this.selSort = true; this.selDesc = false; }
      this.header(); this.fetchRows(true); return;
    }
    this.selSort = false;
    if (layout.sort === c) layout.desc = !layout.desc;
    else { layout.sort = c; layout.desc = c === "year"; }
    saveLayout();
    this.reload();
  }
  chooseColumns() {
    const html = '<div class="columns">' + fields.map((f) => '<label><input type="checkbox" value="' + esc(f) + '"' +
      (layout.cols.includes(f) ? " checked" : "") + "> " + esc(f) + "</label>").join("") + "</div>";
    popup("Columns", html, false);
    for (const box of $("mbody").querySelectorAll("input")) box.onchange = () => {
      if (box.checked) layout.cols.push(box.value); else layout.cols = layout.cols.filter((c) => c !== box.value);
      if (layout.sort !== "pdf" && !layout.cols.includes(layout.sort)) layout.sort = "";
      saveLayout(); this.reload();
    };
  }
  showGroups(groups) { // the group filter: a drop-down adds one, a dark-blue chip (×) removes it
    const add = this.el.groupadd, chips = this.el.groupchips;
    const open = (groups || []).filter((g) => !this.groups.has(g.name));
    add.innerHTML = '<option value="">' + (this.groups.size ? "+ group" : "Group filter…") + "</option>" +
      open.map((g) => '<option value="' + esc(g.name) + '">' + esc(g.name) + " (" + g.n + ")</option>").join("");
    add.hidden = open.length === 0;
    chips.innerHTML = "";
    for (const g of groups || []) {
      if (!this.groups.has(g.name)) continue;
      const c = document.createElement("span");
      c.className = "groupchip";
      c.innerHTML = esc(g.name) + ' <span class="n">' + g.n + '</span><button title="Remove this group filter">×</button>';
      c.querySelector("button").onclick = () => { this.groups.delete(g.name); this.reload(); };
      chips.appendChild(c);
    }
  }
  tickHTML(state) {
    const cls = state === 1 ? "tick on" : state === 2 ? "tick kept" : "tick";
    const title = state === 1 ? "Selected; click to unselect" : state === 2 ? "Unselected, its local key kept until saved; click to select it again" : "Click to select";
    return '<span class="' + cls + '" title="' + title + '">✓</span>';
  }
  renderRows(from) { // rows from 'from' on (0: all again)
    const body = this.el.grid.querySelector("tbody");
    if (from === 0) body.innerHTML = "";
    const html = [];
    for (let n = from; n < this.rows.length; n++) {
      const r = this.rows[n];
      html.push('<tr data-key="' + esc(r.k) + '"><td class="op">' + (this.sel ? this.tickHTML(r.t) : "") +
        '<button data-entry="' + esc(r.k) + '" title="Open ' + esc(r.k) + '">›</button>' +
        (r.pdf ? '<button class="rowpdf" data-pdf="' + esc(r.k) + '" title="Open the PDF">' + PDFICON + "</button><span></span>" : "") + "</td>");
      for (const c of layout.cols) {
        const v = r.cells[c] || "";
        html.push("<td" + (r.inh[c] ? ' class="inh"' : "") + ' title="' + esc(v) + '">' + esc(v) + "</td>");
      }
      html.push("</tr>");
    }
    body.insertAdjacentHTML("beforeend", html.join(""));
    const filtered = this.query || this.groups.size || this.only;
    this.el.count.textContent = this.total.toLocaleString() + (this.total === 1 ? " entry" : " entries") + (filtered ? " match" + (this.total === 1 ? "es" : "") : "");
    this.el.gridmore.textContent = this.rows.length < this.total ? "Scroll for more (" + this.rows.length.toLocaleString() + " shown)…" : "";
  }
  async fetchRows(reset) {
    if (this.busy && !reset) return;
    const my = reset ? ++this.gen : this.gen;
    this.busy = true;
    const from = reset ? 0 : this.rows.length;
    if (reset) this.el.gridmore.textContent = "Loading…";
    const p = new URLSearchParams({ q: this.query, groups: [...this.groups].join("\n"), sort: this.sortKey(),
                                    desc: (this.selSort ? this.selDesc : layout.desc) ? "1" : "0",
                                    cols: layout.cols.join(","), from, n: PAGE });
    if (this.sel) { p.set("sel", this.sel.id); if (this.only) p.set("only", "1"); }
    if (!fields.length) p.set("fields", "1");
    try {
      const d = await get("/api/entries?" + p, "json");
      if (my !== this.gen) return;
      if (d.fields) fields = d.fields;
      if (reset && d.groups) this.showGroups(d.groups);
      this.total = d.total;
      const got = (d.rows || []).map((r) => {
        const cells = {}, inh = {};
        layout.cols.forEach((c, i) => { cells[c] = r.c[i]; });
        for (const i of r.i || []) inh[layout.cols[i]] = true;
        return { k: r.k, cells, inh, pdf: !!r.p, t: r.t || 0 };
      });
      if (reset) this.rows = got; else this.rows.push(...got);
      this.renderRows(reset ? 0 : this.rows.length - got.length);
    } catch (e) { lost(e); if (my === this.gen) this.el.gridmore.textContent = String(e.message || e); }
    finally { if (my === this.gen) this.busy = false; }
  }
  reload() { this.header(); this.el.gridbox.scrollTop = 0; this.fetchRows(true); }

  // ---- Select tabs ----
  setInfo(info) {
    this.sel = Object.assign(this.sel || {}, info);
    const el = this.el;
    el.selname.textContent = info.name + ".keys";
    el.selname.title = info.dir;
    el.selinfo.textContent = info.selected + " selected" + (info.kept ? ", " + info.kept + " kept (blue)" : "") +
      (info.unknown ? ", " + info.unknown + " not in the library (kept)" : "") +
      (info.changes ? " — " + info.changes + " change" + (info.changes === 1 ? "" : "s") + " not saved" : "");
    el.selmsg.textContent = info.message || "";
  }
  async post(action, body) {
    const r = await fetch(BASE + "/api/select/" + this.sel.id + "/" + action, { method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams(body || {}) });
    if (!r.ok) throw new Error(await r.text());
    return r.status === 204 ? null : r.json();
  }
  setTick(key, state) { // a tick changed elsewhere (an entry's pop-up)
    const row = this.rows.find((r) => r.k === key);
    if (row) row.t = state;
    const tr = this.el.grid.querySelector('tr[data-key="' + CSS.escape(key) + '"] .tick');
    if (tr) tr.outerHTML = this.tickHTML(state);
  }
  async toggle(tick) {
    const tr = tick.closest("tr[data-key]");
    try {
      const d = await this.post("toggle", { key: tr.dataset.key });
      const row = this.rows.find((r) => r.k === tr.dataset.key);
      if (row) row.t = d.state;
      tick.outerHTML = this.tickHTML(d.state);
      this.setInfo(d.info);
      if (d.error) alert(d.error);
    } catch (e) { lost(e); alert(e.message || e); }
  }
  async selAction(action) {
    try { const d = await this.post(action); this.setInfo(d.info); if (action === "save" || action === "sync") this.reload(); }
    catch (e) { lost(e); alert(e.message || e); }
  }
  async closeSelect() {
    if (this.sel.changes > 0) {
      popup("Close " + this.sel.name, '<p>' + this.sel.changes + " change" + (this.sel.changes === 1 ? "" : "s") + " to " + esc(this.sel.name) +
        ".keys not saved.</p><p><button class=\"action\" id=\"cs-save\">Save and close</button> <button class=\"action danger\" id=\"cs-discard\">Close without saving</button> <button class=\"plain\" id=\"cs-cancel\">Cancel</button></p>", false);
      $("cs-cancel").onclick = closePopup;
      $("cs-save").onclick = async () => { closePopup(); try { await this.post("save"); await this.post("close"); dropSelectTab(this.sel.id); } catch (e) { alert(e.message || e); } };
      $("cs-discard").onclick = async () => { closePopup(); try { await this.post("close", { discard: "1" }); dropSelectTab(this.sel.id); } catch (e) { alert(e.message || e); } };
      return;
    }
    try { await this.post("close"); } catch (e) { /* already closed */ }
    dropSelectTab(this.sel.id);
  }
}

// ---- Select tabs: opened by -select on the command line; found by asking the service ----
function dropSelectTab(id) {
  const name = "sel-" + id;
  const b = document.querySelector('.tabs button[data-tab="' + name + '"]');
  if (b) b.remove();
  if ($("panel-" + name)) $("panel-" + name).remove();
  delete views[name];
  if (current === name) showTab("search");
}
// selectLabels names the Select tabs: the file name, with as many of its folders
// in front as it takes to tell tabs of files with the same name apart.
function selectLabels(list) {
  const parts = list.map((info) => info.dir.split("/").filter(Boolean));
  const depth = list.map(() => 0);
  const label = (i) => (depth[i] ? parts[i].slice(-depth[i]).join("/") + "/" : "") + list[i].name;
  for (let changed = true; changed; ) {
    changed = false;
    const seen = {};
    list.forEach((_, i) => { (seen[label(i)] = seen[label(i)] || []).push(i); });
    for (const same of Object.values(seen)) {
      if (same.length < 2) continue;
      for (const i of same) if (depth[i] < parts[i].length) { depth[i]++; changed = true; }
    }
  }
  return list.map((_, i) => label(i));
}
async function refreshSelects() {
  let list;
  try { list = await get("/api/selects", "json"); } catch (e) { lost(e); return; }
  list = list || [];
  const labels = selectLabels(list);
  const open = new Set();
  for (const [i, info] of list.entries()) {
    const name = "sel-" + info.id;
    open.add(name);
    const tabHTML = "Select: " + esc(labels[i]) + '<span class="tabx" title="Close this tab">×</span>';
    if (views[name]) {
      views[name].setInfo(info);
      const b = document.querySelector('.tabs button[data-tab="' + name + '"]');
      if (b && b.dataset.label !== labels[i]) { b.innerHTML = tabHTML; b.dataset.label = labels[i]; }
      continue;
    }
    const b = document.createElement("button");
    b.dataset.tab = name;
    b.dataset.label = labels[i];
    b.className = "seltab";
    b.title = info.dir + "/" + info.name + ".keys";
    b.innerHTML = tabHTML;
    $("tabs").insertBefore(b, document.querySelector('.tabs button[data-tab="stats"]')); // after Search and the earlier Select tabs
    const panel = document.createElement("section");
    panel.className = "panel fill";
    panel.id = "panel-" + name;
    panel.hidden = true;
    document.querySelector("main").appendChild(panel);
    views[name] = new SearchView(panel, info);
    onShow[name] = () => views[name].show();
  }
  for (const name of Object.keys(views)) if (name.startsWith("sel-") && !open.has(name)) dropSelectTab(name.slice(4));
}
setInterval(() => { if (!stopped) refreshSelects(); }, 3000);
window.addEventListener("hashchange", async () => {
  const name = location.hash.slice(1);
  if (name.startsWith("sel-") && !$("panel-" + name)) await refreshSelects();
  if (name && name !== current) showTab(name);
});

// ---- pop-ups: an entry, an author; Back goes to the previous one ----
let stack = [];
function popup(title, html, push) {
  if (!push) stack = [];
  $("mtitle").textContent = title;
  $("mbody").innerHTML = html;
  $("mbody").scrollTop = 0;
  for (const a of $("mbody").querySelectorAll('a[href^="http"]')) { a.target = "_blank"; a.rel = "noopener"; }
  $("mback").style.visibility = stack.length > 1 ? "visible" : "hidden";
  $("overlay").classList.add("open");
}
async function show(item) {
  try {
    const html = await get(item.kind === "entry" ? "/frag/entry/" + encodeURIComponent(item.id) : "/frag/author/" + encodeURIComponent(item.id));
    popup(item.title, html, true);
    if (item.kind === "entry") showSelectTicks(item.id);
    else if (item.origin && item.origin.startsWith("sel-")) showListTicks(item.origin.slice(4));
  } catch (e) { lost(e); popup(item.title, '<p class="muted">' + esc(e.message || e) + "</p>", true); }
}

// In an author's pop-up opened from a Select tab: a tick before each entry, for
// that tab's selection.
async function showListTicks(selID) {
  const links = [...$("mbody").querySelectorAll("table.list a.open[data-entry]")];
  if (!links.length) return;
  let states;
  try { states = await get("/api/selstates?sel=" + selID + "&keys=" + encodeURIComponent(links.map((a) => a.dataset.entry).join(",")), "json"); }
  catch (e) { return; }
  const view = views["sel-" + selID];
  const label = view ? document.querySelector('.tabs button[data-tab="sel-' + selID + '"]').dataset.label : "";
  const head = $("mbody").querySelectorAll(".card h2");
  if (head.length > 1 && label) head[1].insertAdjacentHTML("beforeend", ' <span class="muted">— ticks: Select: ' + esc(label) + "</span>");
  for (const a of links) {
    const key = a.dataset.entry, tr = a.closest("tr");
    tr.insertAdjacentHTML("afterbegin", '<td style="width:28px"><span class="selpick" data-sel="' + esc(selID) + '" data-key="' + esc(key) + '">' + SearchView.prototype.tickHTML(states[key] || 0) + "</span></td>");
    const pick = tr.firstElementChild.firstElementChild;
    pick.onclick = () => selPick(pick);
  }
}

// selPick turns a tick in a pop-up (as in the Select tab itself).
async function selPick(pick) {
  const view = views["sel-" + pick.dataset.sel];
  try {
    const r = await fetch(BASE + "/api/select/" + pick.dataset.sel + "/toggle", { method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ key: pick.dataset.key }) });
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json();
    pick.querySelector(".tick").outerHTML = SearchView.prototype.tickHTML(d.state);
    if (view) { view.setInfo(d.info); view.setTick(pick.dataset.key, d.state); }
    if (d.error) alert(d.error);
  } catch (e) { lost(e); alert(e.message || e); }
}

// In an entry's pop-up: a tick per open Select tab, as in the tab itself.
async function showSelectTicks(key) {
  let states;
  try { states = await get("/api/selstate?key=" + encodeURIComponent(key), "json"); } catch (e) { return; }
  if (!states || !states.length || !$("overlay").classList.contains("open")) return;
  const labels = selectLabels(states);
  const line = document.createElement("div");
  line.className = "line";
  line.innerHTML = '<span class="label">Select</span>' + states.map((st, i) =>
    '<span class="selpick" data-sel="' + esc(st.id) + '" data-key="' + esc(st.key) + '">' + SearchView.prototype.tickHTML(st.state) + esc(labels[i]) + "</span>").join("");
  const groups = $("mbody").querySelector(".line");
  if (groups) groups.after(line); else $("mbody").prepend(line);
  for (const pick of line.querySelectorAll(".selpick")) pick.onclick = () => selPick(pick);
}
// A chain of pop-ups remembers the tab it was opened from (origin): from a Select
// tab, an author's entries can be ticked for that tab's selection.
function open1(kind, id, title) {
  const origin = stack.length ? stack[0].origin : current;
  const item = { kind, id, title, origin };
  stack.push(item);
  show(item);
}
function openEntry(key) { open1("entry", key, key); }
function closePopup() { stack = []; $("overlay").classList.remove("open"); }
$("mclose").onclick = closePopup;
// In a pop-up's list of entries (an author's), a click on a row opens the entry,
// but on its links and tick, and when it ends selecting text.
$("mbody").addEventListener("click", (e) => {
  const tr = e.target.closest("table.list tr");
  const open = tr && tr.querySelector("a.open[data-entry]");
  if (!open || e.target.closest("a, button, .selpick") || String(window.getSelection())) return;
  openEntry(open.dataset.entry);
});
$("mback").onclick = () => { if (stack.length > 1) { stack.pop(); show(stack[stack.length - 1]); } };
$("overlay").addEventListener("mousedown", (e) => { if (e.target === $("overlay")) closePopup(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && $("overlay").classList.contains("open")) closePopup(); });

// ---- clicks inside fragments ----
document.addEventListener("click", async (e) => {
  const el = e.target.closest("[data-entry],[data-author],[data-pdf],#stop");
  if (!el) return;
  e.preventDefault();
  if (el.dataset.entry) openEntry(el.dataset.entry);
  else if (el.dataset.author) open1("author", el.dataset.author, el.dataset.name || el.dataset.author);
  else if (el.dataset.pdf) {
    try { const r = await fetch(BASE + "/pdf/" + encodeURIComponent(el.dataset.pdf), { method: "POST" });
          const note = el.nextElementSibling;
          if (note && !el.classList.contains("rowpdf")) note.textContent = r.ok ? "Opened." : "Could not open it (" + r.status + ").";
          else if (!r.ok) alert("Could not open the PDF (" + r.status + ")."); } catch (err) { lost(err); }
  } else if (el.id === "stop") {
    if (!confirm("Stop the service for {{.Library}}? A running job is stopped first, and what is not pushed yet is pushed.")) return;
    try { await fetch(BASE + "/api/stop", { method: "POST" }); } catch (err) {}
    $("banner").style.display = "block";
    $("banner").textContent = "The service is stopping…";
    const wait = setInterval(async () => {
      try { await fetch(BASE + "/frag/status", { cache: "no-store" }); }
      catch (err) { clearInterval(wait); stopped = true; lost(err);
                    $("banner").textContent = "The service has stopped. Start it again with bib.check -start, then open the UI with bib.check -ui."; }
    }, 1000);
  }
});

(async () => {
await loadPrefs();
layout = Object.assign(JSON.parse(JSON.stringify(defaultLayout)), load("layout", {}));
layout.widths = Object.assign({}, layout.widths);
views.search = new SearchView($("panel-search"), null);
onShow.search = () => views.search.show();
await refreshSelects();
// #<tab>, or #entry:<key> (the Search tab with that entry's pop-up);
// #<tab>/entry:<key> and #<tab>/author:<id>: that tab with the pop-up over it.
const deep = location.hash.match(/^#(?:([^/]*)\/)?(entry|author):(.+)$/);
if (deep) { showTab(deep[1] || "search"); const id = decodeURIComponent(deep[3]); open1(deep[2], id, id); }
else showTab(location.hash.slice(1) || "search");
})();
</script>
</body>
</html>{{end}}

{{define "status"}}
<div class="cards">
  <div class="card">
    <h2>Service</h2>
    <div class="state">
      {{if .Busy}}<span class="dot busy"></span><div><b>Running</b><br><span class="muted">{{.Job}}<br>since {{.JobFrom}}</span></div>
      {{else}}<span class="dot ok"></span><div><b>Idle</b><br><span class="muted">no job running</span></div>{{end}}
    </div>
    <p class="muted">Version {{.Version}}, running since {{.Since}}.</p>
  </div>
  <div class="card">
    <h2>Shared DB copy</h2>
    <div class="state">
      {{if .Unpushed}}<span class="dot warn"></span><div><b>Changes not pushed yet</b><br>
        <span class="muted">{{if .PushDue}}push due at {{.PushDue}}{{else}}pushed by the next write job or -push{{end}}</span></div>
      {{else}}<span class="dot ok"></span><div><b>Up to date</b></div>{{end}}
    </div>
    {{if .Pushed}}<p class="muted">Last push: {{.Pushed}}.</p>{{end}}
  </div>
  <div class="card">
    <h2>Stop</h2>
    <p class="muted">Stops the service as <span class="key">-stop</span> does: a running job first, then what is not pushed yet is pushed.</p>
    <button class="action danger" id="stop">Stop the service</button>
  </div>
</div>
{{end}}

{{define "stats"}}
<table class="list">{{range .Stats}}<tr><td>{{.Label}}</td><td class="num">{{.Value}}</td></tr>{{end}}</table>
{{end}}

{{define "people"}}{{range $i, $p := .}}{{if $i}} and {{end}}<a href="#" data-author="{{$p.ID}}" data-name="{{$p.Name}}">{{$p.Name}}</a>{{end}}{{end}}

{{define "entry"}}
{{if .Reference}}<div class="card ref">{{.Reference}}</div>{{end}}
<div class="line"><span class="label">Groups</span>{{range .Groups}}<span class="chip">{{.}}</span>{{else}}<span class="muted">none</span>{{end}}</div>
{{if .HasPDF}}<div class="line"><span class="label">PDF</span><button class="pdf" data-pdf="{{.Key}}" title="Open the PDF with macOS"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M8 13h8M8 17h5"/></svg>Open the PDF</button><span class="muted"></span></div>{{end}}
<div class="card bib">
  <div>@{{.Type}}{<span class="key">{{.Key}}</span>,</div>
  <table>
    {{if .Authors}}<tr><td class="field">author</td><td class="eq">=</td><td>{{"{"}}{{template "people" .Authors}}{{"},"}}</td></tr>{{end}}
    {{if .Editors}}<tr><td class="field">editor</td><td class="eq">=</td><td>{{"{"}}{{template "people" .Editors}}{{"},"}}</td></tr>{{end}}
    {{range .Fields}}<tr><td class="field">{{.Name}}</td><td class="eq">=</td><td>{{"{"}}{{if .Entry}}<a href="#" data-entry="{{.Entry}}">{{.Value}}</a>{{else if .Link}}<a href="{{.Link}}" target="_blank" rel="noopener">{{.Value}}</a>{{else}}{{.Value}}{{end}}{{"},"}}</td></tr>{{end}}
    {{if .GroupsField}}<tr><td class="field">groups</td><td class="eq">=</td><td>{{"{"}}{{.GroupsField}}{{"},"}}</td></tr>{{end}}
  </table>
  <div>}</div>
</div>
{{end}}

{{define "author"}}
<div class="card">
  <h2>{{.Name}} <span class="muted key">{{.ID}}</span></h2>
  <table class="list">
    <tr><td class="field">ORCID</td><td>{{range $i, $o := .ORCIDs}}{{if $i}}, {{end}}<a href="https://orcid.org/{{$o}}" target="_blank" rel="noopener">{{$o}}</a>{{else}}<span class="muted">none</span>{{end}}</td></tr>
    <tr><td class="field">DBLP</td><td>{{if .DBLP}}<a href="https://dblp.org/pid/{{.DBLP}}.html" target="_blank" rel="noopener">{{.DBLP}}</a>{{else}}<span class="muted">none</span>{{end}}</td></tr>
    <tr><td class="field">Other name forms</td><td>{{range $i, $f := .Forms}}{{if $i}}; {{end}}{{$f}}{{else}}<span class="muted">none</span>{{end}}</td></tr>
  </table>
</div>
<div class="card">
  <h2>Entries ({{len .Results}})</h2>
  <table class="list">
    {{range .Results}}<tr><td class="year">{{.Year}}</td><td class="ref">{{if .HTML}}{{.HTML}}{{else}}{{if .Title}}{{.Title}}{{else}}<span class="key">{{.Key}}</span>{{end}}{{if .Authors}} — {{template "people" .Authors}}{{end}}{{end}} <a class="open" href="#" data-entry="{{.Key}}" title="{{.Key}}">›</a></td></tr>{{end}}
  </table>
</div>
{{end}}
`
