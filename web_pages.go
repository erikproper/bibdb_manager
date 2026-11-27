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
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='7' fill='%2303a9f4'/%3E%3Cpath d='M5 9.5c3.5-1.8 7.5-1.6 10.4 1.2v14c-2.9-2.6-6.9-2.8-10.4-1.2z' fill='%23fff'/%3E%3Cpath d='M27 9.5c-3.5-1.8-7.5-1.6-10.4 1.2v14c2.9-2.6 6.9-2.8 10.4-1.2z' fill='%23fff'/%3E%3Cpath d='M8 13.5h4.5M8 16.5h4.5M8 19.5h4.5M19.5 13.5H24M19.5 16.5H24M19.5 19.5H24' stroke='%2303a9f4' stroke-width='1.2' stroke-linecap='round'/%3E%3C/svg%3E">
<style>
/* Colours, one meaning each (Erik, 2026-11-24), from the settings' "colours" (light, dark):
   blue (primary, chosen) the interface itself and active filters; green (ok) fine / in
   place; amber (warn) pending, not final yet; red (busy: the LEDs; danger) busy changing
   data, or a dangerous action; orange (edit, editbg) editing. */
:root { {{.ColoursLight}}
  --shadow: 0 2px 2px rgba(0,0,0,.14), 0 1px 5px rgba(0,0,0,.12);
}
@media (prefers-color-scheme: dark) {
  :root { {{.ColoursDark}} --shadow: 0 2px 2px rgba(0,0,0,.5); }
}
* { box-sizing: border-box; }
[hidden] { display: none !important; }
html, body { overflow: hidden; }
html, body { height: 100%; }
body { margin: 0; background: var(--bg); color: var(--text); display: flex; flex-direction: column;
       font: 15px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
header { position: relative; z-index: 11; /* above a pop-up's overlay: the tabs stay at hand */ background: var(--primary); color: var(--primary-text); padding: 0 16px; flex: none;
         display: flex; align-items: last baseline; gap: 24px; min-height: 56px; } /* tabs at the bottom, the title on their baseline */
header .logo { width: 32px; height: 32px; flex: none; align-self: center; margin-right: -12px; border-radius: 7px;
               box-shadow: 0 0 0 1.5px rgba(255,255,255,.7); }
header .title { font-size: 20px; font-weight: 400; white-space: nowrap; }
header .ver { font-size: 12px; opacity: .75; white-space: nowrap; }
header .conn { margin-left: auto; font-size: 13px; } /* pushes the version to the far right */
header .led { display: inline-block; margin: 0 0 0 7px; vertical-align: 0; width: 9px; height: 9px; border-radius: 50%; flex: none; background: var(--ok);
              box-shadow: 0 0 0 2px rgba(255,255,255,.6); }
header .led.busy { background: var(--busy); animation: ledpulse 1s ease-in-out infinite alternate; }
@keyframes ledpulse { from { opacity: 1; } to { opacity: .45; } }
.tabs { display: flex; flex-wrap: wrap; gap: 2px 4px; min-width: 0; padding-top: 4px; } /* a second line when the first is full */
.tabs button { background: transparent; color: var(--primary-text); border: 0; border-radius: 8px 8px 0 0;
               padding: 8px 18px; font-size: 14px; cursor: pointer; opacity: .85; white-space: nowrap; flex: none; }
.tabs button:hover { opacity: 1; background: rgba(255,255,255,.15); }
.tabs button.active { background: var(--bg); color: var(--text); opacity: 1; }
.tabs button.tabdrop { box-shadow: inset 3px 0 0 var(--primary-text); }
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
input[type=search] { width: 100%; padding: 8px 12px; font: inherit; font-size: 13px; border: 1px solid var(--border); /* as the list and the lines around it */
                     border-radius: 8px; background: var(--card); color: var(--text); }
button.action { background: var(--primary); color: var(--primary-text); border: 0; border-radius: 8px;
                padding: 8px 16px; font-size: 14px; cursor: pointer; }
button.action.danger { background: var(--danger); }
button.action:disabled, button.plain:disabled { opacity: .45; cursor: default; }
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

/* Search */
.searchbar { flex: none; display: flex; gap: 12px; align-items: center; margin-bottom: 14px; }
.searchbar input { flex: 1; }
select.groupadd { font-size: 13px; padding: 3px 6px; border: 1px solid var(--border); border-radius: 8px;
                  background: var(--card); color: var(--primary); max-width: 220px; }
.groupchip { display: inline-flex; align-items: center; gap: 4px; background: var(--chosen); color: #fff; border-radius: 14px;
             padding: 2px 4px 2px 10px; font-size: 13px; white-space: nowrap; }
.groupchip .n { opacity: .75; }
.tabs button .tabx { margin-left: 8px; opacity: .7; padding: 0 2px; border-radius: 4px; }
.tabs button .tabx:hover { opacity: 1; background: rgba(0,0,0,.15); }
.tabs button.seltab { font-style: italic; }
.actbtns { display: flex; flex-wrap: wrap; gap: 6px; } /* under their group's heading, as on the Tables tab */
.tablehelp { margin: 4px 0 10px; padding: 8px 12px; border-left: 3px solid var(--chosen); background: var(--card); border-radius: 4px; font-size: 13.5px; line-height: 1.45; }
.selbar { flex: none; display: flex; gap: 10px; align-items: center; margin-bottom: 14px; /* as below the filter */ font-size: 13px; flex-wrap: wrap; }
.selbar button.action, .editbar button.action { padding: 4px 12px; font-size: 13px; } /* the size of the other small buttons */
.selbar .selmsg { color: var(--muted); }
button.toggle { border: 1px solid var(--border); background: var(--card); color: var(--text); border-radius: 14px;
                padding: 2px 10px; font-size: 13px; cursor: pointer; white-space: nowrap; }
button.toggle.on { background: var(--chosen); border-color: var(--chosen); color: #fff; }
button.toggle.on.stale { background: var(--warn); border-color: var(--warn); color: #000; } /* pending: the list is behind */
.tick { display: inline-block; width: 16px; height: 16px; border: 1.5px solid var(--muted); border-radius: 4px;
        vertical-align: middle; cursor: pointer; margin-right: 4px; text-align: center; line-height: 13px; font-size: 12px; color: transparent; }
.rolepick { font-size: 13px; font-weight: 400; margin-left: 14px; color: var(--muted); cursor: pointer; }
.tabs button.edittab { color: #ffe0b2; }
.tabs button.edittab.active { background: var(--editbg); color: var(--edit); }
.editbar { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; margin-bottom: 12px; padding: 8px 12px;
           border-radius: 10px; background: var(--editbg); border-left: 4px solid var(--edit); }
.editbar .edmsg { color: var(--muted); }
table.edit { width: 100%; border-collapse: collapse; }
table.edit td { padding: 4px 6px; vertical-align: top; border-bottom: 1px solid var(--border); }
table.edit td.field { width: 150px; color: var(--muted); font-family: ui-monospace, Menlo, monospace; font-size: 13px; padding-top: 9px; }
table.edit tr.removed { display: none; }
table.edit tr.changed td.field { color: var(--edit); font-weight: 600; }
table.edit textarea, table.edit select { width: 100%; font: 13px/1.4 ui-monospace, Menlo, monospace; padding: 5px 7px; resize: vertical;
           border: 1px solid var(--border); border-radius: 6px; background: var(--card); color: var(--text); }
table.edit .out { font-size: 12px; color: var(--muted); margin-top: 2px; }
pre.edout { white-space: pre-wrap; font: 12px/1.4 ui-monospace, Menlo, monospace; margin: 8px 0 0; color: var(--muted); }
.selpick { display: inline-flex; align-items: center; gap: 2px; margin-right: 14px; cursor: pointer; font-size: 14px; }
.tick.on { background: var(--ok); border-color: var(--ok); color: #fff; }        /* selected: in place */
.tick.kept { background: var(--warn); border-color: var(--warn); color: #000; }   /* unselected, old local key kept until Save: pending */
.groupchip button { background: transparent; border: 0; color: #fff; cursor: pointer; font-size: 14px; padding: 0 4px; line-height: 1; }
.searchinfo > *:not(.hint) { white-space: nowrap; flex: none; }
.searchinfo { flex: none; display: flex; flex-wrap: wrap; gap: 8px 12px; align-items: center; margin-bottom: 8px; font-size: 13px; }
.searchinfo .hint { flex: 1 1 260px; }
.gridbox { flex: 1; min-height: 0; overflow: auto; background: var(--card); border-radius: 12px; box-shadow: var(--shadow); }
table.grid { border-collapse: separate; border-spacing: 0; table-layout: fixed; font-size: 13px; }
table.grid th, table.grid td { white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
                               padding: 4px 8px; border-bottom: 1px solid var(--border); text-align: left; }
table.grid th { position: sticky; top: 0; background: var(--card); z-index: 1; color: var(--muted); font-weight: 500;
                cursor: pointer; user-select: none; border-right: 1px solid var(--border); }
table.grid th.drop { box-shadow: inset 3px 0 0 var(--primary); }
table.grid th .grip { position: absolute; top: 0; right: -3px; width: 7px; height: 100%; cursor: col-resize; z-index: 2; }
table.grid tbody tr:hover { background: var(--hover); }
table.grid tbody tr.dropping { outline: 2px dashed var(--primary); outline-offset: -2px; background: var(--hover); }
#mbody.dropping { outline: 2px dashed var(--primary); outline-offset: -6px; }
table.grid tbody tr, #mbody table.list tr[data-key] { cursor: pointer; }
#mbody table.list tr[data-key]:hover { background: var(--hover); }
table.grid td.inh { color: var(--muted); font-style: italic; }
table.grid th.op { padding: 4px 3px; overflow: visible; } /* in line with the cells below */
table.grid th.op svg { vertical-align: -2px; margin-left: 2px; }
table.grid th .headtick { display: inline-block; width: 16px; height: 16px; border: 1.5px solid var(--muted); border-radius: 4px;
        vertical-align: middle; cursor: pointer; margin-right: 4px; text-align: center; line-height: 13px; font-size: 12px; color: transparent;
  color: var(--text); cursor: pointer; } /* as a box of an entry: no colour, the tick as the text (white in dark mode) */
table.grid td.op { padding: 2px 3px; text-align: left; white-space: nowrap; }
table.grid td.op .tick { margin-right: 3px; }
table.grid td.op button.copykey, table.grid td.op button.rowpdf { border: 0 !important; padding: 0 2px !important; margin: 0 2px 0 0; vertical-align: middle; }
table.grid td.op .copyslot { display: inline-block; width: 17px; margin-right: 2px; }
table.grid td.op button.rowpdf svg, table.grid td.op button.copykey svg { vertical-align: -2px; }
button.rowpdf.faded { opacity: .35; } button.rowpdf.faded:hover { opacity: .8; }
table.grid td.op button { background: transparent; border: 1px solid var(--border); border-radius: 6px; color: var(--primary);
                          cursor: pointer; padding: 0 6px; font-size: 13px; line-height: 18px; }
.loading { padding: 12px; }

/* Log */
ul.notes { margin: 0; padding-left: 20px; } ul.notes li { margin: 4px 0; }
.qwarn { padding: 8px 12px; border-radius: 10px; background: var(--hover); border-left: 4px solid var(--warn); margin-bottom: 12px; }
.qrow { display: flex; gap: 8px; align-items: center; padding: 8px 10px; border: 1px solid var(--border); border-radius: 10px; margin-bottom: 8px; background: var(--card); }
.qrow button { padding: 6px 12px; font-size: 13px; min-width: 8.5em; }
.qedit { display: flex; gap: 8px; margin-top: 10px; }
.qedit input { flex: 1; font: 14px ui-monospace, Menlo, monospace; padding: 6px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--card); color: var(--text); }
.qframe { padding: 10px 12px; border: 1px solid var(--border); border-radius: 10px; margin-bottom: 8px; background: var(--card); }
.qactions button { padding: 8px 16px; font-size: 14px; min-width: 7em; } /* Confirm, Stop and the others the same size */
.qvals { margin: 6px 0 2px; border-collapse: collapse; } .qvals td { padding: 2px 10px 2px 0; vertical-align: top; } .qvals td.f { white-space: nowrap; }
.qtext { font-weight: 600; margin: 12px 0 8px; }
.qopts label { display: flex; gap: 10px; align-items: flex-start; padding: 8px 10px; border: 1px solid var(--border); border-radius: 10px; margin-bottom: 8px; cursor: pointer; background: var(--card); }
.qopts label:has(input:checked) { border-color: var(--chosen); box-shadow: 0 0 0 1px var(--chosen); }
.qopts input[type=radio] { margin-top: 3px; }
.qopts .qlabel { font-weight: 500; }
.qbib { font-family: ui-monospace, Menlo, monospace; font-size: 12.5px; margin-top: 4px; }
.qbib table { border-collapse: collapse; } .qbib td { padding: 0 6px 0 0; vertical-align: top; } .qbib td.f { color: var(--muted); white-space: nowrap; }
.qsub { color: var(--muted); margin-top: 6px; font-size: 13px; }
.qactions { display: flex; gap: 10px; margin-top: 14px; align-items: center; }
.qactions input { flex: 1; font: 14px ui-monospace, Menlo, monospace; padding: 6px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--card); color: var(--text); }
.actionscard { flex: 1; min-height: 0; display: flex; flex-direction: column; margin: 0; }
.tabs button .waiting { display: inline-block; width: 9px; height: 9px; border-radius: 50%; background: var(--warn); margin-left: 7px;
                        box-shadow: 0 0 0 2px rgba(255,255,255,.6); animation: ledpulse 1s ease-in-out infinite alternate; }
.tabs button .waiting.idle { background: var(--ok); animation: none; } /* green: no question waiting */
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
#tray { position: fixed; left: 16px; bottom: 16px; z-index: 15; display: none; gap: 10px; align-items: center; flex-wrap: wrap;
        background: var(--card); border: 1px solid var(--border); border-radius: 12px; box-shadow: var(--shadow); padding: 8px 12px; font-size: 13px; }
#tray.show { display: flex; }
#tray .part { display: flex; gap: 6px; align-items: center; }
table.compare { border-collapse: collapse; width: 100%; font-size: 13px; }
table.compare td, table.compare th { padding: 5px 8px; border-bottom: 1px solid var(--border); vertical-align: top; text-align: left; }
table.compare td.f { color: var(--muted); font-family: ui-monospace, Menlo, monospace; white-space: nowrap; width: 120px; }
table.compare tr.diff td:not(.f) { background: rgba(255,166,0,.12); }
table.compare td.kept { box-shadow: inset 2px 0 0 var(--ok); }
#toast { position: fixed; left: 50%; bottom: 24px; transform: translate(-50%, 40px); opacity: 0; transition: all .2s;
         background: #323232; color: #fff; padding: 10px 16px; border-radius: 10px; box-shadow: 0 4px 16px rgba(0,0,0,.3);
         display: flex; gap: 12px; align-items: center; z-index: 20; font-size: 14px; pointer-events: none; }
#toast.show { transform: translate(-50%, 0); opacity: 1; pointer-events: auto; }
#banner { display: none; background: var(--warn); color: #000; padding: 8px 16px; flex: none; }
</style>
</head>
<body>
<header>
  <svg class="logo" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#03a9f4"/><path d="M5 9.5c3.5-1.8 7.5-1.6 10.4 1.2v14c-2.9-2.6-6.9-2.8-10.4-1.2z" fill="#fff"/><path d="M27 9.5c-3.5-1.8-7.5-1.6-10.4 1.2v14c2.9-2.6 6.9-2.8 10.4-1.2z" fill="#fff"/><path d="M8 13.5h4.5M8 16.5h4.5M8 19.5h4.5M19.5 13.5H24M19.5 16.5H24M19.5 19.5H24" stroke="#03a9f4" stroke-width="1.2" stroke-linecap="round"/></svg>
  <span class="title">{{.Library}}</span>
  <nav class="tabs" id="tabs">
    <button data-tab="status">Status</button>
    <button data-tab="search">Search</button>
    <button data-tab="tables">Tables</button>
    <button data-tab="actions">Actions<span class="waiting idle" title="No question waiting"></span></button>
    <button data-tab="stats">Statistics</button>
    <button data-tab="log">Log<span class="led" id="led" title=""></span></button>
  </nav>
  <span class="conn" id="conn"></span>
  <span class="ver" title="bibdb_manager version">{{.Version}}</span>
</header>
<div id="banner"></div>
<main>
  <section class="panel" id="panel-status" hidden><div class="narrow" id="status"><p class="muted">Loading…</p></div></section>
  <section class="panel fill" id="panel-search" hidden></section>
  <section class="panel" id="panel-tables" hidden><div class="narrow">
    <p class="muted" style="font-size:13px;margin:0 0 12px">Each table can have a tab of its own: choose the ones to show in the tab bar.</p>
    <div id="tablelist"><p class="muted">Loading…</p></div></div></section>
  <section class="panel" id="panel-actions" hidden><div class="narrow">
    <div class="card"><h2>Checks</h2><div class="actbtns">
        <button class="plain" data-action="check" title="Check the library">Check</button>
        <button class="plain" data-action="fix_duplicates" title="Resolve title duplicates">Fix duplicates</button>
        <button class="plain" data-action="align_countries" title="Country names in booktitles, as the Country names in titles table has them">Country names in titles</button></div>
    </div>
    <div class="card"><h2>Contributors</h2><div class="actbtns">
        <button class="plain" data-action="enrich" title="Absorb DBLP names and ORCIDs, enrich from ORCID profiles, merge ORCID duplicates">Enrich contributors</button>
        <button class="plain" data-action="enrich_matched" title="As Enrich contributors, but without asking about names that differ from the ORCID profile">Enrich, matched ORCIDs only</button>
        <button class="plain" data-action="refresh_orcid" title="Fetch the ORCID profiles again, the oldest first (Stop when it is enough)">Refresh ORCID</button>
        <button class="plain" data-action="merge_orcid" title="Merge contributors that share an ORCID, or whose ORCIDs were merged by ORCID">Merge ORCID duplicates</button>
        <button class="plain" data-action="disambiguate" title="Resolve names that several contributors share, entry by entry">Disambiguate contributors</button>
        <button class="plain" data-action="triage" title="Triage the author/editor values that lost a challenge">Triage contributor mappings</button>
        <button class="plain" data-action="triage_aliases" title="Name forms used in one entry only: generalise them, or keep them there">Triage contributor aliases</button></div>
    </div>
    <div class="card"><h2>PDF</h2><div class="actbtns">
        <button class="plain" data-action="get_pdfs" title="Download missing PDFs from the url fields ending in .pdf">Get from URL</button>
        <button class="plain" data-action="get_arxiv" title="Download missing PDFs of arXiv eprints from arXiv (one every 3 s; Stop when it is enough)">Get from arXiv</button>
        <button class="plain" data-action="check_pdfs" title="Check the PDFs (health, own title, duplicates), going on where the last check stopped (Stop when it is enough)">Check</button>
        <button class="plain" data-action="check_pdfs_new" title="Check the PDFs from the first one again">Check from the start</button>
        <button class="plain" data-action="add_failed" title="Add the URLs of the last failed downloads to Bad URLs, so that Get skips them">Add failed</button></div>
    </div>
    <div class="card"><h2>DBLP</h2><div class="actbtns">
        <button class="plain" data-action="update_dblp" title="Import the DBLP export into the local DBLP cache (downloaded from dblp_url, or from Downloads)">Update cache</button>
        <button class="plain" data-action="update_entries" title="Update every entry with a DBLP key from the DBLP cache">Update entries</button>
        <button class="plain" data-action="fix_candidates" title="Link entries without a DBLP key to DBLP records">Candidates</button>
        <button class="plain" data-action="watch" title="Check the watched persons and ORCIDs for missing publications">Watch</button>
        <button class="plain" id="dblplookup" title="Find an entry in the local DBLP cache, and add it to the library">Lookup</button></div>
    </div>
    <div class="card"><h2>Other</h2><div class="actbtns">
        <button class="plain" data-action="entry_actions" title="Run the group assignment rules (entry_actions)">Entry actions</button>
        <button class="plain" data-action="audit" title="Report mapping rows that never apply">Audit mappings</button>
        <button class="plain" data-action="push" title="Push the changes of this machine to the shared DB folder now">Push</button></div>
    </div>
    <div class="card">
      <div class="state"><span class="dot ok" id="dlgdot"></span><div><b id="dlgtitle">No job yet</b><br><span class="muted" id="dlgstate">Start one with an action above. Its output goes to the Log tab.</span></div>
        <span style="margin-left:auto"></span>
        <button class="action" id="dlgopen" hidden>Answer the question</button>
        <button class="action danger" id="dlgstop" hidden title="Stop the job, as Ctrl-C does">Stop</button></div>
    </div>
    <div class="card" id="dlgstepcard" hidden><h2>Progress</h2>
      <table class="list"><tr><td class="field">step</td><td id="dlgstep"></td></tr><tr><td class="field">item</td><td id="dlgsub"></td></tr>
        <tr><td class="field">now</td><td id="dlgnow"></td></tr></table></div>
    <div class="card" id="dlgoutcard" hidden><h2 id="dlgouttitle">Result</h2><div id="dlgout"></div></div>
    <div class="card" id="dlgnotescard" hidden><h2>Warnings</h2><div id="dlgnotes"></div></div>
    <div class="card" id="dlghistcard" hidden><h2>Answers</h2><table class="list" id="dlghist"></table></div>
  </div></section>
  <section class="panel" id="panel-stats" hidden><div class="narrow"><div class="card"><h2>Library statistics <button class="plain" id="statsrefresh">Refresh</button></h2><div id="stats"><p class="muted">Loading…</p></div></div></div></section>
  <section class="panel fill" id="panel-log" hidden><div class="card logcard"><h2>Log <span class="muted">(the service and its jobs)</span>
      <button class="plain" id="logend">To the end</button></h2><pre class="log" id="log"></pre></div></section>
</main>
<template id="tabletpl">
  <div class="tablehelp thelp"></div>
  <div class="searchbar"><input type="search" class="tq" placeholder="Filter: words in any column" autocomplete="off">
    <button class="action tadd" title="Add a row">+</button></div>
  <div class="searchinfo"><span class="tcount muted"></span></div>
  <div class="gridbox tbox"><table class="list ttable"><thead><tr></tr></thead><tbody></tbody></table><div class="loading muted tmore"></div></div>
</template>
<template id="searchtpl">
  <div class="selbar" hidden><b class="selname"></b><span class="selinfo muted"></span>
    <button class="action selsave">Save</button><button class="action selsync">Sync</button><button class="plain selclose">Close</button>
    <span class="selmsg"></span></div>
  <div class="searchbar"><input type="search" class="q" placeholder="Filter: words from the key, alias, title, authors, editors, year, venue, DOI (empty: all entries)" autocomplete="off"></div>
  <div class="searchinfo"><span class="count muted"></span><button class="toggle selonly" hidden title="Show only the selected entries (green and amber)">Selection</button>
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
  if (name === "dialogue") name = "actions"; // its name before v30.167 (links, saved tabs)
  if (!$("panel-" + name)) name = "search";
  if (current === "actions" && name !== "actions" && qOpen) { qOpen = false; qShown = ""; closePopup(); } // back when the tab is shown again
  else if (name !== current && $("overlay").classList.contains("open")) closePopup(); // a pop-up belongs to the tab it was opened on
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
// ---- the order of the tabs: the fixed ones can be dragged (the order is kept in the
// preferences); the Select, edit and author tabs always follow Search, in their order.
const linkedTab = (name) => /^(sel|edit|author|merge|tbl)-/.test(name);
function arrangeTabs() {
  const bar = $("tabs");
  const all = [...bar.querySelectorAll("button[data-tab]")];
  const fixed = all.filter((b) => !linkedTab(b.dataset.tab));
  const linked = all.filter((b) => linkedTab(b.dataset.tab) && !/^tbl-/.test(b.dataset.tab));
  const tables = all.filter((b) => /^tbl-/.test(b.dataset.tab)); // follow the Tables tab, in the order chosen there
  const rank = (b) => /^sel-/.test(b.dataset.tab) ? 0 : 1; // the Select tabs first, then the edit tabs
  linked.sort((a, b) => rank(a) - rank(b));
  const order = load("taborder", []).map((n) => n === "dialogue" ? "actions" : n); // its name before v30.167
  fixed.sort((a, b) => {
    const ia = order.indexOf(a.dataset.tab), ib = order.indexOf(b.dataset.tab);
    return (ia < 0 ? 999 + fixed.indexOf(a) : ia) - (ib < 0 ? 999 + fixed.indexOf(b) : ib);
  });
  for (const b of fixed) {
    bar.appendChild(b);
    if (b.dataset.tab === "search") for (const l of linked) bar.appendChild(l);
    if (b.dataset.tab === "tables") for (const t of tables) bar.appendChild(t);
  }
}
let draggedTab = "";
$("tabs").addEventListener("dragstart", (e) => {
  const b = e.target.closest("button[data-tab]");
  if (!b || linkedTab(b.dataset.tab)) { e.preventDefault(); return; }
  draggedTab = b.dataset.tab; e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("text/plain", draggedTab);
});
$("tabs").addEventListener("dragover", (e) => {
  const b = e.target.closest("button[data-tab]");
  if (!draggedTab || !b || linkedTab(b.dataset.tab)) return;
  e.preventDefault();
  for (const x of $("tabs").querySelectorAll(".tabdrop")) x.classList.remove("tabdrop");
  b.classList.add("tabdrop");
});
$("tabs").addEventListener("dragleave", (e) => { const b = e.target.closest("button[data-tab]"); if (b) b.classList.remove("tabdrop"); });
$("tabs").addEventListener("drop", (e) => {
  const b = e.target.closest("button[data-tab]");
  for (const x of $("tabs").querySelectorAll(".tabdrop")) x.classList.remove("tabdrop");
  if (!draggedTab || !b || linkedTab(b.dataset.tab) || b.dataset.tab === draggedTab) { draggedTab = ""; return; }
  e.preventDefault();
  const names = [...$("tabs").querySelectorAll("button[data-tab]")].map((x) => x.dataset.tab).filter((n) => !linkedTab(n));
  names.splice(names.indexOf(draggedTab), 1);
  const r = b.getBoundingClientRect(), after = e.clientX > r.left + r.width / 2;
  names.splice(names.indexOf(b.dataset.tab) + (after ? 1 : 0), 0, draggedTab);
  save("taborder", names);
  draggedTab = "";
  arrangeTabs();
});
$("tabs").addEventListener("dragend", () => { draggedTab = ""; for (const x of $("tabs").querySelectorAll(".tabdrop")) x.classList.remove("tabdrop"); });

function every(name, ms, fn) { // fn runs every ms while the tab is shown
  clearInterval(timers[name]);
  timers[name] = setInterval(() => { if (current === name && !stopped) fn(); }, ms);
}

// ---- PDFs dropped on an entry (a row, an entry's pop-up, an author's list) ----
function draggingFiles(e) { return e.dataTransfer && [...e.dataTransfer.types].includes("Files"); }
// A note at the bottom with an Undo button, for 10 seconds.
let toastTimer;
function toast(text, undo) {
  let t = $("toast");
  if (!t) { t = document.createElement("div"); t.id = "toast"; document.body.appendChild(t); }
  t.innerHTML = esc(text) + (undo ? ' <button class="action">Undo</button>' : "");
  t.className = "show";
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.className = ""; }, 10000);
  if (undo) t.querySelector("button").onclick = async () => {
    t.className = "";
    try {
      const r = await fetch(BASE + "/api/pdfundo/" + undo, { method: "POST" });
      if (!r.ok) { alert(await r.text()); return; }
      const d = await r.json();
      pdfChanged(d.key);
      toast("Undone: the PDF of " + d.key + " is as it was.");
    } catch (e) { lost(e); }
  };
}
// pdfChanged shows an entry's PDF state anew: its rows and an open pop-up.
async function pdfChanged(key) {
  let has = false;
  try { has = (await get("/frag/entry/" + encodeURIComponent(key))).includes('data-pdf="'); } catch (e) {}
  for (const v of Object.values(views)) if (v instanceof SearchView) {
    const row = v.rows.find((r) => r.k === key);
    if (row && row.pdf !== has) { row.pdf = has; const top = v.el.gridbox.scrollTop; v.renderRows(0); v.el.gridbox.scrollTop = top; }
  }
  const top = stack[stack.length - 1];
  if ($("overlay").classList.contains("open") && top) show(top);
}
// downloadPDF downloads an entry's PDF from its url or eprint, as a job (step 25), staying
// where the user is; the icon or pop-up follows when it is done.
async function downloadPDF(key) {
  await entryPDFJob("get_pdf", key, "Downloading the PDF of " + key + " …", "Downloaded the PDF of " + key + ".", "No PDF for " + key + ": see the Log tab.");
}
// entryPDFJob runs an entry's PDF job (download, title check) and follows it: a question it
// asks (logged in? is it the entry's PDF?) opens in the Actions tab, after which the page
// goes back to where the user was; the entry's icon and pop-up follow.
async function entryPDFJob(action, key, startText, okText, failText) {
  try {
    const r = await fetch(BASE + "/api/actions/start", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, given: [key] }) });
    if (!r.ok) { if (startText) alert(await r.text()); return; } // a check after a drop: silent when the service is busy
  } catch (e) { lost(e); return; }
  if (startText) toast(startText);
  let back = "";
  for (let i = 0; i < 1800; i++) { // a browser download (JSTOR) waits for the user
    await new Promise((res) => setTimeout(res, 1000));
    let d; try { d = await get("/api/actions", "json"); } catch (e) { return; }
    if (d.asking && !back) { back = current; closePopup(); showTab("actions"); loadActions(); }
    if (!d.running) {
      if (back && back !== "actions") { if (qOpen) { qOpen = false; closePopup(); } showTab(back); }
      await pdfChanged(key);
      if (okText || failText) toast(d.code ? failText : okText);
      return;
    }
  }
}
// uploadPDF stores file as the entry's PDF; asks before replacing one. True when stored.
async function uploadPDF(key, file) {
  if (!file || !(file.type === "application/pdf" || /\.pdf$/i.test(file.name))) { alert("Only a PDF file can be dropped on an entry."); return false; }
  for (let replace = false; ; replace = true) {
    try {
      const r = await fetch(BASE + "/api/pdfup/" + encodeURIComponent(key) + (replace ? "?replace=1" : ""), { method: "POST",
        headers: { "Content-Type": "application/pdf" }, body: file });
      if (r.status === 409 && !replace) {
        if (!confirm(key + " has a PDF already. Replace it with " + file.name + "? (The old one goes to the Trash.)")) return false;
        continue;
      }
      if (!r.ok) { alert("Not stored: " + await r.text()); return false; }
      const d = await r.json();
      toast(d.message, d.undo);
      entryPDFJob("check_pdf_title", key, "", "", ""); // its title, cross-checked at once (Erik, 2026-11-27)
      return true;
    } catch (e) { lost(e); alert(String(e.message || e)); return false; }
  }
}
// Dropping a file anywhere else must not open it in the browser (leaving the page).
window.addEventListener("dragover", (e) => { if (draggingFiles(e)) e.preventDefault(); });
window.addEventListener("drop", (e) => { if (draggingFiles(e)) e.preventDefault(); });

// ---- the busy light: red while data is being changed or brought up to date ----
let seenIndex = -1;
async function pollState() {
  let st;
  try { st = await get("/api/state", "json"); } catch (e) { lost(e); $("led").className = "led"; $("led").style.background = "var(--muted)"; $("led").title = "no connection"; return; }
  $("led").style.background = "";
  const tabBtn = document.querySelector('.tabs button[data-tab="actions"]');
  // The Actions tab's light (amber): the current job waits for an answer, wherever
  // it was started; or the library could not load without one.
  const waitTitle = st.asking ? "A question waits for your answer here"
    : (st.answer && !st.busy) ? "The library is not loaded: it needed an answer. Run Check here, or bib.manager in a terminal." : "";
  const dlgTitle = waitTitle || "No question waiting";
  tabBtn.innerHTML = "Actions" + '<span class="' + (waitTitle ? "waiting" : "waiting idle") + '" title="' + esc(dlgTitle) + '"></span>';
  tabBtn.title = dlgTitle;
  // The Log tab's light: red while work is underway (a job, the library or the lists
  // being brought up to date), green when idle and up to date.
  const what = st.busy ? "Running: " + (st.job || "a job") : st.unloaded ? "Loading the library" : st.indexing ? "Bringing the lists up to date" : "";
  $("led").className = what ? "led busy" : "led";
  $("led").title = what || ("Idle, up to date" + (st.unpushed ? " (changes not pushed yet)" : ""));
  // The number of cases on the action buttons ("Fix duplicates (1784)").
  for (const b of document.querySelectorAll("[data-action]")) {
    if (!b.dataset.label) b.dataset.label = b.textContent;
    const n = st.counts ? st.counts[b.dataset.action] : undefined;
    const pct = b.dataset.action === "check_pdfs" && st.counts ? st.counts.check_pdfs_done : undefined; // where the PDF check goes on
    b.textContent = b.dataset.label + (n !== undefined ? " (" + n.toLocaleString() + ")" : "") + (pct !== undefined ? " (" + pct + "% done)" : "");
  }
  if (seenIndex >= 0 && st.index !== seenIndex) { // a new index: refresh the tables shown
    for (const v of Object.values(views)) if (v instanceof SearchView && v.started) v.refreshRows();
  }
  seenIndex = st.index;
}
setInterval(() => { if (!stopped) pollState(); }, 2000);
// The service sees by this connection that a page is open (it then opens no other). The
// browser connects again by itself after an interruption.
if (window.EventSource) new EventSource(BASE + "/api/live");

// ---- The Actions tab: actions, warnings, and questions as pop-ups (the output goes to the Log tab) ----
let dlg = null, qOpen = false, qShown = ""; // qShown: the question the pop-up shows (its line and position)
async function loadActions() {
  try { dlg = await get("/api/actions", "json"); } catch (e) { lost(e); return; }
  const d = dlg;
  $("dlgtitle").textContent = d.title || "No job yet";
  $("dlgstate").textContent = d.title ? (d.running ? (d.asking ? "Waits for your answer" : "Running since " + d.since) + " — its output is in the Log tab"
                                                  : "Ended" + (d.code ? " with code " + d.code : "") + " — its output is in the Log tab")
                                      : "Start one with an action above. Its output goes to the Log tab.";
  $("dlgdot").className = "dot " + (d.asking ? "warn" : d.running ? "busy" : "ok");
  $("dlgstop").hidden = !d.running;
  $("dlgopen").hidden = !d.asking;
  const st = d.status || {};
  $("dlgstepcard").hidden = !(d.running && (st.step || st.item || st.progress));
  $("dlgstep").textContent = st.step || "—";
  $("dlgsub").textContent = st.item ? (st.of ? st.n + " of " + st.of + ": " : "") + st.item : "—";
  $("dlgnow").textContent = st.progress || "—";
  // The result so far: what became of each item, and the tables.
  const outs = d.outcomes || [], tabs = d.tables || [];
  $("dlgoutcard").hidden = !(outs.length || tabs.length);
  // One table and nothing else (the load's Homework): the card is called after it.
  const single = !outs.length && tabs.length === 1;
  $("dlgouttitle").textContent = single ? tabs[0].text : "Result";
  $("dlgout").innerHTML = (outs.length ? '<table class="list">' + outs.slice().reverse().map((o) => '<tr><td class="key">' + esc(o.key) + "</td><td>" + esc(o.action) +
      (o.text ? ' <span class="muted">(' + esc(o.text) + ")</span>" : "") + '</td><td class="key">' + (o.target ? '<a href="#" data-entry="' + esc(o.target) + '">' + esc(o.target) + "</a>" : "") + "</td></tr>").join("") + "</table>" : "") +
    tabs.map((t) => (single ? "" : '<div class="qtext">' + esc(t.text) + "</div>") + '<table class="list">' + (t.rows || []).map((r) => "<tr><td>" + esc(r.label) + '</td><td style="text-align:right">' + esc(r.value) +
      '</td><td class="muted">' + (r.action ? '<button class="plain" data-start="' + esc(r.action) + '">' + esc(r.note) + "</button>" : esc(r.note || "")) + "</td></tr>").join("") + "</table>").join("");
  $("dlgnotescard").hidden = !(d.warnings && d.warnings.length);
  $("dlgnotes").innerHTML = (d.warnings || []).slice().reverse().map((w) => { const ls = w.text.trim().split("\n"); return warnHTML((w.kind === "error" ? "Error: " : "") + ls[0], ls.slice(1)); }).join("");
  $("dlghistcard").hidden = !(d.history && d.history.length);
  $("dlghist").innerHTML = (d.history || []).map((h) => '<tr><td class="year">' + esc(h.at) + "</td><td>" + esc(h.question.replace(/^QUESTION:\s*/, "")) +
    '</td><td class="key">' + esc(h.answer) + "</td></tr>").join("");
  // The question pop-up: opened while the tab is shown; a new question re-uses it; when the
  // job goes on without asking, it waits (Confirm greyed); when the job ends, it says so.
  const key = qKey(d);
  if (current === "actions") {
    if (d.asking && (!qOpen || key !== qShown) && (qOpen || !$("overlay").classList.contains("open"))) openQuestion();
    else if (qOpen && !d.asking && !d.running) qEnded(d);
    else if (qOpen && !d.asking && qShown) { qShown = ""; qWaiting(); } // answered (in another tab, too): waits for the job
  }
}
// linkEntries is text, escaped, with each entry key after "entry" a link that opens
// the entry's edit tab (Erik, 2026-11-26). Only after "entry": a contributor's id
// looks the same.
const linkEntries = (t) => esc(t).replace(/(\b[Ee]ntry\s+)(EP-\d{4}(?:-\d\d){5})\b/g,
  (_, pre, key) => pre + '<a href="#" data-edit="' + key + '" title="Edit this entry in an edit tab">' + key + "</a>");
// warnHTML renders a warning: its first line, value lines ("- Current : …") as a small
// table with their notes ("(source: dblp)") under them, other lines as text.
function warnHTML(head, lines) {
  let rows = "", rest = "";
  for (const l of lines) {
    const m = l.match(/^\s*-\s*([^:]{1,24}?)\s*:\s*(.*)$/);
    if (m) rows += '<tr><td class="f"><b>' + esc(m[1]) + "</b></td><td>" + esc(m[2]) + "</td></tr>";
    else if (/^\s*\(.*\)\s*$/.test(l) && rows) rows += '<tr><td></td><td class="muted">' + esc(l.trim()) + "</td></tr>";
    else rest += "<div>" + linkEntries(l.trim()) + "</div>";
  }
  return '<div class="qwarn"><div>' + linkEntries(head) + "</div>" + (rows ? '<table class="qvals">' + rows + "</table>" : "") + rest + "</div>";
}
// The question pop-up, made from the job's question message (d.q: kind, topic, warning,
// text, options with their labels and candidate entries, entries, field, values); the
// output text is never read for it. A line asked for without a message (d.q null) gets a
// text field under the job's last line.
const cap = (t) => (t || "").charAt(0).toUpperCase() + (t || "").slice(1);
// entryHTML lays an entry out as BibTeX ("@type{key," its fields "}"), framed; its notes
// under it, its crossref parent likewise under "Crossref parent:".
function entryHTML(e, framed) {
  if (!e) return "";
  const rows = (e.fields || []).map((f) => '<tr><td class="f">' + esc(f.name) + "</td><td>= {" + esc(f.value) + "},</td></tr>").join("");
  let html = '<div class="qbib">' + (e.key ? "<div>@" + esc(e.type || "misc") + "{" + esc(e.key) + ",</div>" : "") +
    '<table style="margin-left:2ch">' + rows + "</table>" + (e.key ? "<div>}</div>" : "") + "</div>" +
    (e.notes || []).map((n) => '<div class="muted" style="font-size:12.5px">' + esc(n) + "</div>").join("");
  if (e.parent) html += '<div class="qsub" style="font-weight:600;color:var(--text);margin-top:8px">Crossref parent:</div>' + entryHTML(e.parent, false);
  return framed ? '<div class="qframe">' + html + "</div>" : html;
}
const entryCard = (e, title) => (title || e.role ? '<div class="qtext">' + esc(title || e.role) + ":</div>" : "") + entryHTML(e, true);
const warnBox = (w) => { if (!w) return ""; const ls = w.split("\n"); return warnHTML(ls[0], ls.slice(1)); };
const valueOf = (q, role) => (q.values || []).find((v) => v.role === role);
const optKeys = (q) => (q.options || []).map((o) => o.key);
function showQuestion(d, html) {
  qOpen = true;
  qShown = qKey(d);
  popup("Question — " + (d.title || ""), html, false);
  for (const b of $("mbody").querySelectorAll("button[data-qopt]")) b.onclick = () => sendAnswer(b.dataset.qopt);
  const sj = $("qstopjob");
  if (sj) sj.onclick = async () => { try { if (dlg && dlg.job) await fetch(BASE + "/api/job/" + dlg.job + "/stop", { method: "POST" }); } catch (e) {} setTimeout(loadActions, 300); };
  const p = $("qpdf");
  if (p) p.onclick = async () => { try { const r = await fetch(BASE + "/api/qpdf", { method: "POST" }); if (!r.ok) toast("Could not open the PDF (" + r.status + ")."); } catch (e) { lost(e); } };
}
function qKey(d) { return d.asking && d.q ? d.q.id : ""; }
// editHTML: [Edit…] opens a field (prefilled); its value answers opt and then the question
// that asks for the value.
const editHTML = (prefill) => '<div class="qedit" hidden><input id="qeditin" value="' + esc(prefill || "") + '" autocomplete="off"><button class="action" id="qeditok">Use this value</button></div>';
function wireEdit(opt) {
  const btn = $("qeditbtn"); if (!btn) return;
  btn.onclick = () => { $("mbody").querySelector(".qedit").hidden = false; $("qeditin").focus(); $("qeditin").select(); };
  const go = () => { const v = $("qeditin").value.trim(); if (v) sendAnswer(opt, v); };
  $("qeditok").onclick = go;
  $("qeditin").onkeydown = (e) => { if (e.key === "Enter") go(); };
}
// qPDFIcon: a PDF icon that opens the PDF the question is about (q.pdf).
const qPDFIcon = (q) => q.pdf ? ' <button class="pdf" id="qpdf" title="Open the PDF to look" style="vertical-align:middle">' + PDFICON + "</button>" : "";
// stopButton: Stop answers "q" where the question takes it, else stops the job (the pop-up
// covers the tab's own Stop button).
const stopButton = (q) => optKeys(q).includes("q") ? '<button class="action danger" data-qopt="q" title="Stop the job">Stop</button>'
                                                  : '<button class="action danger" id="qstopjob" title="Stop the job, as Ctrl-C does">Stop</button>';
function openQuestion() {
  const d = dlg;
  if (!d || !d.asking) return;
  const q = d.q;
  if (!q) return;
  const keys = optKeys(q), has = (k) => keys.includes(k);
  const label = (k) => { const o = (q.options || []).find((x) => x.key === k); return o && o.label ? o.label : k; };
  const entries = q.entries || [];
  const cur = valueOf(q, "current"), chal = valueOf(q, "challenger");
  // A name pair: each name with its buttons, everywhere (a name form) or for this entry only.
  if (q.topic === "name_pair") {
    showQuestion(d, warnBox((q.warning || "").split("\n")[0]) +
      '<div class="qtext">Which name?</div><div class="qopts">' +
      '<div class="qrow"><div><span class="qlabel">Current</span><div class="qbib" style="font-size:13.5px">' + esc(cur ? cur.value : "") + '</div></div><span style="flex:1"></span>' +
        '<button class="plain" data-qopt="Y" title="Keep it, and map the other name to it in every entry">Keep everywhere</button><button class="plain" data-qopt="y" title="Keep it in this entry only">Keep here</button></div>' +
      '<div class="qrow"><div><span class="qlabel">Challenger</span><div class="qbib" style="font-size:13.5px">' + esc(chal ? chal.value : "") + '</div></div><span style="flex:1"></span>' +
        '<button class="plain" data-qopt="N" title="Take it, and map the current name to it in every entry">Take everywhere</button><button class="plain" data-qopt="n" title="Take it in this entry only">Take here</button></div></div>' +
      '<div class="muted" style="font-size:13px;margin:-2px 0 6px">Everywhere: remembered as a name form, for all entries. Here: this entry only.</div>' +
      '<div class="qactions"><button class="plain" id="qeditbtn" title="Type the canonical name: both names are mapped to it">Edit…</button>' +
      '<button class="plain" data-qopt="d" title="These are two different people: never ask again">Different people</button><span style="flex:1"></span>' + stopButton(q) + "</div>" +
      editHTML(cur ? cur.value : ""));
    wireEdit("e");
    return;
  }
  // A field challenge: the entry, a choice between the current and the challenging value,
  // for this entry or (when offered) all; Edit; further options; the challenging entry below.
  if (q.topic === "field_challenge") {
    const current = entries.find((e) => e.role === "Current entry"), challenging = entries.find((e) => e.role === "Challenging entry");
    const extra = keys.filter((o) => !["y", "n", "Y", "N", "e", "q", "s", "S"].includes(o));
    const forAll = has("Y") && has("N");
    const radio = (name, ans, v) => v ? '<label><input type="radio" name="qopt" value="' + ans + '"><div><span class="qlabel">' + name + '</span><div class="qbib" style="font-size:13.5px">' + esc(v.value) + "</div>" +
      (v.note ? '<div class="muted" style="font-size:12.5px">' + esc(v.note) + "</div>" : "") + "</div></label>" : "";
    showQuestion(d, (current && current.type ? entryCard(current, "For entry") : warnBox(q.warning)) +
      '<div class="qtext">Select as ' + esc(q.field) + ':</div><div class="qopts">' + radio("Current", "y", cur) + radio("Challenger", "n", chal) + "</div>" +
      (forAll ? '<label class="muted" style="display:block;margin:2px 0 8px;font-size:13px"><input type="checkbox" id="qforall"> for all entries (remembered: the other value is mapped to this one everywhere)</label>' : "") +
      '<div class="qactions"><button class="action" id="qconfirm" disabled>Confirm</button>' +
      (has("e") ? '<button class="plain" id="qeditbtn" title="Type the value to use">Edit…</button>' : "") +
      extra.map((o) => '<button class="plain" data-qopt="' + esc(o) + '">' + esc(cap(label(o))) + "</button>").join("") +
      '<span style="flex:1"></span>' + stopButton(q) + "</div>" + editHTML(cur ? cur.value : "") +
      (challenging && challenging.type ? '<div class="qtext" style="margin-top:18px">From challenging entry:</div>' + entryHTML(challenging, true) : ""));
    const cb = $("qconfirm");
    for (const r of $("mbody").querySelectorAll('input[name="qopt"]')) r.onchange = () => { cb.disabled = false; };
    cb.onclick = () => {
      const r = $("mbody").querySelector('input[name="qopt"]:checked');
      if (r) sendAnswer($("qforall") && $("qforall").checked ? r.value.toUpperCase() : r.value);
    };
    wireEdit("e");
    return;
  }
  // What it is about: the warning, the entries, the values.
  let html = warnBox(q.warning) + entries.map((e) => entryCard(e)).join("");
  if (q.values && q.values.length) html += '<table class="qvals" style="margin-bottom:8px">' + q.values.map((v) => '<tr><td class="f"><b>' + esc(cap(v.role)) + "</b></td><td>" + esc(v.value) +
    (v.note ? ' <span class="muted">(' + esc(v.note) + ")</span>" : "") + "</td></tr>").join("") + "</table>";
  // A typed answer (options, when given, as buttons besides).
  if (q.kind === "text") {
    showQuestion(d, html + '<div class="qtext">' + esc(q.text || "Your answer?") + "</div>" +
      '<div class="qactions"><input id="qtextin" placeholder="Your answer" autocomplete="off"><button class="action" id="qconfirm">Confirm</button>' +
      (q.options || []).map((o) => '<button class="plain" data-qopt="' + esc(o.key) + '">' + esc(cap(o.label || o.key)) + "</button>").join("") +
      '<button class="action danger" id="qstoptext" title="Stop the job">Stop</button></div>');
    $("qstoptext").onclick = () => { qWaiting(); $("dlgstop").onclick(); };
    const t = $("qtextin"); t.focus();
    t.onkeydown = (e) => { if (e.key === "Enter") sendAnswer("", t.value); };
    $("qconfirm").onclick = () => sendAnswer("", t.value);
    return;
  }
  // A yes/no question: two buttons, named after what they do where the question says.
  if (keys.every((k) => ["y", "n", "q"].includes(k)) && has("y") && has("n")) {
    const merge = q.topic === "merge";
    showQuestion(d, html + '<div class="qtext">' + esc(q.text) + "</div>" +
      '<div class="qactions"><button class="action" data-qopt="y">' + (merge ? "Merge" : esc(cap(label("y")))) + '</button><button class="plain" data-qopt="n">' + (merge ? "Keep" : esc(cap(label("n")))) + "</button>" +
      '<span style="flex:1"></span>' + stopButton(q) + "</div>");
    return;
  }
  // A choice: candidates (with their entries) or labelled options as radio buttons; "none"
  // (0), "enter key manually" (k: Lookup) and Stop as buttons of their own.
  const candidate = q.topic === "dblp_candidate" || q.topic === "library_candidate";
  const noneOpt = candidate && has("0") ? "0" : "", keyOpt = q.topic === "dblp_candidate" && has("k") ? "k" : "";
  const radios = (q.options || []).filter((o) => o.key !== "q" && o.key !== noneOpt && o.key !== keyOpt);
  const single = radios.length === 1 && radios[0].entry && q.topic === "dblp_candidate";
  html += '<div class="qtext">' + (single ? "Possible DBLP entry match:" : esc(q.text || "Your answer?")) + qPDFIcon(q) + "</div>" +
    '<div class="qopts">' + radios.map((o) => '<label><input type="radio" name="qopt" value="' + esc(o.key) + '"' + (single ? " checked" : "") + '><div style="flex:1">' +
      (o.entry ? entryHTML(o.entry, false) : '<span class="qlabel">' + esc(cap(o.label || o.key)) + "</span>") +
      (o.value ? ' <input class="qval" data-for="' + esc(o.key) + '" placeholder="' + esc(cap(o.value)) + '" autocomplete="off" style="width:min(420px,90%);margin-top:4px;font:13px ui-monospace, Menlo, monospace;padding:4px 8px;border:1px solid var(--border);border-radius:6px;background:var(--card);color:var(--text)">' : "") + "</div></label>").join("") + "</div>" +
    '<div class="qactions">' + (radios.length ? '<button class="action" id="qconfirm"' + (single ? "" : " disabled") + ">Confirm</button>" : "") +
    (keyOpt ? '<button class="plain" id="qlookup" title="Find the key in the local DBLP data">Lookup</button>' : "") +
    (noneOpt ? '<button class="plain" data-qopt="0">' + esc(cap(label("0"))) + "</button>" : "") +
    '<span style="flex:1"></span>' + stopButton(q) + "</div>";
  showQuestion(d, html);
  const confirmBtn = $("qconfirm");
  if (confirmBtn) {
    for (const r of $("mbody").querySelectorAll('input[name="qopt"]')) r.onchange = () => { confirmBtn.disabled = false; };
    for (const v of $("mbody").querySelectorAll("input.qval")) v.onfocus = () => { $("mbody").querySelector('input[name="qopt"][value="' + v.dataset.for + '"]').checked = true; confirmBtn.disabled = false; };
    confirmBtn.onclick = () => {
      const r = $("mbody").querySelector('input[name="qopt"]:checked');
      if (!r) return;
      const v = $("mbody").querySelector('input.qval[data-for="' + r.value + '"]');
      if (v && !v.value.trim()) { v.focus(); return; }
      sendAnswer(r.value, v ? v.value.trim() : "");
    };
  }
  if ($("qlookup")) $("qlookup").onclick = () => openLookup(keyOpt, entries[0]);
}
// The Lookup pop-up: finds a DBLP key in the local DBLP data; "Use" answers the question
// with "enter key manually" and then with that key.
function openLookup(keyOpt, entry, onUse) {
  const field = (name) => { const f = ((entry && entry.fields) || []).find((x) => x.name === name); return f ? f.value.replace(/[{}]/g, "") : ""; };
  const title = field("title"), firstAuthor = (field("author").split(" and ")[0] || "").trim();
  const authorDblp = firstAuthor.includes(",") ? firstAuthor.split(",").slice(1).join(",").trim() + " " + firstAuthor.split(",")[0].trim() : firstAuthor;
  popup("Lookup a DBLP key", '<div class="line"><input id="lkq" style="flex:1;min-width:260px" placeholder="title (whole), or a DBLP key" value="' + esc(title) + '">' +
    '<input id="lka" style="width:220px" placeholder="author as in DBLP (optional)" value=""></div>' +
    '<div class="line"><button class="action" id="lkgo">Search</button><button class="plain" id="lkauthor">Search by ' + esc(authorDblp || "author") + '</button>' +
    '<span style="flex:1"></span><button class="plain" id="lkback">' + (onUse ? "Close" : "Back to the question") + "</button></div>" +
    '<div class="muted" style="font-size:13px">A title is looked up whole (as the matching does); with an author, the words of the title filter their entries.</div>' +
    '<div id="lkres" style="margin-top:12px"></div>', false);
  qOpen = true;
  for (const i of $("mbody").querySelectorAll("input")) { i.style.font = "14px ui-monospace, Menlo, monospace"; i.style.padding = "6px 8px"; i.style.border = "1px solid var(--border)"; i.style.borderRadius = "6px"; i.style.background = "var(--card)"; i.style.color = "var(--text)"; }
  const search = async () => {
    $("lkres").innerHTML = '<p class="muted">Searching…</p>';
    const p = new URLSearchParams({ q: $("lkq").value.trim(), author: $("lka").value.trim() });
    let list = [];
    try { list = await get("/api/dblpsearch?" + p, "json"); } catch (e) { lost(e); }
    $("lkres").innerHTML = list.length ? '<div class="qopts">' + list.map((r) => '<label style="cursor:default"><div style="flex:1"><span class="qlabel key">' + esc(r.key) +
      "</span> (" + esc(r.fields.entrytype || "") + ')<div class="qbib"><table>' +
      Object.entries(r.fields).filter(([f]) => f !== "entrytype").map(([f, v]) => '<tr><td class="f">' + esc(f) + "</td><td>= {" + esc(v) + "},</td></tr>").join("") +
      '</table></div></div><button class="action" data-use="' + esc(r.key) + '">' + (onUse ? "Add to library" : "Use") + "</button></label>").join("") + "</div>"
      : '<p class="muted">Nothing found.' + ($("lka").value.trim() ? "" : " Try with the author, or part of the key.") + "</p>";
    for (const b of $("lkres").querySelectorAll("button[data-use]")) b.onclick = () => {
      if (onUse) { onUse(b.dataset.use); return; } // the Add section: the key into its field
      sendAnswer(keyOpt, b.dataset.use);
    };
  };
  $("lkgo").onclick = search;
  $("lkauthor").onclick = () => { $("lka").value = authorDblp; search(); };
  $("lkq").onkeydown = (e) => { if (e.key === "Enter") search(); };
  $("lka").onkeydown = (e) => { if (e.key === "Enter") search(); };
  $("lkback").onclick = () => { if (onUse) { closePopup(); return; } qShown = ""; openQuestion(); };
  search();
}
// qWaiting greys the pop-up's Confirm while the job takes the answer (the pop-up stays).
function qWaiting() {
  const b = $("qconfirm");
  if (b) { b.disabled = true; b.textContent = "Waiting for the job…"; }
  for (const el of $("mbody").querySelectorAll("input, .qactions button")) el.disabled = true;
}
// qEnded: the job ended while the pop-up was open.
function qEnded(d) {
  qShown = "";
  const b = $("qconfirm");
  if ((b && b.dataset.ended) || $("qclose")) return; // said once (a choice question has no Confirm button)
  $("mbody").insertAdjacentHTML("beforeend", '<div class="qwarn" style="margin-top:12px">The job ended' + (d.code ? " with code " + d.code : "") +
    '. Its output is in the Log tab.</div><div class="qactions"><button class="action" id="qclose">Close</button></div>');
  if (b) { b.dataset.ended = "1"; b.textContent = "Done"; }
  $("qclose").onclick = () => { qOpen = false; closePopup(); };
}
// sendAnswer answers the question shown: a choice, with the value that goes with it, or a
// text. It names the question: one answered already (in another tab) is refused.
async function sendAnswer(choice, text) {
  const d = dlg;
  if (!d || !d.q) return;
  const id = qShown; // the question in the pop-up, not one that came meanwhile
  qWaiting();
  try {
    const r = await fetch(BASE + "/api/job/" + d.job + "/answer", { method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question: id, choice: choice || "", text: text || "" }) });
    if (!r.ok) { toast(await r.text()); qShown = ""; } // not taken: shown again when it is still open
  } catch (e) { lost(e); }
  setTimeout(loadActions, 400);
}
$("dlgopen").onclick = openQuestion;
// DBLP Lookup: an entry found in the local DBLP cache is added to the library (with its parent).
$("dblplookup").onclick = () => openLookup(null, null, (key) => startAction("add_dblp", [key]));
$("dlgstop").onclick = async () => { try { if (dlg && dlg.job) await fetch(BASE + "/api/job/" + dlg.job + "/stop", { method: "POST" }); } catch (e) {} setTimeout(loadActions, 300); };
// startAction starts an action (with what it is given) and shows the Actions tab.
async function startAction(action, given) {
  try {
    const r = await fetch(BASE + "/api/actions/start", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, given: given || [] }) });
    if (!r.ok) { alert(await r.text()); return false; }
  } catch (e) { lost(e); return false; }
  closePopup();
  showTab("actions");
  setTimeout(loadActions, 300);
  return true;
}
for (const b of document.querySelectorAll("[data-action]")) b.onclick = async () => {
  try {
    const r = await fetch(BASE + "/api/actions/start", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: b.dataset.action }) });
    if (!r.ok) { alert(await r.text()); return; }
  } catch (e) { lost(e); }
  setTimeout(loadActions, 300);
};
onShow.actions = loadActions;
every("actions", 1000, loadActions);

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
const COPYICON = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
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
    // The fixed columns, before those of the layout: in a Select tab the selection (tick and
    // copy), and the PDF.
    this.fixed = sel ? [["sel", 56], ["pdf", 42]] : [["pdf", 42]];
    this.opW = this.fixed.reduce((w, f) => w + f[1], 0);
    const el = this.el;
    el.gridbox.addEventListener("scroll", () => {
      if (this.rows.length < this.total && el.gridbox.scrollTop + el.gridbox.clientHeight > el.gridbox.scrollHeight - 600) this.fetchRows(false);
    });
    let typing;
    el.q.addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(() => { this.query = el.q.value.trim(); this.reload(); }, 300); });
    el.q.addEventListener("keydown", (e) => { if (e.key === "Enter") { clearTimeout(typing); this.query = el.q.value.trim(); this.reload(); } });
    // A click anywhere on a row opens the entry, but on its buttons and tick (they act
    // themselves) and when it ends selecting text.
    // A PDF dropped on a row becomes that entry's PDF.
    let over = null;
    el.grid.addEventListener("dragover", (e) => {
      const tr = e.target.closest("tbody tr[data-key]");
      if (!tr || !draggingFiles(e)) return;
      e.preventDefault(); e.dataTransfer.dropEffect = "copy";
      if (over !== tr) { if (over) over.classList.remove("dropping"); over = tr; tr.classList.add("dropping"); }
    });
    el.grid.addEventListener("dragleave", (e) => { if (over && !over.contains(e.relatedTarget)) { over.classList.remove("dropping"); over = null; } });
    el.grid.addEventListener("drop", async (e) => {
      const tr = e.target.closest("tbody tr[data-key]");
      if (over) { over.classList.remove("dropping"); over = null; }
      if (!tr || !draggingFiles(e)) return;
      e.preventDefault();
      if (await uploadPDF(tr.dataset.key, e.dataTransfer.files[0])) {
        const row = this.rows.find((r) => r.k === tr.dataset.key);
        if (row) { row.pdf = true; const top = el.gridbox.scrollTop; this.renderRows(0); el.gridbox.scrollTop = top; }
      }
    });
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
      // The Selection filter; amber once a tick changed while it is on (the list shows the
      // selection as it was): a click then brings the list to the selection as it is now.
      el.selonly.onclick = () => {
        if (this.only && this.onlyStale) this.markOnlyStale(false);
        else { this.only = !this.only; el.selonly.classList.toggle("on", this.only); this.markOnlyStale(false); }
        this.reload();
      };
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
    cg.innerHTML = this.fixed.map((f) => '<col style="width:' + f[1] + 'px">').join("") + layout.cols.map((c) => '<col style="width:' + width(c) + 'px">').join("");
    tr.innerHTML = "";
    for (const [name] of this.fixed) {
      const th = document.createElement("th");
      th.className = "op";
      th.title = name === "sel" ? "Selected: sort on it" : "PDF: sort on having one";
      th.innerHTML = (name === "sel" ? '<span class="headtick">✓</span>' : PDFICON) +
        (this.sortKey() === name ? ((this.selSort ? this.selDesc : layout.desc) ? " ▼" : " ▲") : "");
      th.onclick = () => this.sortOn(name);
      tr.appendChild(th);
    }
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
    const col = this.el.grid.querySelectorAll("col")[i + this.fixed.length], x0 = ev.clientX, w0 = width(c);
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
  // copyHTML: for a selected entry, a button putting its local key (as in the .keys file) on
  // the clipboard, for citing it in LaTeX.
  copyHTML(r) {
    if (r.t !== 1 || !r.l) return '<span class="copyslot"></span>';
    return '<button class="copykey" data-copy="' + esc(r.l) + '" title="Copy ' + esc(r.l) + ' to the clipboard">' + COPYICON + "</button>";
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
      html.push('<tr data-key="' + esc(r.k) + '">' + (this.sel ? '<td class="op">' + this.tickHTML(r.t) + this.copyHTML(r) + "</td>" : "") + '<td class="op">' +
        (r.pdf ? '<button class="rowpdf" data-pdf="' + esc(r.k) + '" title="Open the PDF">' + PDFICON + "</button><span></span>"
               : r.get ? '<button class="rowpdf faded" data-getpdf="' + esc(r.k) + '" title="No PDF yet: download it (from its url or eprint)">' + PDFICON + "</button><span></span>" : "") + "</td>");
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
        return { k: r.k, cells, inh, pdf: !!r.p, get: !!r.g, t: r.t || 0, l: r.l || "" };
      });
      if (reset) this.rows = got; else this.rows.push(...got);
      this.renderRows(reset ? 0 : this.rows.length - got.length);
    } catch (e) { lost(e); if (my === this.gen) this.el.gridmore.textContent = String(e.message || e); }
    finally { if (my === this.gen) this.busy = false; }
  }
  reload() { this.header(); this.el.gridbox.scrollTop = 0; this.fetchRows(true); }
  // refreshRows fetches the rows shown again (a new index), keeping the scroll position.
  async refreshRows() {
    if (this.only && this.onlyStale) return; // the Selection button (amber) brings it up to date
    const top = this.el.gridbox.scrollTop, n = Math.max(this.rows.length, PAGE);
    const my = ++this.gen;
    const p = new URLSearchParams({ q: this.query, groups: [...this.groups].join("\n"), sort: this.sortKey(),
      desc: (this.selSort ? this.selDesc : layout.desc) ? "1" : "0", cols: layout.cols.join(","), from: 0, n: Math.min(n, 5000) });
    if (this.sel) { p.set("sel", this.sel.id); if (this.only) p.set("only", "1"); }
    try {
      const d = await get("/api/entries?" + p, "json");
      if (my !== this.gen) return;
      this.total = d.total;
      if (d.groups) this.showGroups(d.groups);
      this.rows = (d.rows || []).map((r) => {
        const cells = {}, inh = {};
        layout.cols.forEach((c, i) => { cells[c] = r.c[i]; });
        for (const i of r.i || []) inh[layout.cols[i]] = true;
        return { k: r.k, cells, inh, pdf: !!r.p, get: !!r.g, t: r.t || 0, l: r.l || "" };
      });
      this.renderRows(0);
      this.el.gridbox.scrollTop = top;
    } catch (e) { lost(e); }
  }

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
  markOnlyStale(stale) {
    this.onlyStale = stale;
    this.el.selonly.classList.toggle("stale", stale);
    this.el.selonly.title = stale ? "The list shows the selection as it was; click to show it as it is now"
                                  : "Show only the selected entries (green and amber)";
  }
  setTick(key, state) { // a tick changed elsewhere (an entry's pop-up)
    if (this.only) this.markOnlyStale(true);
    const row = this.rows.find((r) => r.k === key);
    if (row) row.t = state;
    const tr = this.el.grid.querySelector('tr[data-key="' + CSS.escape(key) + '"] .tick');
    if (tr) tr.outerHTML = this.tickHTML(state);
    this.refreshRows(); // its local key (the copy button) comes with the rows
  }
  async toggle(tick) {
    const tr = tick.closest("tr[data-key]");
    try {
      const d = await this.post("toggle", { key: tr.dataset.key });
      const row = this.rows.find((r) => r.k === tr.dataset.key);
      if (row) { row.t = d.state; row.l = d.local || ""; }
      const slot = tr.querySelector(".copykey, .copyslot");
      tick.outerHTML = this.tickHTML(d.state);
      if (slot && row) slot.outerHTML = this.copyHTML(row);
      if (this.only) this.markOnlyStale(true);
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
    $("tabs").appendChild(b); arrangeTabs(); // after Search (arrangeTabs)
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

// ---- table tabs: a mapping or alias table, listed; "+" adds, × deletes, a click edits ----
class TableView {
  constructor(panel, name) {
    this.panel = panel; this.name = name; this.rows = []; this.total = 0; this.gen = 0; this.query = ""; this.sort = ""; this.desc = false; this.busy = false;
    panel.appendChild($("tabletpl").content.cloneNode(true));
    const q = (c) => panel.querySelector("." + c);
    this.el = { q: q("tq"), count: q("tcount"), help: q("thelp"), box: q("tbox"), table: q("ttable"), more: q("tmore") };
    let typing;
    this.el.q.addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(() => { this.query = this.el.q.value.trim(); this.load(true); }, 300); });
    this.el.box.addEventListener("scroll", () => { const b = this.el.box; if (this.rows.length < this.total && b.scrollTop + b.clientHeight > b.scrollHeight - 400) this.load(false); });
    q("tadd").onclick = () => this.editRow(null);
    this.addBtn = q("tadd");
    this.el.table.addEventListener("click", (e) => {
      const del = e.target.closest("button[data-del]");
      const tr = e.target.closest("tbody tr[data-i]");
      if (!tr) return;
      const row = this.rows[+tr.dataset.i];
      if (del) { e.stopPropagation(); this.deleteRow(row); } else this.editRow(row);
    });
    this.started = false;
  }
  show() { if (!this.started) { this.started = true; this.load(true); } this.el.q.focus(); }
  async load(reset) {
    if (this.busy && !reset) return;
    const my = reset ? ++this.gen : this.gen; this.busy = true;
    const p = new URLSearchParams({ q: this.query, sort: this.sort, desc: this.desc ? "1" : "0", from: reset ? 0 : this.rows.length, n: 300 });
    try {
      const d = await get("/api/table/" + this.name + "?" + p, "json");
      if (my !== this.gen) return;
      this.def = d.table; this.total = d.total;
      this.el.help.textContent = d.table.help + (d.table.readonly ? " (Derived data: read-only.)" : "");
      this.addBtn.hidden = !!d.table.readonly;
      const got = d.rows.map((r) => Object.fromEntries(d.table.columns.map((c, i) => [c, r[i]])));
      this.rows = reset ? got : this.rows.concat(got);
      this.render();
    } catch (e) { lost(e); } finally { if (my === this.gen) this.busy = false; }
  }
  render() {
    const cols = this.def.columns;
    this.el.table.querySelector("thead tr").innerHTML = cols.map((c) => '<th data-col="' + esc(c) + '" style="cursor:pointer">' + esc(c) +
      (this.sort === c ? (this.desc ? " ▼" : " ▲") : "") + "</th>").join("") + "<th></th>";
    for (const th of this.el.table.querySelectorAll("th[data-col]")) th.onclick = () => {
      if (this.sort === th.dataset.col) this.desc = !this.desc; else { this.sort = th.dataset.col; this.desc = false; }
      this.load(true);
    };
    this.el.table.querySelector("tbody").innerHTML = this.rows.map((r, i) => '<tr data-i="' + i + '" style="cursor:pointer">' +
      cols.map((c) => '<td class="' + (/key|alias|hint|field/.test(c) ? "key" : "") + '">' + esc((r[c] || "").length > 300 ? r[c].slice(0, 300) + "…" : r[c]) + "</td>").join("") +
      '<td style="width:32px">' + (this.def.readonly ? "" : '<button class="plain" data-del="1" title="Delete this row">×</button>') + "</td></tr>").join("");
    this.el.count.textContent = this.total.toLocaleString() + " rows" + (this.query ? " match" : "");
    this.el.more.textContent = this.rows.length < this.total ? "Scroll for more (" + this.rows.length.toLocaleString() + " shown)…" : "";
  }
  editRow(row) {
    const cols = this.def.columns;
    if (this.def.readonly) {
      if (row) popup(this.def.title, '<table class="edit">' + cols.map((c) => '<tr><td class="field">' + esc(c) + '</td><td class="qbib">' + esc(row[c]) + "</td></tr>").join("") + "</table>", false);
      return;
    }
    popup((row ? "Change a row — " : "Add a row — ") + this.def.title, '<p class="muted" style="font-size:13px">' + esc(this.def.help) + "</p>" +
      '<table class="edit">' + cols.map((c) => '<tr><td class="field">' + esc(c) + '</td><td><textarea class="edval" rows="1" data-col="' + esc(c) + '">' +
        esc(row ? row[c] : "") + "</textarea></td></tr>").join("") + "</table>" +
      '<div class="qactions"><button class="action" id="tsave">' + (row ? "Save" : "Add") + "</button>" +
      (row ? '<button class="plain" id="tdel">Delete</button>' : "") + '<span style="flex:1"></span><button class="plain" id="tcancel">Cancel</button></div>' +
      '<div class="muted" id="tmsg" style="margin-top:8px"></div>', false);
    $("tcancel").onclick = closePopup;
    if (row) $("tdel").onclick = () => this.deleteRow(row);
    $("tsave").onclick = async () => {
      const vals = {};
      for (const t of $("mbody").querySelectorAll("textarea[data-col]")) vals[t.dataset.col] = t.value.trim();
      await this.change(row ? { op: "update", row: vals, old: row } : { op: "add", row: vals });
    };
    const first = $("mbody").querySelector("textarea"); if (first) first.focus();
  }
  async deleteRow(row) {
    if (!confirm("Delete this row from " + this.def.title + "?\n\n" + this.def.columns.map((c) => c + ": " + row[c]).join("\n"))) return;
    await this.change({ op: "delete", row });
  }
  async change(edit) {
    if ($("tmsg")) $("tmsg").textContent = "Saving…";
    try {
      const r = await fetch(BASE + "/api/table/" + this.name, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(edit) });
      if (!r.ok) { alert(await r.text()); return; }
      const d = await r.json();
      if (!d.ok) { if ($("tmsg")) $("tmsg").textContent = d.message; else alert(d.message); return; }
      closePopup(); toast(d.message); this.load(true);
    } catch (e) { lost(e); }
  }
}
// The Tables tab: every table (but the entries themselves), grouped; a table chosen there
// gets a tab in the tab bar (the choice is kept in the preferences).
let tableDefs = [];
const shownTables = () => load("tables", ["field_mappings", "name_forms", "old_keys", "key_aliases"]);
function addTableTab(name) {
  const def = tableDefs.find((t) => t.name === name);
  if (!def || $("panel-tbl-" + name)) return;
  const b = document.createElement("button");
  b.dataset.tab = "tbl-" + name; b.className = "seltab";
  b.innerHTML = esc(def.title) + '<span class="tabx" title="Close this tab (it can be opened again in the Tables tab)">×</span>';
  $("tabs").appendChild(b); // arrangeTabs puts it after the Tables tab
  const panel = document.createElement("section");
  panel.className = "panel fill"; panel.id = "panel-tbl-" + name; panel.hidden = true;
  $("panel-tables").parentNode.appendChild(panel);
  const v = new TableView(panel, name);
  v.closeSelect = () => { save("tables", shownTables().filter((n) => n !== name)); dropTableTab(name); renderTablesTab(); };
  views["tbl-" + name] = v;
  onShow["tbl-" + name] = () => v.show();
}
function dropTableTab(name) {
  const b = document.querySelector('.tabs button[data-tab="tbl-' + name + '"]');
  if (b) b.remove();
  if ($("panel-tbl-" + name)) $("panel-tbl-" + name).remove();
  delete views["tbl-" + name]; delete onShow["tbl-" + name];
  if (current === "tbl-" + name) showTab("tables");
}
function renderTablesTab() {
  const shown = new Set(shownTables()), groups = [];
  for (const t of tableDefs) { if (!groups.length || groups[groups.length - 1].name !== t.group) groups.push({ name: t.group, list: [] }); groups[groups.length - 1].list.push(t); }
  // A card per group, its name on a line of its own: the buttons wrap within it.
  $("tablelist").innerHTML = groups.map((g) => '<div class="card"><h2>' + esc(g.name) + '</h2><div style="display:flex;flex-wrap:wrap;gap:6px">' +
    g.list.map((t) => '<button class="toggle' + (shown.has(t.name) ? " on" : "") + '" data-tbl="' + esc(t.name) + '" title="' + esc(t.help) + '">' + esc(t.title) + "</button>").join("") + "</div></div>").join("");
  for (const b of $("tablelist").querySelectorAll("button[data-tbl]")) b.onclick = () => {
    const list = shownTables().filter((n) => n !== b.dataset.tbl);
    if (!b.classList.contains("on")) { list.push(b.dataset.tbl); addTableTab(b.dataset.tbl); arrangeTabs(); } else dropTableTab(b.dataset.tbl);
    save("tables", list);
    b.classList.toggle("on");
  };
}
async function setupTables() {
  try { tableDefs = await get("/api/tables", "json") || []; } catch (e) { lost(e); return; }
  // The tables renamed in step 22.4d keep their tabs.
  const renamed = { key_hints: "key_aliases", key_oldies: "old_keys" };
  if (shownTables().some((n) => renamed[n])) save("tables", shownTables().map((n) => renamed[n] || n));
  for (const name of shownTables()) addTableTab(name);
  arrangeTabs();
  renderTablesTab();
}

// ---- merging: marked entries or authors gather in a tray; the Merge tab compares them ----
let mergeTray = { entries: [], authors: [] };
function trayHTML() {
  const part = (kind, one, more) => mergeTray[kind].length ? '<span class="part"><b>' + mergeTray[kind].length + "</b> " +
    (mergeTray[kind].length === 1 ? one : more) + ' <button class="plain" data-tray="compare" data-kind="' + kind + '">Compare</button>' +
    '<button class="plain" data-tray="clear" data-kind="' + kind + '">Clear</button></span>' : "";
  return '<span class="muted">To merge:</span>' + part("entries", "entry", "entries") + part("authors", "contributor", "contributors");
}
function showTray() {
  let t = $("tray");
  if (!t) { t = document.createElement("div"); t.id = "tray"; document.body.appendChild(t);
    t.addEventListener("click", (e) => { const b = e.target.closest("button[data-tray]"); if (!b) return;
      if (b.dataset.tray === "clear") { mergeTray[b.dataset.kind] = []; save("mergetray", mergeTray); showTray(); }
      else openMerge(b.dataset.kind); }); }
  t.innerHTML = trayHTML();
  t.className = mergeTray.entries.length || mergeTray.authors.length ? "show" : "";
}
function markForMerge(kind, key) {
  const list = mergeTray[kind];
  if (list.includes(key)) { mergeTray[kind] = list.filter((k) => k !== key); toast("Taken out of the merge tray: " + key); }
  else { list.push(key); toast((kind === "entries" ? "Entry " : "Contributor ") + key + " marked for merge (" + list.length + ")"); }
  save("mergetray", mergeTray); showTray();
  for (const v of Object.values(views)) if (v instanceof MergeView && v.kind === kind) v.load();
}
class MergeView {
  constructor(panel, kind) { this.panel = panel; this.kind = kind; this.target = pendingMergeTarget; pendingMergeTarget = ""; this.load(); }
  async load() {
    const keys = mergeTray[this.kind];
    if (!keys.length) { this.panel.innerHTML = '<p class="muted">Nothing marked. Mark ' + this.kind + ' with "Mark for merge" in their pop-ups.</p>'; return; }
    let items = [];
    try { items = await get("/api/compare?kind=" + this.kind + "&keys=" + encodeURIComponent(keys.join(",")), "json"); } catch (e) { lost(e); return; }
    if (!this.target || !keys.includes(this.target)) { // the default one to keep
      const score = (it) => this.kind === "authors" ? (+it.fields.entries || 0) + (it.fields.orcid ? 1e6 : 0) : Object.keys(it.fields).length + (it.fields.dblp ? 1000 : 0);
      this.target = items.slice().sort((a, b) => score(b) - score(a))[0].key;
    }
    const order = this.kind === "authors" ? ["name", "orcid", "dblp", "name forms", "entries"]
      : ["entrytype", "author", "editor", "title", "booktitle", "journal", "crossref", "year", "volume", "number", "pages", "doi", "dblp", "preferredalias", "groups"];
    const fields = order.filter((f) => items.some((it) => it.fields[f]));
    for (const it of items) for (const f of Object.keys(it.fields).sort()) if (!fields.includes(f)) fields.push(f);
    // Where the entries have different values (more than one filled in), a choice per
    // field: one radio button per entry's value, and one for an edited value.
    const isEntries = this.kind === "entries";
    this.choices = this.choices || {};
    const differing = isEntries ? fields.filter((f) => new Set(items.map((it) => it.fields[f] || "").filter(Boolean)).size > 1) : [];
    for (const f of differing) {
      const kept = items.find((it) => it.key === this.target);
      if (!(f in this.choices)) this.choices[f] = { pick: kept && kept.fields[f] ? this.target : items.find((it) => it.fields[f]).key, edited: kept ? kept.fields[f] || "" : "" };
    }
    const head = '<tr><th></th>' + (differing.length ? '<th class="muted">edited</th>' : "") + items.map((it) => '<th><label style="cursor:pointer"><input type="radio" name="mkeep" value="' + esc(it.key) + '"' +
      (it.key === this.target ? " checked" : "") + "> keep</label> <span class=\"key\">" + esc(it.key) + '</span> <button class="plain" data-unmark="' + esc(it.key) +
      '" title="Take it out of the tray">×</button>' + (it.found ? "" : ' <span class="muted">(gone)</span>') + "</th>").join("") + "</tr>";
    const rows = fields.map((f) => {
      const vals = items.map((it) => it.fields[f] || "");
      const choose = differing.includes(f), ch = this.choices[f];
      const diff = new Set(vals).size > 1;
      const cell = (it, i) => '<td class="' + (it.key === this.target ? "kept" : "") + '">' +
        (choose && vals[i] ? '<label style="cursor:pointer;display:flex;gap:6px"><input type="radio" name="c:' + esc(f) + '" value="' + esc(it.key) + '"' +
          (ch.pick === it.key ? " checked" : "") + "><span>" + esc(vals[i]) + "</span></label>" : esc(vals[i])) + "</td>";
      const editCell = differing.length ? "<td>" + (choose ? '<label style="display:flex;gap:6px;align-items:flex-start"><input type="radio" name="c:' + esc(f) +
        '" value="__edited"' + (ch.pick === "__edited" ? " checked" : "") + '><textarea class="medit" data-field="' + esc(f) + '" rows="1" style="flex:1;font:12.5px ui-monospace, Menlo, monospace;padding:3px 6px;border:1px solid var(--border);border-radius:6px;background:var(--card);color:var(--text)">' +
        esc(ch.edited) + "</textarea></label>" : "") + "</td>" : "";
      return '<tr class="' + (diff ? "diff" : "") + '"><td class="f">' + esc(f) + "</td>" + editCell + items.map(cell).join("") + "</tr>";
    }).join("");
    this.panel.innerHTML = '<div class="editbar"><b>Merge ' + (this.kind === "authors" ? "contributors" : this.kind) + '</b><span class="muted">' + items.length + " marked" +
      (differing.length ? "; choose a value where they differ (" + differing.length + ")" : "") + "</span>" +
      '<button class="action" id="mgo">Merge into the kept one</button><button class="plain" id="mclose2">Close</button></div>' +
      '<div class="card" style="overflow:auto"><table class="compare">' + head + rows + "</table></div>" +
      '<p class="muted" style="font-size:13px">The others are merged into the one kept: the values chosen above, their other fields where it has none, their groups and PDFs; their keys stay as old keys' +
      (this.kind === "authors" ? " (their ids and name forms go to the one kept)" : "") + ". Anything still differing comes up as a question in the Actions tab.</p>";
    for (const r of this.panel.querySelectorAll('input[type=radio][name^="c:"]')) r.onchange = () => { this.choices[r.name.slice(2)].pick = r.value; };
    for (const t of this.panel.querySelectorAll("textarea.medit")) t.oninput = () => {
      const c = this.choices[t.dataset.field]; c.edited = t.value; c.pick = "__edited";
      const r = this.panel.querySelector('input[name="c:' + CSS.escape(t.dataset.field) + '"][value="__edited"]'); if (r) r.checked = true;
    };
    this.items = items;
    for (const r of this.panel.querySelectorAll('input[name="mkeep"]')) r.onchange = () => { this.target = r.value; this.choices = {}; this.load(); };
    for (const b of this.panel.querySelectorAll("button[data-unmark]")) b.onclick = () => markForMerge(this.kind, b.dataset.unmark);
    this.panel.querySelector("#mclose2").onclick = () => this.close();
    this.panel.querySelector("#mgo").onclick = () => this.merge(items);
  }
  // chosenValues: field → the value chosen (an entry's, or the edited one).
  chosenValues(items) {
    const out = {};
    for (const [f, c] of Object.entries(this.choices || {})) {
      if (c.pick === "__edited") out[f] = c.edited;
      else { const it = items.find((x) => x.key === c.pick); if (it && it.fields[f]) out[f] = it.fields[f]; }
    }
    return out;
  }
  async merge(items) {
    if (items.length < 2) { alert("Mark at least two to merge."); return; }
    if (!confirm("Merge " + (items.length - 1) + " into " + this.target + "?")) return;
    try {
      const r = await fetch(BASE + "/api/merge", { method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ kind: this.kind, keys: items.map((it) => it.key), target: this.target, choices: this.chosenValues(items) }) });
      if (!r.ok) { alert(await r.text()); return; }
    } catch (e) { lost(e); return; }
    mergeTray[this.kind] = []; save("mergetray", mergeTray); showTray();
    this.close();
    showTab("actions");
  }
  close() {
    const name = "merge-" + this.kind;
    const b = document.querySelector('.tabs button[data-tab="' + name + '"]');
    if (b) b.remove();
    this.panel.remove(); delete views[name];
    if (current === name) showTab("search");
  }
  closeSelect() { this.close(); }
}
// #merge-entries=k1,k2 (from a command line: bib.merge): those entries in the tray, the
// last one kept, the Merge tab open.
function openMergeFromHash(hash) {
  const m = hash.match(/^#merge-(entries|authors)(?:=(.*))?$/);
  if (!m) return;
  const kind = m[1];
  if (m[2]) {
    const keys = decodeURIComponent(m[2]).split(",").filter(Boolean);
    mergeTray[kind] = keys; save("mergetray", mergeTray); showTray();
    if (views["merge-" + kind]) views["merge-" + kind].target = keys[keys.length - 1];
    pendingMergeTarget = keys[keys.length - 1];
  }
  if (!current) showTab("search");
  openMerge(kind);
}
let pendingMergeTarget = "";
window.addEventListener("hashchange", () => { if (/^#merge-(entries|authors)=/.test(location.hash)) openMergeFromHash(location.hash); });
function openMerge(kind) {
  const name = "merge-" + kind;
  if (!views[name]) {
    const b = document.createElement("button");
    b.dataset.tab = name; b.className = "edittab";
    b.innerHTML = "Merge " + kind + '<span class="tabx" title="Close this tab">×</span>';
    $("tabs").appendChild(b); arrangeTabs();
    const panel = document.createElement("section");
    panel.className = "panel"; panel.id = "panel-" + name; panel.hidden = true;
    document.querySelector("main").appendChild(panel);
    views[name] = new MergeView(panel, kind);
  } else views[name].load();
  closePopup();
  showTab(name);
}

// ---- edit tabs (22.3c): an entry's form; Save runs bib.field (-set_field) per change ----
class EditView {
  constructor(panel, key) { this.panel = panel; this.key = key; this.orig = null; this.load(); }
  async load(message) {
    try { this.panel.innerHTML = await get("/frag/edit/" + encodeURIComponent(this.key)); }
    catch (e) { lost(e); this.panel.innerHTML = '<p class="muted">' + esc(e.message || e) + "</p>"; return; }
    const q = (c) => this.panel.querySelector("." + c);
    this.orig = JSON.parse(q("edstate").dataset.state);
    const alias = this.orig.fields.preferredalias; // the tab is named by the alias, when there is one
    const tab = document.querySelector('.tabs button[data-tab="' + CSS.escape("edit-" + this.key) + '"]');
    if (tab && alias) tab.innerHTML = "Edit: " + esc(alias) + '<span class="tabx" title="Close this tab">×</span>';
    if (message) this.say(message.text, message.output);
    q("edsave").onclick = () => this.save();
    q("edrevert").onclick = () => { if (!this.dirty() || confirm("Undo your changes to " + this.key + "?")) this.load(); };
    q("edclose").onclick = () => this.close();
    this.panel.addEventListener("input", () => this.mark());
    this.panel.addEventListener("change", () => this.mark());
    this.panel.querySelectorAll(".edremove").forEach((b) => b.onclick = () => { b.closest("tr").classList.add("removed"); this.mark(); });
    // The fields to add follow the entry type: those allowed for it, less those the entry has.
    const fillAddable = () => {
      const byType = JSON.parse(q("edadd").dataset.bytype || "{}"), sel = q("edadd");
      const have = new Set([...this.panel.querySelectorAll("table.edit tr[data-field]:not(.removed)")].map((tr) => tr.dataset.field));
      sel.innerHTML = '<option value="">Add a field…</option>' + (byType[q("edtype").value] || []).filter((f) => !have.has(f)).map((f) => "<option>" + esc(f) + "</option>").join("");
    };
    q("edtype").addEventListener("change", fillAddable);
    q("edadd").onchange = () => {
      const f = q("edadd").value; if (!f) return;
      q("edadd").querySelector('option[value="' + CSS.escape(f) + '"], option:checked').remove();
      q("edadd").value = "";
      const tr = document.createElement("tr");
      tr.dataset.field = f;
      tr.innerHTML = '<td class="field">' + esc(f) + '</td><td><textarea class="edval" rows="1"></textarea></td><td style="width:32px"><button class="plain edremove" title="Remove this field">×</button></td>';
      tr.querySelector(".edremove").onclick = () => { tr.remove(); this.mark(); };
      this.panel.querySelector("table.edit").appendChild(tr);
      tr.querySelector("textarea").focus();
    };
    const addGroup = (g) => {
      g = g.trim(); if (!g || this.groups().includes(g)) return;
      const c = document.createElement("span");
      c.className = "groupchip"; c.dataset.group = g;
      c.innerHTML = esc(g) + '<button title="Remove from this group">×</button>';
      q("edgroups").appendChild(c); this.mark();
    };
    q("edgroups").addEventListener("click", (e) => { const b = e.target.closest("button"); if (b) { b.closest(".groupchip").remove(); this.mark(); } });
    q("edgroupadd").onchange = () => { addGroup(q("edgroupadd").value); q("edgroupadd").value = ""; };
    q("edgroupnew").onkeydown = (e) => { if (e.key === "Enter") { addGroup(q("edgroupnew").value); q("edgroupnew").value = ""; } };
    this.mark();
  }
  groups() { return [...this.panel.querySelectorAll(".edgroups .groupchip")].map((c) => c.dataset.group).sort(); }
  collect() {
    const st = { type: this.panel.querySelector(".edtype").value, fields: {}, groups: this.groups() };
    for (const tr of this.panel.querySelectorAll("table.edit tr[data-field]")) {
      if (tr.classList.contains("removed")) continue;
      const v = tr.querySelector("textarea").value.trim();
      if (v !== "") st.fields[tr.dataset.field] = v;
    }
    return st;
  }
  dirty() {
    if (!this.orig) return false;
    const now = this.collect(), o = this.orig;
    if (now.type !== o.type || now.groups.join("\n") !== [...o.groups].sort().join("\n")) return true;
    const keys = new Set([...Object.keys(now.fields), ...Object.keys(o.fields)]);
    for (const k of keys) if ((now.fields[k] || "") !== (o.fields[k] || "")) return true;
    return false;
  }
  mark() { // a changed field in orange; the count in the bar
    if (!this.orig) return;
    for (const tr of this.panel.querySelectorAll("table.edit tr[data-field]")) {
      const v = tr.querySelector("textarea").value.trim();
      tr.classList.toggle("changed", tr.classList.contains("removed") || v !== (this.orig.fields[tr.dataset.field] || ""));
    }
    if (this.dirty()) this.say("Changes not saved.");
    else if (this.panel.querySelector(".edmsg").dataset.sticky !== "1") this.say("");
  }
  say(text, output) {
    const m = this.panel.querySelector(".edmsg");
    m.textContent = text || "";
    m.dataset.sticky = output !== undefined ? "1" : "";
    let pre = this.panel.querySelector("pre.edout");
    if (output) {
      if (!pre) { pre = document.createElement("pre"); pre.className = "edout"; this.panel.querySelector(".editbar").appendChild(pre); }
      pre.textContent = output;
    } else if (pre) pre.remove();
  }
  async save() {
    if (!this.dirty()) { this.say("Nothing changed."); return; }
    this.say("Saving…");
    try {
      const r = await fetch(BASE + "/api/edit/" + encodeURIComponent(this.key), { method: "POST",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify({ orig: this.orig, new: this.collect() }) });
      if (!r.ok) { this.say(await r.text()); return; }
      const d = await r.json();
      await this.load({ text: d.message, output: d.output || "" });
    } catch (e) { lost(e); this.say(String(e.message || e)); }
  }
  close() {
    if (this.dirty() && !confirm("Close the edit tab of " + this.key + " without saving your changes?")) return;
    const name = "edit-" + this.key;
    const b = document.querySelector('.tabs button[data-tab="' + CSS.escape(name) + '"]');
    if (b) b.remove();
    this.panel.remove();
    delete views[name];
    if (current === name) showTab("search");
  }
  closeSelect() { this.close(); } // the × on the tab
}
// An author's edit tab: the stored name (the old one kept as a name form), an ORCID to add.
class AuthorEditView {
  constructor(panel, id) { this.panel = panel; this.id = id; this.load(); }
  async load(message) {
    try { this.panel.innerHTML = await get("/frag/editauthor/" + encodeURIComponent(this.id)); }
    catch (e) { lost(e); this.panel.innerHTML = '<p class="muted">' + esc(e.message || e) + "</p>"; return; }
    const q = (c) => this.panel.querySelector("." + c);
    this.origName = q("edstate").dataset.name;
    const tab = document.querySelector('.tabs button[data-tab="' + CSS.escape("author-" + this.id) + '"]');
    if (tab) tab.innerHTML = "Contributor: " + esc(this.origName) + '<span class="tabx" title="Close this tab">×</span>';
    q("edsave").onclick = () => this.save();
    // The name forms are saved at once, each change as a job (the Name forms table).
    const formChange = async (edit) => {
      try {
        const r = await fetch(BASE + "/api/table/name_forms", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(edit) });
        const d = r.ok ? await r.json() : { ok: false, message: await r.text() };
        if (!d.ok) alert(d.message); else toast(d.message);
      } catch (e) { lost(e); }
      this.load();
    };
    q("edforms").addEventListener("click", (e) => {
      const b = e.target.closest("button"); if (!b) return;
      const form = b.closest(".groupchip").dataset.form;
      if (confirm("Remove the name form " + form + "?")) formChange({ op: "delete", row: { form, id: this.id } });
    });
    const addForm = () => { const v = q("edformnew").value.trim(); if (v) formChange({ op: "add", row: { form: v, id: this.id } }); };
    q("edformadd").onclick = addForm;
    q("edformnew").onkeydown = (e) => { if (e.key === "Enter") addForm(); };
    q("edcorrect").onclick = () => {
      const good = q("edgood").value.trim();
      if (!good) { q("edgood").focus(); return; }
      if (this.dirty() && !confirm("Your unsaved changes to this contributor are lost. Go on?")) return;
      startAction("correct_name", [q("edbad").value, good]).then((ok) => { if (ok) this.load(); });
    };
    q("edrevert").onclick = () => { if (!this.dirty() || confirm("Undo your changes?")) this.load(); };
    q("edclose").onclick = () => this.close();
    this.panel.addEventListener("input", () => this.mark());
    if (message) { q("edmsg").textContent = message.text; if (message.output) { const pre = document.createElement("pre"); pre.className = "edout"; pre.textContent = message.output; q("editbar").appendChild(pre); } }
  }
  values() { return { name: this.panel.querySelector(".edname").value.trim(), orcid: this.panel.querySelector(".edorcid").value.trim() }; }
  dirty() { const v = this.values(); return v.name !== this.origName || v.orcid !== ""; }
  mark() {
    const v = this.values();
    this.panel.querySelector(".edname").closest("tr").classList.toggle("changed", v.name !== this.origName);
    this.panel.querySelector(".edorcid").closest("tr").classList.toggle("changed", v.orcid !== "");
    this.panel.querySelector(".edmsg").textContent = this.dirty() ? "Changes not saved." : "";
  }
  async save() {
    if (!this.dirty()) { this.panel.querySelector(".edmsg").textContent = "Nothing changed."; return; }
    this.panel.querySelector(".edmsg").textContent = "Saving…";
    try {
      const r = await fetch(BASE + "/api/editauthor/" + encodeURIComponent(this.id), { method: "POST",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify(Object.assign({ origName: this.origName }, this.values())) });
      if (!r.ok) { this.panel.querySelector(".edmsg").textContent = await r.text(); return; }
      const d = await r.json();
      await this.load({ text: d.message, output: d.output || "" });
    } catch (e) { lost(e); this.panel.querySelector(".edmsg").textContent = String(e.message || e); }
  }
  close() {
    if (this.dirty() && !confirm("Close without saving your changes?")) return;
    const name = "author-" + this.id;
    const b = document.querySelector('.tabs button[data-tab="' + CSS.escape(name) + '"]');
    if (b) b.remove();
    this.panel.remove();
    delete views[name];
    if (current === name) showTab("search");
  }
  closeSelect() { this.close(); }
}
function openEditAuthor(id) {
  const name = "author-" + id;
  if (!views[name]) {
    const b = document.createElement("button");
    b.dataset.tab = name;
    b.className = "edittab";
    b.innerHTML = "Contributor: " + esc(id) + '<span class="tabx" title="Close this tab">×</span>';
    $("tabs").appendChild(b); arrangeTabs();
    const panel = document.createElement("section");
    panel.className = "panel";
    panel.id = "panel-" + name;
    panel.hidden = true;
    document.querySelector("main").appendChild(panel);
    views[name] = new AuthorEditView(panel, id);
  }
  closePopup();
  showTab(name);
}
function openEdit(key) {
  const name = "edit-" + key;
  if (!views[name]) {
    const b = document.createElement("button");
    b.dataset.tab = name;
    b.className = "edittab";
    b.title = "Edit " + key;
    b.innerHTML = "Edit: " + esc(key) + '<span class="tabx" title="Close this tab">×</span>';
    $("tabs").appendChild(b); arrangeTabs();
    const panel = document.createElement("section");
    panel.className = "panel";
    panel.id = "panel-" + name;
    panel.hidden = true;
    document.querySelector("main").appendChild(panel);
    views[name] = new EditView(panel, key);
  }
  closePopup();
  showTab(name);
}

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
  const links = [...$("mbody").querySelectorAll("table.list tr[data-key]")];
  if (!links.length) return;
  let states;
  try { states = await get("/api/selstates?sel=" + selID + "&keys=" + encodeURIComponent(links.map((tr) => tr.dataset.key).join(",")), "json"); }
  catch (e) { return; }
  const view = views["sel-" + selID];
  const label = view ? document.querySelector('.tabs button[data-tab="sel-' + selID + '"]').dataset.label : "";
  const head = $("mbody").querySelectorAll(".card h2");
  if (head.length > 1 && label) head[1].insertAdjacentHTML("beforeend", ' <span class="muted">— ticks: Select: ' + esc(label) + "</span>");
  for (const tr of links) {
    const key = tr.dataset.key;
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
function closePopup() { stack = []; qOpen = false; $("overlay").classList.remove("open"); }
$("mclose").onclick = closePopup;
// In a pop-up's list of entries (an author's), a click on a row opens the entry,
// but on its links and tick, and when it ends selecting text.
$("mbody").addEventListener("click", (e) => {
  const tr = e.target.closest("table.list tr[data-key]");
  if (!tr || e.target.closest("a, button, .selpick") || String(window.getSelection())) return;
  openEntry(tr.dataset.key);
});
// An author's list: the authored / edited ticks show the entries in a ticked role.
$("mbody").addEventListener("change", (e) => {
  if (!e.target.classList.contains("rolefilter")) return;
  const on = [...$("mbody").querySelectorAll(".rolefilter")].filter((b) => b.checked).map((b) => b.value);
  let n = 0;
  for (const tr of $("mbody").querySelectorAll("table.list tr[data-roles]")) {
    const show = tr.dataset.roles.split(" ").some((r) => on.includes(r));
    tr.hidden = !show;
    if (show) n++;
  }
  const count = $("mbody").querySelector(".rolecount");
  if (count) count.textContent = n;
});
// A PDF dropped on an entry's pop-up (anywhere on it, its title too), or on a row of
// an author's list.
$("overlay").addEventListener("dragover", (e) => {
  if (!draggingFiles(e) || !e.target.closest(".modal")) return;
  const top = stack[stack.length - 1];
  if (e.target.closest("table.list tr[data-key]") || (top && top.kind === "entry")) { e.preventDefault(); $("mbody").classList.add("dropping"); }
});
$("overlay").addEventListener("dragleave", (e) => { if (!$("overlay").contains(e.relatedTarget)) $("mbody").classList.remove("dropping"); });
$("overlay").addEventListener("drop", async (e) => {
  $("mbody").classList.remove("dropping");
  if (!draggingFiles(e)) return;
  const row = e.target.closest("table.list tr[data-key]"), top = stack[stack.length - 1];
  const key = row ? row.dataset.key : (top && top.kind === "entry" ? top.id : "");
  if (!key) return;
  e.preventDefault();
  if (await uploadPDF(key, e.dataTransfer.files[0])) show(top); // read the pop-up again: its PDF button
});
// A PDF dropped on an entry's edit tab (Erik, 2026-11-26).
document.addEventListener("dragover", (e) => {
  if (draggingFiles(e) && e.target.closest('section.panel[id^="panel-edit-"]')) e.preventDefault();
});
document.addEventListener("drop", async (e) => {
  const p = draggingFiles(e) && e.target.closest('section.panel[id^="panel-edit-"]');
  if (!p) return;
  e.preventDefault();
  const key = p.id.slice("panel-edit-".length);
  if (await uploadPDF(key, e.dataTransfer.files[0])) pdfChanged(key);
});
$("mback").onclick = () => { if (stack.length > 1) { stack.pop(); show(stack[stack.length - 1]); } };
$("overlay").addEventListener("mousedown", (e) => { if (e.target === $("overlay")) closePopup(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && $("overlay").classList.contains("open")) closePopup(); });

// ---- clicks inside fragments ----
document.addEventListener("click", async (e) => {
  const el = e.target.closest("[data-entry],[data-author],[data-pdf],[data-getpdf],[data-pdfremove],[data-edit],[data-editauthor],[data-copy],[data-mark],[data-start],#stop");
  if (!el) return;
  e.preventDefault();
  if (el.dataset.start) { // an action on this entry or author
    if (el.dataset.confirm && !confirm(el.dataset.confirm)) return;
    startAction(el.dataset.start, el.dataset.given ? JSON.parse(el.dataset.given) : []);
    return;
  }
  if (el.dataset.mark) { markForMerge(el.dataset.mark, el.dataset.key); return; }
  if (el.dataset.getpdf) { downloadPDF(el.dataset.getpdf); return; }
  if (el.dataset.copy) {
    try { await navigator.clipboard.writeText(el.dataset.copy); toast("Copied: " + el.dataset.copy); }
    catch (err) { prompt("Copy the key:", el.dataset.copy); }
    return;
  }
  if (el.dataset.pdfremove) {
    try {
      const r = await fetch(BASE + "/api/pdfremove/" + encodeURIComponent(el.dataset.pdfremove), { method: "POST" });
      if (!r.ok) { alert(await r.text()); return; }
      const d = await r.json();
      pdfChanged(el.dataset.pdfremove);
      toast(d.message, d.undo);
    } catch (err) { lost(err); }
    return;
  }
  if (el.dataset.editauthor) openEditAuthor(el.dataset.editauthor);
  else if (el.dataset.edit) openEdit(el.dataset.edit);
  else if (el.dataset.entry) openEntry(el.dataset.entry);
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
                    $("banner").textContent = "The service has stopped. Start it again with bib.manager -start, then open the UI with bib.manager -ui."; }
    }, 1000);
  }
});

(async () => {
await loadPrefs();
await setupTables();
mergeTray = Object.assign({ entries: [], authors: [] }, load("mergetray", {}));
showTray();
for (const b of $("tabs").querySelectorAll("button[data-tab]")) if (!linkedTab(b.dataset.tab)) b.draggable = true;
arrangeTabs();
layout = Object.assign(JSON.parse(JSON.stringify(defaultLayout)), load("layout", {}));
layout.widths = Object.assign({}, layout.widths);
views.search = new SearchView($("panel-search"), null);
onShow.search = () => views.search.show();
await refreshSelects();
// #<tab>, or #entry:<key> (the Search tab with that entry's pop-up);
// #<tab>/entry:<key> and #<tab>/author:<id>: that tab with the pop-up over it.
const deep = location.hash.match(/^#(?:([^/]*)\/)?(entry|author):(.+)$/);
if (deep) { showTab(deep[1] || "search"); const id = decodeURIComponent(deep[3]); open1(deep[2], id, id); }
else if (/^#merge-(entries|authors)(=|$)/.test(location.hash)) { openMergeFromHash(location.hash); }
else if (location.hash.startsWith("#author-")) { const id = decodeURIComponent(location.hash.slice(8)); showTab("search"); openEditAuthor(id); }
else if (location.hash.startsWith("#edit-")) { const key = decodeURIComponent(location.hash.slice(6)); showTab("search"); openEdit(key); }
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

{{define "people"}}{{range $i, $p := .}}{{if $i}} and {{end}}<a href="#" data-author="{{$p.ID}}" data-name="{{tex $p.Name}}">{{$p.Name}}</a>{{end}}{{end}}
{{define "peopletext"}}{{range $i, $p := .}}{{if $i}}, {{end}}<a href="#" data-author="{{$p.ID}}" data-name="{{tex $p.Name}}">{{tex $p.Name}}</a>{{end}}{{end}}

{{define "entry"}}
<div class="line"><button class="plain" data-edit="{{.Key}}" title="Edit this entry in an edit tab">Edit</button>
  <button class="plain" data-mark="entries" data-key="{{.Key}}" title="Collect it for a merge (the tray at the bottom)">Mark for merge</button>
  <button class="plain" data-start="check_entry" data-given="{{given .Key}}" title="Run the checks on this entry (in the Actions tab)">Check</button>
  <span style="flex:1"></span>
  <button class="plain" data-start="delete_entry" data-given="{{given .Key}}" title="Delete this entry (asks first, in the Actions tab)">Delete</button></div>
{{if .Reference}}<div class="card ref">{{.Reference}}</div>{{else}}<p class="muted">The reference (HTML) shows once the library is loaded or the running job is done (the busy light is then green); open the entry again then.</p>{{end}}
<div class="line"><span class="label">Groups</span>{{range .Groups}}<span class="chip">{{.}}</span>{{else}}<span class="muted">none</span>{{end}}</div>
{{if .PDFSource}}<div class="line"><span class="label">PDF</span><span class="muted">none yet</span><button class="plain" data-getpdf="{{.Key}}" title="Download it from {{.PDFSource}}">Download PDF</button></div>{{end}}
{{if .HasPDF}}<div class="line"><span class="label">PDF</span><button class="plain" data-pdfremove="{{.Key}}" title="Move this PDF to the Trash (Undo for a few minutes)" style="order:2">Remove</button><button class="pdf" data-pdf="{{.Key}}" title="Open the PDF"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M8 13h8M8 17h5"/></svg>Open the PDF</button><span class="muted"></span></div>{{end}}
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
{{if or .Authors .Editors}}<details class="card" style="font-size:14px"><summary class="muted" style="cursor:pointer">One of the authors or editors is someone else</summary>
  <p class="muted" style="font-size:13px">Gives that position a contributor of its own, with the same name, recorded as a different person. For two people with the same name that neither an ORCID nor DBLP tells apart.</p>
  <table class="list">{{$key := .Key}}{{range .Authors}}<tr><td>{{tex .Name}}</td><td class="muted">author {{.Position}}</td><td class="key">{{.ID}}</td><td style="text-align:right"><button class="plain" data-start="split_position" data-given="{{given $key "author" .Position}}" data-confirm="Make this position ({{.Name}}) a different person?">Someone else</button></td></tr>{{end}}
  {{range .Editors}}<tr><td>{{tex .Name}}</td><td class="muted">editor {{.Position}}</td><td class="key">{{.ID}}</td><td style="text-align:right"><button class="plain" data-start="split_position" data-given="{{given $key "editor" .Position}}" data-confirm="Make this position ({{.Name}}) a different person?">Someone else</button></td></tr>{{end}}</table></details>{{end}}
{{end}}

{{define "edit"}}
<div class="editbar"><b>Editing</b> <span class="key">{{.Key}}</span>{{if .Alias}}<span class="muted">({{.Alias}})</span>{{end}}
  <button class="action edsave">Save</button><button class="plain edrevert">Revert</button><button class="plain edclose">Close</button>
  {{if .PDFSource}}<button class="plain" data-getpdf="{{.Key}}" title="No PDF yet: download it from {{.PDFSource}}">Download PDF</button>{{end}}
  <span class="edmsg"></span></div>
<div class="edstate" hidden data-state="{{.State}}"></div>
{{if .Reference}}<div class="card ref">{{.Reference}}</div>{{end}}
<div class="card">
  <table class="edit">
    <tr><td class="field">entry type</td><td><select class="edtype">{{range .Types}}<option{{if eq . $.Type}} selected{{end}}>{{.}}</option>{{end}}</select></td><td></td></tr>
    {{range .Fields}}<tr data-field="{{.Name}}"><td class="field" title="{{if .Required}}required for this type{{end}}">{{.Name}}{{if .Required}} *{{end}}</td>
      <td><textarea class="edval" rows="{{if .Long}}2{{else}}1{{end}}">{{.Value}}</textarea>{{if .Output}}<div class="out">output: {{.Output}}</div>{{end}}</td>
      <td style="width:32px"><button class="plain edremove" title="Remove this field">×</button></td></tr>
    {{end}}
  </table>
  <div class="line" style="margin-top:10px"><select class="edadd" data-bytype="{{.AddableByType}}"><option value="">Add a field…</option>{{range .Addable}}<option>{{.}}</option>{{end}}</select>
    <span class="muted">* required for the type. A changed field is shown in orange; Save runs bib.field for each.</span></div>
</div>
<div class="card"><h2>Groups</h2>
  <div class="edgroups" style="display:flex;gap:6px;flex-wrap:wrap;margin-bottom:8px">{{range .Groups}}<span class="groupchip" data-group="{{.}}">{{.}}<button title="Remove from this group">×</button></span>{{end}}</div>
  <div class="line"><select class="edgroupadd"><option value="">Add to a group…</option>{{range .AllGroups}}<option>{{.}}</option>{{end}}</select>
    <input class="edgroupnew" placeholder="or a new group" style="font-size:13px;padding:4px 8px;border:1px solid var(--border);border-radius:8px;background:var(--card);color:var(--text)"></div>
</div>
{{end}}

{{define "editauthor"}}
<div class="editbar"><b>Editing contributor</b> <span class="key">{{.ID}}</span><span class="muted">({{.Entries}} entries)</span>
  <button class="action edsave">Save</button><button class="plain edrevert">Revert</button><button class="plain edclose">Close</button>
  <span class="edmsg"></span></div>
<div class="edstate" hidden data-name="{{.Name}}"></div>
<div class="card">
  <table class="edit">
    <tr><td class="field">name</td><td><textarea class="edname" rows="1">{{.Name}}</textarea>
      <div class="out">Changing it keeps the current name as another name form (an alias). A name that is another contributor's is refused: merge them instead (Mark for merge).</div></td></tr>
    <tr><td class="field">misspelt</td><td><div class="line" style="margin:0"><select class="edbad"><option value="{{.Name}}">{{.Name}}</option>{{range .Forms}}<option value="{{.}}">{{.}}</option>{{end}}</select>
      <input class="edgood" placeholder="the correct spelling" style="flex:1;font:13px ui-monospace, Menlo, monospace;padding:5px 8px;border:1px solid var(--border);border-radius:6px;background:var(--card);color:var(--text)"><button class="plain edcorrect">Correct everywhere</button></div>
      <div class="out">Replaces a wrong spelling in every entry; unlike a changed name, it is not kept as a name form. Asks first, in the Actions tab.</div></td></tr>
    <tr><td class="field">ORCID</td><td>{{range $i, $o := .ORCIDs}}{{if $i}}, {{end}}<a href="https://orcid.org/{{$o}}" target="_blank" rel="noopener">{{$o}}</a>{{else}}<span class="muted">none</span>{{end}}
      <textarea class="edorcid" rows="1" placeholder="add an ORCID: 0000-0000-0000-0000" style="margin-top:6px"></textarea></td></tr>
    <tr><td class="field">DBLP</td><td>{{if .DBLP}}<a href="https://dblp.org/pid/{{.DBLP}}.html" target="_blank" rel="noopener">{{.DBLP}}</a>{{else}}<span class="muted">none</span>{{end}}</td></tr>
    <tr><td class="field">name forms</td><td><div class="edforms" style="display:flex;gap:6px;flex-wrap:wrap;margin-bottom:6px">{{range .Forms}}<span class="groupchip" data-form="{{.}}">{{tex .}}<button title="Remove this name form">×</button></span>{{else}}<span class="muted">none</span>{{end}}</div>
      <div class="line" style="margin:0"><input class="edformnew" placeholder="add a name form, e.g. Dietz, J. L. G." style="flex:1;font:13px ui-monospace, Menlo, monospace;padding:5px 8px;border:1px solid var(--border);border-radius:6px;background:var(--card);color:var(--text)"><button class="plain edformadd">Add</button></div>
      <div class="out">A form found in an entry is taken as this contributor. Saved at once (not with Save).</div></td></tr>
  </table>
</div>
{{end}}

{{define "author"}}
<div class="line"><button class="plain" data-editauthor="{{.ID}}" title="Edit this contributor in an edit tab">Edit</button>
  <button class="plain" data-mark="authors" data-key="{{.ID}}" title="Collect them for a merge (the tray at the bottom)">Mark for merge</button></div>
<div class="card">
  <h2>{{tex .Name}} <span class="muted key">{{.ID}}</span></h2>
  <table class="list">
    <tr><td class="field">ORCID</td><td>{{range $i, $o := .ORCIDs}}{{if $i}}, {{end}}<a href="https://orcid.org/{{$o}}" target="_blank" rel="noopener">{{$o}}</a>{{else}}<span class="muted">none</span>{{end}}</td></tr>
    <tr><td class="field">DBLP</td><td>{{if .DBLP}}<a href="https://dblp.org/pid/{{.DBLP}}.html" target="_blank" rel="noopener">{{.DBLP}}</a>{{else}}<span class="muted">none</span>{{end}}</td></tr>
    <tr><td class="field">Other name forms</td><td>{{range $i, $f := .Forms}}{{if $i}}; {{end}}{{tex $f}}{{else}}<span class="muted">none</span>{{end}}</td></tr>
  </table>
</div>
<div class="card">
  <h2>Entries (<span class="rolecount">{{len .Results}}</span>)
    <label class="rolepick"><input type="checkbox" class="rolefilter" value="author" checked> authored ({{.Authored}})</label>
    <label class="rolepick"><input type="checkbox" class="rolefilter" value="editor" checked> edited ({{.Edited}})</label></h2>
  <table class="list">
    {{range .Results}}<tr data-key="{{.Key}}" data-roles="{{.Roles}}" title="{{.Key}}"><td class="year">{{.Year}}</td><td class="ref">{{if .HTML}}{{.HTML}}{{else}}{{if .Title}}{{tex .Title}}{{else}}<span class="key">{{.Key}}</span>{{end}}{{if .Authors}} — {{template "peopletext" .Authors}}{{end}}{{end}}</td>
      <td style="width:34px">{{if .HasPDF}}<button class="pdf rowpdf" data-pdf="{{.Key}}" title="Open the PDF"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/></svg></button>{{end}}</td></tr>{{end}}
  </table>
</div>
{{end}}
`
