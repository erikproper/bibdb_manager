package main

// The page templates of the web UI (web_ui.go). Look and feel after Home
// Assistant (Erik, 2026-11-22): a coloured top bar, cards on a light (or dark)
// background, clear status colours.

const webPages = `
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Library}} — bibdb</title>
{{if eq .Page "log"}}<meta http-equiv="refresh" content="3">{{end}}
<style>
:root {
  --bg: #fafafa; --card: #ffffff; --text: #212121; --muted: #727272;
  --primary: #03a9f4; --primary-text: #ffffff; --border: #e0e0e0;
  --ok: #43a047; --warn: #ffa600; --busy: #039be5; --shadow: 0 2px 2px rgba(0,0,0,.14), 0 1px 5px rgba(0,0,0,.12);
}
@media (prefers-color-scheme: dark) {
  :root { --bg: #111111; --card: #1c1c1c; --text: #e1e1e1; --muted: #9b9b9b;
          --primary: #0288d1; --border: #2f2f2f; --shadow: 0 2px 2px rgba(0,0,0,.5); }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text);
       font: 15px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
header { background: var(--primary); color: var(--primary-text); padding: 0 16px;
         display: flex; align-items: center; gap: 24px; height: 56px; }
header .title { font-size: 20px; font-weight: 400; }
header nav a { color: var(--primary-text); text-decoration: none; margin-right: 16px; opacity: .85; }
header nav a.current, header nav a:hover { opacity: 1; border-bottom: 2px solid var(--primary-text); }
main { padding: 16px; max-width: 1100px; margin: 0 auto; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 16px; }
.card { background: var(--card); border-radius: 12px; box-shadow: var(--shadow); padding: 16px; margin-bottom: 16px; }
.card h2 { margin: 0 0 12px; font-size: 16px; font-weight: 500; }
.state { display: flex; align-items: center; gap: 12px; }
.dot { width: 14px; height: 14px; border-radius: 50%; flex: none; }
.dot.ok { background: var(--ok); } .dot.warn { background: var(--warn); } .dot.busy { background: var(--busy); }
.muted { color: var(--muted); }
table { width: 100%; border-collapse: collapse; }
td, th { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--border); vertical-align: top; }
th { color: var(--muted); font-weight: 500; }
td.num { text-align: right; font-variant-numeric: tabular-nums; }
td.field { color: var(--muted); width: 160px; white-space: nowrap; }
a { color: var(--primary); }
input[type=search] { width: 100%; padding: 10px 12px; font-size: 16px; border: 1px solid var(--border);
                     border-radius: 8px; background: var(--card); color: var(--text); }
button { background: var(--primary); color: var(--primary-text); border: 0; border-radius: 8px;
         padding: 8px 16px; font-size: 14px; cursor: pointer; }
.chip { display: inline-block; padding: 2px 10px; border-radius: 12px; border: 1px solid var(--border);
        margin: 0 6px 6px 0; font-size: 13px; }
pre.log { margin: 0; white-space: pre-wrap; font: 12px/1.4 ui-monospace, Menlo, monospace; }
.key { font-family: ui-monospace, Menlo, monospace; font-size: 13px; }
.bib { font-family: ui-monospace, Menlo, monospace; font-size: 13px; }
.bib table { margin-left: 2ch; width: calc(100% - 2ch); }
.bib td { border-bottom: 0; padding: 1px 4px; }
.bib td.eq { width: 1ch; color: var(--muted); }
td.year { width: 4em; color: var(--muted); }
.ref { line-height: 1.5; }
a.open { text-decoration: none; font-weight: 600; padding: 0 4px; }
</style>
</head>
<body>
<header>
  <span class="title">{{.Library}}</span>
  <nav>
    <a href="{{.Base}}/" {{if eq .Page "status"}}class="current"{{end}}>Status</a>
    <a href="{{.Base}}/search" {{if or (eq .Page "search") (eq .Page "entry") (eq .Page "author")}}class="current"{{end}}>Search</a>
    <a href="{{.Base}}/stats" {{if eq .Page "stats"}}class="current"{{end}}>Statistics</a>
    <a href="{{.Base}}/log" {{if eq .Page "log"}}class="current"{{end}}>Log</a>
  </nav>
</header>
<main>
{{if eq .Page "status"}}{{template "status" .}}{{end}}
{{if eq .Page "stats"}}{{template "stats" .}}{{end}}
{{if eq .Page "search"}}{{template "search" .}}{{end}}
{{if eq .Page "entry"}}{{template "entry" .}}{{end}}
{{if eq .Page "author"}}{{template "author" .}}{{end}}
{{if eq .Page "log"}}{{template "log" .}}{{end}}
</main>
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
</div>
<div class="card">
  <form action="{{.Base}}/search"><input type="search" name="q" placeholder="Search: key, alias, title, author, DOI" autofocus></form>
</div>
{{end}}

{{define "stats"}}
<div class="card">
  <h2>Library statistics</h2>
  <table>{{range .Stats}}<tr><td>{{.Label}}</td><td class="num">{{.Value}}</td></tr>{{end}}</table>
</div>
{{end}}

{{define "search"}}
<div class="card">
  <form action="{{.Base}}/search"><input type="search" name="q" value="{{.Query}}" placeholder="Search: key, alias, title, author, DOI" autofocus></form>
</div>
{{if .Query}}
<div class="card">
  {{if .Results}}
  <table>
    <tr><th>Year</th><th colspan="3">Reference</th></tr>
    {{range .Results}}{{if .HTML}}<tr><td class="year">{{.Year}}</td><td colspan="3" class="ref">{{.HTML}} <a class="open" href="{{$.Base}}/entry/{{esc .Key}}" title="{{.Key}}">›</a></td></tr>
    {{else}}<tr><td class="year">{{.Year}}</td><td><a href="{{$.Base}}/entry/{{esc .Key}}">{{if .Title}}{{.Title}}{{else}}<span class="key">{{.Key}}</span>{{end}}</a></td><td>{{range $i, $p := .Authors}}{{if $i}} and {{end}}<a href="{{$.Base}}/author/{{esc $p.ID}}">{{$p.Name}}</a>{{end}}</td><td class="muted">{{.Type}}</td></tr>{{end}}{{end}}
  </table>
  {{if .More}}<p class="muted">The first 200 shown; refine the search.</p>{{end}}
  {{else}}<p class="muted">Nothing found.</p>{{end}}
</div>
{{end}}
{{end}}

{{define "entry"}}
{{if .Reference}}<div class="card ref">{{.Reference}}</div>{{end}}
<div class="card bib">
  <div>@{{.Type}}{<span class="key">{{.Key}}</span>,</div>
  <table>
    {{if .Authors}}<tr><td class="field">author</td><td class="eq">=</td><td>{{"{"}}{{range $i, $p := .Authors}}{{if $i}} and {{end}}<a href="{{$.Base}}/author/{{esc $p.ID}}">{{$p.Name}}</a>{{end}}{{"},"}}</td></tr>{{end}}
    {{if .Editors}}<tr><td class="field">editor</td><td class="eq">=</td><td>{{"{"}}{{range $i, $p := .Editors}}{{if $i}} and {{end}}<a href="{{$.Base}}/author/{{esc $p.ID}}">{{$p.Name}}</a>{{end}}{{"},"}}</td></tr>{{end}}
    {{range .Fields}}<tr><td class="field">{{.Name}}</td><td class="eq">=</td><td>{{"{"}}{{if .Link}}<a href="{{.Link}}" target="_blank" rel="noopener">{{.Value}}</a>{{else}}{{.Value}}{{end}}{{"},"}}</td></tr>{{end}}
  </table>
  <div>}</div>
</div>
<div class="cards">
  <div class="card">
    <h2>Groups</h2>
    {{range .Groups}}<span class="chip">{{.}}</span>{{else}}<span class="muted">none</span>{{end}}
  </div>
  <div class="card">
    <h2>PDF</h2>
    {{if .HasPDF}}<form method="post" action="{{.Base}}/pdf/{{esc .Key}}"><button>Open the PDF</button></form>
      {{if .Opened}}<p class="muted">Opened.</p>{{end}}
    {{else}}<span class="muted">none</span>{{end}}
  </div>
</div>
{{end}}

{{define "author"}}
<div class="card">
  <h2>{{.Name}} <span class="muted key">{{.ID}}</span></h2>
  <table>
    <tr><td class="field">ORCID</td><td>{{range $i, $o := .ORCIDs}}{{if $i}}, {{end}}<a href="https://orcid.org/{{$o}}" target="_blank" rel="noopener">{{$o}}</a>{{else}}<span class="muted">none</span>{{end}}</td></tr>
    <tr><td class="field">DBLP</td><td>{{if .DBLP}}<a href="https://dblp.org/pid/{{.DBLP}}.html" target="_blank" rel="noopener">{{.DBLP}}</a>{{else}}<span class="muted">none</span>{{end}}</td></tr>
    <tr><td class="field">Other name forms</td><td>{{range $i, $f := .Forms}}{{if $i}}; {{end}}{{$f}}{{else}}<span class="muted">none</span>{{end}}</td></tr>
  </table>
</div>
<div class="card">
  <h2>Entries ({{len .Results}})</h2>
  <table>
    <tr><th>Year</th><th colspan="3">Reference</th></tr>
    {{range .Results}}{{if .HTML}}<tr><td class="year">{{.Year}}</td><td colspan="3" class="ref">{{.HTML}} <a class="open" href="{{$.Base}}/entry/{{esc .Key}}" title="{{.Key}}">›</a></td></tr>
    {{else}}<tr><td class="year">{{.Year}}</td><td><a href="{{$.Base}}/entry/{{esc .Key}}">{{if .Title}}{{.Title}}{{else}}<span class="key">{{.Key}}</span>{{end}}</a></td><td>{{range $i, $p := .Authors}}{{if $i}} and {{end}}<a href="{{$.Base}}/author/{{esc $p.ID}}">{{$p.Name}}</a>{{end}}</td><td class="muted">{{.Type}}</td></tr>{{end}}{{end}}
  </table>
</div>
{{end}}

{{define "log"}}
<div class="card">
  <h2>Log <span class="muted">(the service and its jobs; refreshed every 3 seconds)</span></h2>
  <pre class="log" id="log">{{.Log}}</pre>
</div>
<script>window.scrollTo(0, document.body.scrollHeight);</script>
{{end}}
`
