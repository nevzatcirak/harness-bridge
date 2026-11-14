// Harness Bridge dashboard, slice 4: polling and the audit filter. Read-only: only
// GET requests, every value from the server is set with textContent, no markup is
// built from data, and nothing is evaluated (ADR-023 sections 3 and 4). The page is
// fully functional without this script.
(function () {
  'use strict';
  var app = document.getElementById('app');
  if (!app) { return; }

  // Audit feed: the filter runs in the browser only.
  var filter = document.getElementById('audit-filter');
  var auditRows = document.getElementById('audit-rows');
  if (filter && auditRows) {
    filter.addEventListener('input', function () {
      var needle = filter.value.toLowerCase();
      var rows = auditRows.children;
      for (var i = 0; i < rows.length; i++) {
        rows[i].hidden = needle !== '' && rows[i].textContent.toLowerCase().indexOf(needle) < 0;
      }
    });
  }

  var kind = app.getAttribute('data-poll');
  if (!kind || typeof fetch !== 'function') { return; }
  var url, visibleMs, hiddenMs;
  if (kind === 'list') {
    url = '/api/state';
    visibleMs = 5000; hiddenMs = 5000; // the list polls every 5 s
  } else if (kind === 'detail') {
    var h = app.getAttribute('data-harness'), id = app.getAttribute('data-session');
    if (!h || !id) { return; }
    url = '/api/state/session/' + encodeURIComponent(h) + '/' + encodeURIComponent(id);
    visibleMs = 2000; hiddenMs = 10000; // 2 s while visible, 10 s when hidden
  } else {
    return;
  }

  function el(tag, text, cls) {
    var e = document.createElement(tag);
    if (text !== undefined && text !== null) { e.textContent = String(text); }
    if (cls) { e.className = cls; }
    return e;
  }
  function cell(tr, text) { var td = el('td', text); tr.appendChild(td); return td; }
  function guardText(g) { return g && g.held ? 'held (' + (g.state || 'unknown') + ')' : 'none'; }
  function safeHref(href) { return typeof href === 'string' && href.indexOf('/session/') === 0 ? href : ''; }

  function renderList(m, live) {
    while (live.firstChild) { live.removeChild(live.firstChild); }
    if (m.message) { live.appendChild(el('p', m.message)); }
    if (m.sessions && m.sessions.length) {
      var table = el('table', null, 'sessions');
      var head = el('tr');
      ['Harness', 'Session', 'Title', 'Root', 'State', 'Guard', 'Current execution', 'Last activity'].forEach(function (t) { head.appendChild(el('th', t)); });
      table.appendChild(el('thead')).appendChild(head);
      var body = el('tbody');
      m.sessions.forEach(function (s) {
        var tr = el('tr');
        var badge = el('span', s.harness, 'badge harness-' + (s.harness === 'opencode' || s.harness === 'claudecode' ? s.harness : 'other'));
        cell(tr).appendChild(badge);
        var idCell = cell(tr), href = safeHref(s.href);
        if (href) { var a = el('a', s.id); a.setAttribute('href', href); idCell.appendChild(a); } else { idCell.textContent = s.id; }
        cell(tr, s.title);
        cell(tr).appendChild(el('code', s.root));
        cell(tr, s.state);
        cell(tr, guardText(s.guard));
        cell(tr, s.current_execution || '-');
        cell(tr, s.last_activity);
        body.appendChild(tr);
      });
      table.appendChild(body);
      live.appendChild(table);
    }
    if (m.truncated) { live.appendChild(el('p', m.truncated, 'note')); }
  }

  function renderDetail(m) {
    var s = m.session || {};
    var set = {title: s.title, root: s.root, state: s.state, guard: guardText(s.guard), current: s.current_execution || '-', last: s.last_activity};
    Object.keys(set).forEach(function (k) {
      var dd = app.querySelector('dd[data-f="' + k + '"]');
      if (dd) { if (k === 'root') { dd.textContent = ''; dd.appendChild(el('code', set[k])); } else { dd.textContent = set[k]; } }
    });
    var tbody = document.getElementById('exec-rows');
    if (!tbody) {
      // The page was rendered with no executions: a table appears only on a reload.
      if (m.executions && m.executions.length) { window.location.reload(); }
      return;
    }
    while (tbody.firstChild) { tbody.removeChild(tbody.firstChild); }
    (m.executions || []).forEach(function (x) {
      var tr = el('tr');
      cell(tr, x.id + (x.current ? ' (current)' : ''));
      cell(tr, x.state); cell(tr, x.started); cell(tr, x.updated); cell(tr, x.ended);
      tbody.appendChild(tr);
    });
  }

  var etag = null, timer = null, busy = false;
  function interval() { return document.hidden ? hiddenMs : visibleMs; }
  function schedule() { if (timer !== null) { clearTimeout(timer); } timer = setTimeout(poll, interval()); }

  function poll() {
    timer = null;
    if (busy) { schedule(); return; }
    busy = true;
    var headers = {'Accept': 'application/json'};
    if (etag) { headers['If-None-Match'] = etag; }
    fetch(url, {method: 'GET', headers: headers, credentials: 'same-origin', cache: 'no-store'}).then(function (r) {
      if (r.status === 304) { return null; }
      if (!r.ok) { return null; } // keep what is shown; the next poll retries (503 carries Retry-After)
      etag = r.headers.get('ETag');
      return r.json();
    }).then(function (m) {
      if (!m) { return; }
      if (kind === 'list') { var live = document.getElementById('live'); if (live) { renderList(m, live); } }
      else { renderDetail(m); }
    }).catch(function () { /* a failed poll changes nothing */ }).then(function () { busy = false; schedule(); });
  }

  document.addEventListener('visibilitychange', schedule);
  schedule();
})();
