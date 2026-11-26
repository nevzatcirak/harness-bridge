// Cirak dashboard script (ADR-023 A1.6, S1 and S2). GET only; server values go in through textContent or
// setAttribute; no markup is built from data; nothing is evaluated. Every page works without it.
(function () {
  'use strict';
  var doc = document;
  var app = doc.getElementById('content');
  if (!app) { return; }

  function byId(id) { return doc.getElementById(id); }
  function el(tag, text, cls) {
    var e = doc.createElement(tag);
    if (cls) { e.className = cls; }
    if (text !== undefined && text !== null) { e.textContent = String(text); }
    return e;
  }
  function clear(e) { while (e.firstChild) { e.removeChild(e.firstChild); } }
  function show(e) { if (e) { e.hidden = false; if (e.removeAttribute) { e.removeAttribute('hidden'); } } }
  function safeHref(href) { return typeof href === 'string' && href.indexOf('/session/') === 0 ? href : ''; }

  // as display.go relTime
  function rel(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) { return '-'; }
    var s = Math.floor((Date.now() - t) / 1000);
    if (s < 0) { s = 0; }
    if (s < 60) { return s + ' s ago'; }
    if (s < 3600) { return Math.floor(s / 60) + ' min ago'; }
    if (s < 86400) { return Math.floor(s / 3600) + ' h ago'; }
    return Math.floor(s / 86400) + ' d ago';
  }
  function chipClass(tone) { return /^(ok|info|secondary|error|warn|outline)$/.test(tone) ? 'chip chip--' + tone : 'chip'; }

  // JS-6: menu open below 60rem
  var menu = byId('menu');
  if (menu && typeof window.matchMedia === 'function') {
    var narrow = window.matchMedia('(max-width: 60rem)');
    menu.open = !narrow.matches;
    if (narrow.addEventListener) { narrow.addEventListener('change', function (e) { menu.open = !e.matches; }); }
  }

  // JS-4: filters, state in the URL
  var archived = /(^|[?&])archived=1(&|$)/.test(window.location.search);
  var query = {};
  window.location.search.replace(/^\?/, '').split('&').forEach(function (pair) {
    var i = pair.indexOf('=');
    if (i < 1) { return; }
    try { query[decodeURIComponent(pair.slice(0, i))] = decodeURIComponent(pair.slice(i + 1).replace(/\+/g, ' ')); } catch (e) { /* a malformed escape is ignored */ }
  });
  var fq = byId('f-q'), fstate = byId('f-state'), fharness = byId('f-harness');
  var auditFilter = byId('audit-filter'), auditRows = byId('audit-rows');

  function allowed(select, value) {
    if (!select || !select.options) { return ''; }
    for (var i = 0; i < select.options.length; i++) { if (select.options[i].value === value) { return value; } }
    return '';
  }
  function filterRows(rows, needle, state, harness) {
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i];
      r.hidden = (needle !== '' && r.textContent.toLowerCase().indexOf(needle) < 0) ||
        (state !== '' && r.getAttribute('data-state') !== state) ||
        (harness !== '' && r.getAttribute('data-harness') !== harness);
    }
  }
  function writeUrl(q, state, harness) {
    var parts = [];
    if (archived) { parts.push('archived=1'); }
    if (q) { parts.push('q=' + encodeURIComponent(q)); }
    if (state) { parts.push('state=' + encodeURIComponent(state)); }
    if (harness) { parts.push('harness=' + encodeURIComponent(harness)); }
    if (window.history && typeof window.history.replaceState === 'function') {
      window.history.replaceState(null, '', window.location.pathname + (parts.length ? '?' + parts.join('&') : ''));
    }
  }
  function applySessionFilter() {
    var live = byId('live');
    var body = live ? live.querySelector('tbody') : null;
    var q = fq.value.slice(0, 64), state = fstate.value, harness = fharness.value;
    if (body) { filterRows(body.children, q.toLowerCase(), state, harness); }
    writeUrl(q, state, harness);
  }
  if (fq && fstate && fharness) {
    fq.value = (query.q || '').slice(0, 64);
    fstate.value = allowed(fstate, query.state || '');
    fharness.value = allowed(fharness, query.harness || '');
    show(byId('filters'));
    [fq, fstate, fharness].forEach(function (c) { c.addEventListener('input', applySessionFilter); c.addEventListener('change', applySessionFilter); });
    applySessionFilter();
  } else if (auditFilter && auditRows) {
    auditFilter.value = (query.q || '').slice(0, 64);
    show(byId('filters'));
    var applyAudit = function () {
      var q = auditFilter.value.slice(0, 64);
      filterRows(auditRows.children, q.toLowerCase(), '', '');
      writeUrl(q, '', '');
    };
    auditFilter.addEventListener('input', applyAudit);
    applyAudit();
  }

  // the sessions table, rebuilt from the model
  function cell(tr, text, cls) { var td = el('td', text, cls); tr.appendChild(td); return td; }
  function sessionRow(s) {
    var tr = el('tr');
    tr.setAttribute('data-harness', s.harness);
    tr.setAttribute('data-state', s.state);
    cell(tr).appendChild(el('span', s.harness_name, 'badge'));
    var idCell = cell(tr), href = safeHref(s.href);
    function archivedChip() {
      if (s.archived) { idCell.appendChild(doc.createTextNode(' ')); idCell.appendChild(el('span', 'Archived', 'chip chip--outline')); }
    }
    if (s.title) {
      if (href) { var a = el('a', s.title); a.setAttribute('href', href); idCell.appendChild(a); } else { idCell.appendChild(doc.createTextNode(s.title)); }
      archivedChip();
      idCell.appendChild(el('br'));
      idCell.appendChild(el('code', s.id));
    } else {
      if (href) { var b = el('a', s.id); b.setAttribute('href', href); idCell.appendChild(b); } else { idCell.appendChild(el('code', s.id)); }
      archivedChip();
    }
    var path = el('span', s.path_short);
    path.setAttribute('title', s.root);
    cell(tr, null, 'path').appendChild(path);
    var life = cell(tr, null, 'path');
    if (s.lifecycle_root) { life.appendChild(el('span', s.lifecycle_root)); } else { life.textContent = 'None'; }
    var chip = el('span', s.state_label, chipClass(s.state_tone));
    chip.setAttribute('title', s.state);
    cell(tr).appendChild(chip);
    cell(tr, s.guard_text);
    var cur = cell(tr);
    if (s.current_execution) { cur.appendChild(el('code', s.current_execution)); } else { cur.textContent = 'None'; }
    var last = cell(tr);
    if (s.last_activity_iso) {
      var t = el('time', rel(s.last_activity_iso));
      t.setAttribute('datetime', s.last_activity_iso);
      t.setAttribute('title', s.last_activity);
      last.appendChild(t);
    } else { last.textContent = '-'; }
    return tr;
  }
  function stateBlock(heading, text, href, linkText) {
    var d = el('div', null, 'state');
    d.appendChild(el('h2', heading));
    d.appendChild(el('p', text));
    if (href) { var a = el('a', linkText, 'btn'); a.setAttribute('href', href); d.appendChild(a); }
    return d;
  }
  function renderList(m, live) {
    clear(live);
    if (!m.connected) { live.appendChild(stateBlock('No data source', 'No data source is connected.')); return; }
    if (!m.sessions || !m.sessions.length) {
      live.appendChild(stateBlock('No sessions', 'This Bridge lists no session you may see. Archived sessions are hidden unless you ask for them.',
        archived ? '/' : '/?archived=1', archived ? 'Hide archived' : 'Show archived'));
      return;
    }
    var wrap = el('div', null, 'table-wrap');
    wrap.setAttribute('tabindex', '0');
    wrap.setAttribute('role', 'region');
    wrap.setAttribute('aria-labelledby', 'h-main');
    var table = el('table');
    table.appendChild(el('caption', 'Sessions known to this Bridge', 'visually-hidden'));
    var head = el('tr');
    ['Harness', 'Session', 'Repository and path', 'Lifecycle root', 'State', 'Guard', 'Current execution', 'Last activity'].forEach(function (t) {
      var th = el('th', t);
      th.setAttribute('scope', 'col');
      head.appendChild(th);
    });
    table.appendChild(el('thead')).appendChild(head);
    var body = el('tbody');
    m.sessions.forEach(function (s) { body.appendChild(sessionRow(s)); });
    table.appendChild(body);
    wrap.appendChild(table);
    live.appendChild(wrap);
    if (m.truncated) { live.appendChild(el('p', m.truncated, 'label')); }
    if (fq && fstate && fharness) { applySessionFilter(); }
  }
  function setStatus(m) {
    var status = byId('status');
    if (!status || !m.count_text) { return; }
    clear(status);
    status.appendChild(doc.createTextNode(m.count_text + ', updated '));
    var iso = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
    var t = el('time', rel(iso));
    t.setAttribute('datetime', iso);
    t.setAttribute('title', iso.slice(0, 10) + ' ' + iso.slice(11, 19) + 'Z');
    status.appendChild(t);
  }

  // parts: as the server renders them
  function addParts(node, ps) {
    (ps || []).forEach(function (p) {
      if (p.kind === 'chip') { node.appendChild(el('span', p.text, chipClass(p.tone))); }
      else if (p.kind === 'code') { node.appendChild(el('code', p.text)); }
      else if (p.kind === 'time') {
        var t = el('time', rel(p.iso));
        t.setAttribute('datetime', p.iso);
        t.setAttribute('title', p.abs);
        node.appendChild(t);
        node.appendChild(doc.createTextNode(', ' + p.abs));
      } else { node.appendChild(doc.createTextNode(p.text)); }
    });
  }
  function renderReport(box, r) {
    clear(box);
    (r.sentences || []).forEach(function (t) { box.appendChild(el('p', t, 'report')); });
    var dl = el('dl', null, 'kv');
    (r.rows || []).forEach(function (row) {
      dl.appendChild(el('dt', row.label));
      var dd = el('dd');
      addParts(dd, row.parts);
      dl.appendChild(dd);
    });
    box.appendChild(dl);
    box.appendChild(el('p', r.note, 'label'));
  }
  function renderDetail(m) {
    var s = m.session || {};
    function field(k) { return app.querySelector('dd[data-f="' + k + '"]'); }
    var d = field('title');
    if (d) { d.textContent = s.title || ''; }
    if ((d = field('root'))) { clear(d); d.appendChild(el('code', s.root)); }
    if ((d = field('lifecycle'))) { clear(d); if (s.lifecycle_root) { d.appendChild(el('code', s.lifecycle_root)); } else { d.textContent = '-'; } }
    if ((d = field('state'))) { clear(d); var c = el('span', s.state_label, chipClass(s.state_tone)); c.setAttribute('title', s.state); d.appendChild(c); }
    if ((d = field('guard'))) { d.textContent = s.guard_text; }
    if ((d = field('current'))) { clear(d); if (s.current_execution) { d.appendChild(el('code', s.current_execution)); } else { d.textContent = 'None'; } }
    if ((d = field('last'))) { clear(d); addParts(d, [s.last_activity_iso ? { kind: 'time', iso: s.last_activity_iso, abs: s.last_activity } : { kind: 'text', text: '-' }]); }
    // the timeline changed shape: reload
    if (m.sig !== app.getAttribute('data-sig')) { window.location.reload(); return; }
    var cur = (m.executions || []).filter(function (x) { return x.current; })[0];
    var box = byId('report'), perm = byId('perm');
    if (cur && cur.report && box) { renderReport(box, cur.report); }
    if (cur && cur.permission && perm) {
      clear(perm);
      perm.appendChild(el('span', cur.permission.label, chipClass(cur.permission.tone)));
      perm.appendChild(doc.createTextNode(' '));
      addParts(perm, cur.permission.parts);
    }
  }

  // JS-5: relative times every 30 s
  var tickTimer = null;
  function refreshTimes() {
    var ts = app.querySelectorAll('time');
    for (var i = 0; i < ts.length; i++) {
      var iso = ts[i].getAttribute('datetime');
      if (iso) { ts[i].textContent = rel(iso); }
    }
  }
  function tick() {
    tickTimer = null;
    refreshTimes();
    if (!doc.hidden) { tickTimer = setTimeout(tick, 30000); }
  }

  // JS-3, A1.7: poll, pause while hidden, back off
  var kind = app.getAttribute('data-poll');
  var base = kind === 'list' ? 5000 : (kind === 'detail' ? 2000 : 0);
  var url = null;
  if (kind === 'list') { url = '/api/state' + (archived ? '?archived=1' : ''); }
  if (kind === 'detail') {
    var h = app.getAttribute('data-harness'), id = app.getAttribute('data-session');
    if (h && id) { url = '/api/state/session/' + encodeURIComponent(h) + '/' + encodeURIComponent(id) + (archived ? '?archived=1' : ''); }
  }
  var etag = null, fresh = null, timer = null, ctl = null, failures = 0, retryMs = 0, lastOk = Date.now();
  var statusEl = byId('status');
  var statusTime = statusEl ? statusEl.querySelector('time') : null;
  if (statusTime && statusTime.getAttribute('datetime') && !isNaN(Date.parse(statusTime.getAttribute('datetime')))) {
    lastOk = Date.parse(statusTime.getAttribute('datetime'));
  }

  // failure delay: max(interval, Retry-After), doubling, at most 60 s
  function delay() {
    if (failures === 0) { return base; }
    return Math.min(Math.max(base, retryMs) * Math.pow(2, failures - 1), 60000);
  }
  function stale(on) {
    var b = byId('stale');
    if (!b) { return; }
    if (!on) {
      b.textContent = '';
      b.hidden = true;
      if (b.setAttribute) { b.setAttribute('hidden', ''); }
      return;
    }
    b.textContent = 'Showing the data from ' + new Date(lastOk).toISOString().slice(11, 19) + 'Z; the last refresh failed';
    show(b);
  }
  function schedule(ms) {
    if (timer !== null) { clearTimeout(timer); timer = null; }
    if (doc.hidden || !url) { return; }
    timer = setTimeout(poll, ms);
  }
  function fail(r) {
    failures++;
    var ra = r && r.headers ? parseInt(r.headers.get('Retry-After'), 10) : NaN;
    retryMs = isNaN(ra) || ra < 0 ? 0 : Math.min(ra, 3600) * 1000;
    stale(true);
    schedule(delay());
  }
  function ok() {
    failures = 0;
    retryMs = 0;
    lastOk = Date.now();
    stale(false);
    schedule(base);
  }
  function poll() {
    timer = null;
    if (doc.hidden || ctl || !url) { return; }
    var mine = new AbortController();
    ctl = mine;
    function done() { if (ctl === mine) { ctl = null; } }
    var headers = { 'Accept': 'application/json' };
    if (etag) { headers['If-None-Match'] = etag; }
    fetch(url, { method: 'GET', headers: headers, credentials: 'same-origin', cache: 'no-store', signal: mine.signal }).then(function (r) {
      if (r.status === 304) { done(); ok(); return null; }
      if (!r.ok) { done(); fail(r); return null; }
      fresh = r.headers.get('ETag');
      return r.json().then(function (m) { done(); return m; });
    }).then(function (m) {
      if (!m) { return; }
      if (kind === 'list') {
        var live = byId('live');
        if (live) { renderList(m, live); setStatus(m); }
      } else { renderDetail(m); }
      etag = fresh; // only after a successful render
      ok();
    }).catch(function (e) {
      done();
      if (e && e.name === 'AbortError') { return; } // hidden: not a failure
      fail(null);
    });
  }
  doc.addEventListener('visibilitychange', function () {
    if (doc.hidden) {
      if (timer !== null) { clearTimeout(timer); timer = null; }
      if (tickTimer !== null) { clearTimeout(tickTimer); tickTimer = null; }
      if (ctl) { ctl.abort(); ctl = null; }
      return;
    }
    refreshTimes();
    if (tickTimer === null) { tickTimer = setTimeout(tick, 30000); }
    if (timer !== null) { clearTimeout(timer); timer = null; }
    poll();
  });
  // pagehide stops timers and aborts; a restored page refreshes once
  window.addEventListener('pagehide', function () {
    if (timer !== null) { clearTimeout(timer); timer = null; }
    if (tickTimer !== null) { clearTimeout(tickTimer); tickTimer = null; }
    if (ctl) { ctl.abort(); ctl = null; }
  });
  window.addEventListener('pageshow', function (e) {
    if (!e || !e.persisted || doc.hidden) { return; }
    refreshTimes();
    if (tickTimer === null) { tickTimer = setTimeout(tick, 30000); }
    poll();
  });
  if (!doc.hidden) {
    tickTimer = setTimeout(tick, 30000);
    schedule(base);
  }

  // JS-1: copy button
  var copyBtn = byId('copy');
  if (copyBtn && window.navigator && window.navigator.clipboard && window.navigator.clipboard.writeText) {
    show(copyBtn);
    copyBtn.addEventListener('click', function () {
      window.navigator.clipboard.writeText(copyBtn.getAttribute('data-copy') || '').then(
        function () { copyBtn.textContent = 'Copied'; },
        function () { copyBtn.textContent = 'Copy failed'; });
      setTimeout(function () { copyBtn.textContent = 'Copy ID'; }, 2000);
    });
  }
})();
