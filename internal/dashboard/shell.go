package dashboard

import "html/template"

// The page shell of ADR-023 Amendment A1 (A1.3, A1.5): a skip link, a top bar with the destinations that
// exist, one main landmark with one h1, and the sessions screen (A1.4 item 1). Every page of the dashboard
// goes through this one template; nothing in it is inline (no style attribute, no event handler, no inline
// script), so the CSP of section 3 needs no hash and no nonce. Everything that comes from a Source is
// escaped by html/template; no value is ever converted to template.HTML, template.JS or template.URL.
//
// The session detail and the audit page keep their S0 content inside the shell until slices S2 and S3
// redesign them; the headings, captions and column scopes already follow A1.5.
var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<meta name="description" content="Read-only view of the sessions, executions and audit trail of this Cirak instance.">
<link rel="stylesheet" href="/dashboard.css">
</head>
<body>
<a class="skip" href="#content">Skip to content</a>
<header class="topbar"><div class="wrap bar">
<span class="brand">Cirak</span>
<details class="menu" id="menu" open><summary>Menu</summary><nav aria-label="Main"><ul>
<li><a href="/"{{if eq .Active "sessions"}} aria-current="page"{{end}}>Sessions</a></li>
<li><a href="/audit"{{if eq .Active "audit"}} aria-current="page"{{end}}>Audit</a></li>
</ul></nav></details>
<span class="spacer"></span>
<span class="label">Read-only</span>
</div></header>
<main id="content" tabindex="-1" class="wrap"{{if .Poll}} data-poll="{{.Poll}}"{{if .PollHarness}} data-harness="{{.PollHarness}}" data-session="{{.PollID}}"{{end}}{{end}}>
<p class="banner" id="stale" role="status" hidden></p>
{{if .ListPage}}<section aria-labelledby="h-main" class="cards">
<h1 id="h-main">Sessions</h1>
<div class="card">
<div class="toolbar" role="search">
<div class="filters" id="filters" hidden>
<label class="visually-hidden" for="f-q">Search sessions</label>
<input class="input" id="f-q" type="search" maxlength="64" autocomplete="off" placeholder="Search title, path or ID">
<label class="visually-hidden" for="f-state">State</label>
<select class="select" id="f-state"><option value="">All states</option><option value="running">Running</option><option value="waiting_for_permission">Waiting for permission</option><option value="idle">Idle</option><option value="unknown">Unknown</option></select>
<label class="visually-hidden" for="f-harness">Harness</label>
<select class="select" id="f-harness"><option value="">All harnesses</option><option value="opencode">OpenCode</option><option value="claudecode">Claude Code</option></select>
</div>
<a class="btn" href="{{.ToggleHref}}">{{.ToggleText}}</a>
<a class="btn btn--primary" href="{{.RefreshHref}}">Refresh</a>
<span class="label" id="status">{{if .CountText}}{{.CountText}}, updated <time datetime="{{.StatusISO}}" title="{{.StatusAbs}}">{{.StatusRel}}</time>{{end}}</span>
</div>
<div id="live">
{{with .State}}<div class="{{.Class}}"><h2>{{.Heading}}</h2><p>{{.Text}}</p>{{if .LinkHref}}<a class="btn" href="{{.LinkHref}}">{{.LinkText}}</a>{{end}}</div>
{{else}}<div class="table-wrap" tabindex="0" role="region" aria-labelledby="h-main">
<table>
<caption class="visually-hidden">Sessions known to this Bridge</caption>
<thead><tr><th scope="col">Harness</th><th scope="col">Session</th><th scope="col">Repository and path</th><th scope="col">Lifecycle root</th><th scope="col">State</th><th scope="col">Guard</th><th scope="col">Current execution</th><th scope="col">Last activity</th></tr></thead>
<tbody>
{{range .Rows}}<tr data-harness="{{.Harness}}" data-state="{{.State}}">
<td><span class="badge">{{.HarnessName}}</span></td>
<td>{{if .Title}}{{if .Href}}<a href="{{.Href}}">{{.Title}}</a>{{else}}{{.Title}}{{end}}{{if .Archived}} <span class="chip chip--outline">Archived</span>{{end}}<br><code>{{.ID}}</code>{{else}}{{if .Href}}<a href="{{.Href}}">{{.ID}}</a>{{else}}<code>{{.ID}}</code>{{end}}{{if .Archived}} <span class="chip chip--outline">Archived</span>{{end}}{{end}}</td>
<td class="path"><span title="{{.Root}}">{{.PathShort}}</span></td>
<td class="path">{{if .LifecycleRoot}}<span>{{.LifecycleRoot}}</span>{{else}}None{{end}}</td>
<td><span class="{{.StateClass}}" title="{{.State}}">{{.StateLabel}}</span></td>
<td>{{.GuardText}}</td>
<td>{{if .Current}}<code>{{.Current}}</code>{{else}}None{{end}}</td>
<td>{{if .LastISO}}<time datetime="{{.LastISO}}" title="{{.LastActivity}}">{{.LastRel}}</time>{{else}}-{{end}}</td>
</tr>
{{end}}</tbody></table></div>
{{if .Truncate}}<p class="label">{{.Truncate}}</p>{{end}}{{end}}
</div>
</div>
</section>{{end}}
{{with .Detail}}<section class="session cards" aria-labelledby="h-main">
<h1 id="h-main">Session <code>{{.ID}}</code></h1>
<p><a href="/">Back to the sessions</a></p>
<div class="card">
<dl class="kv">
<dt>Harness</dt><dd><span class="badge">{{.HarnessName}}</span></dd>
<dt>Title</dt><dd data-f="title">{{.Title}}</dd>
<dt>Root</dt><dd data-f="root"><code>{{.Root}}</code></dd>
<dt>Lifecycle root</dt><dd data-f="lifecycle">{{if .LifecycleRoot}}<code>{{.LifecycleRoot}}</code>{{else}}-{{end}}</dd>
<dt>State</dt><dd data-f="state">{{.State}}</dd>
<dt>Guard</dt><dd data-f="guard">{{if .Guard.Held}}held ({{.Guard.State}}){{else}}none{{end}}</dd>
<dt>Current execution</dt><dd data-f="current">{{if .Current}}{{.Current}}{{else}}-{{end}}</dd>
<dt>Last activity</dt><dd data-f="last">{{.LastActivity}}</dd>
</dl>
{{if $.Pending}}<p class="label">Claude Code: history and diffs unavailable (ADR-021 pending).</p>{{end}}
</div>
</section>
<section class="executions card" aria-labelledby="h-exec">
<h2 id="h-exec">Executions</h2>
{{if $.Executions}}<div class="table-wrap" tabindex="0" role="region" aria-labelledby="h-exec"><table>
<caption class="visually-hidden">Executions of this session recorded by this Bridge process</caption>
<thead><tr><th scope="col">Execution</th><th scope="col">State</th><th scope="col">Started</th><th scope="col">Updated</th><th scope="col">Ended</th></tr></thead>
<tbody id="exec-rows">
{{range $.Executions}}<tr><td>{{.ID}}{{if .Current}} (current){{end}}</td><td>{{.State}}</td><td>{{.Started}}</td><td>{{.Updated}}</td><td>{{.Ended}}</td></tr>
{{end}}</tbody></table></div>{{else}}<p>No executions recorded by this Bridge process.</p>{{end}}
{{if $.Truncate}}<p class="label">{{$.Truncate}}</p>{{end}}
</section>{{end}}
{{with .Diff}}<section class="changes card" aria-labelledby="h-changes">
<h2 id="h-changes">Changes</h2>
{{if .Pending}}<p class="label">diff unavailable (ADR-021 pending)</p>{{end}}
{{with .Working}}{{template "diffview" .}}{{end}}
{{with .Single}}{{template "diffview" .}}{{end}}
{{if .TurnsNote}}<p class="label">{{.TurnsNote}}</p>{{end}}
{{if .Turns}}<h3 id="h-turns">Turns</h3><div class="table-wrap" tabindex="0" role="region" aria-labelledby="h-turns"><table class="turns">
<caption class="visually-hidden">User turns of this session</caption>
<thead><tr><th scope="col">Turn</th><th scope="col">Time</th><th scope="col">Diff</th></tr></thead>
<tbody>
{{range .Turns}}<tr><td>{{if .Href}}<a href="{{.Href}}">{{.ID}}</a>{{else}}{{.ID}}{{end}}</td><td>{{.Time}}</td><td>{{.Label}}</td></tr>
{{end}}</tbody></table></div>{{end}}
</section>{{end}}
{{if .BackHref}}<p><a href="{{.BackHref}}">Back to the session</a></p>{{end}}
{{if .AuditPage}}<section class="audit cards" aria-labelledby="h-main">
<h1 id="h-main">Audit</h1>
<div class="card">
{{if .AuditMsg}}<p class="label">{{.AuditMsg}}</p>{{end}}
{{if .AuditOn}}<div class="filters" id="filters" hidden><label for="audit-filter">Filter</label> <input class="input" type="search" id="audit-filter" maxlength="64" autocomplete="off"></div>
{{if .Audit}}<div class="table-wrap" tabindex="0" role="region" aria-labelledby="h-main"><table class="audit-feed">
<caption class="visually-hidden">The audit ring of this Bridge process, newest first</caption>
<thead><tr><th scope="col">Time</th><th scope="col">Event</th><th scope="col">Outcome</th><th scope="col">Error</th><th scope="col">Session</th><th scope="col">Execution</th><th scope="col">Principal</th><th scope="col">Other</th></tr></thead>
<tbody id="audit-rows">
{{range .Audit}}<tr><td>{{.Time}}</td><td>{{.Event}}</td><td>{{.Outcome}}</td><td>{{.Error}}</td><td>{{.Session}}</td><td>{{.Execution}}</td><td>{{.Principal}}</td><td>{{.Other}}</td></tr>
{{end}}</tbody></table></div>{{end}}{{end}}
</div>
</section>{{end}}
</main>
<script src="/dashboard.js"></script>
</body>
</html>
{{define "diffview"}}<div class="diff"><p><span class="badge label-{{.LabelClass}}">{{.Label}}</span> {{.Scope}}{{if .Reason}} ({{.Reason}}){{end}}</p>
{{if .Redaction}}<p class="label">{{.Redaction}}</p>{{end}}
{{range .Files}}<section class="file"><h3>{{.Path}} <span class="status">{{.Status}}</span> +{{.Additions}} -{{.Deletions}}</h3>
{{if .Note}}<p class="label">{{.Note}}</p>{{end}}{{if .ShowPatch}}<pre>{{.Patch}}</pre>{{end}}</section>
{{end}}{{if .More}}<p class="label">{{.More}}</p>{{end}}</div>{{end}}
`))
