// Cirak dashboard core script (ADR-023 A1.6, D34). Every page loads it; the per-page scripts (dashboard-list.js,
// dashboard-detail.js, dashboard-audit.js) load after it and use window.cirakDash. GET only; server values go in
// through textContent or setAttribute; no markup is built from data; nothing is evaluated. Every page works without it.
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

  // JS-4 (shared by the sessions list and the audit feed): the URL carries the filter state
  var archived = /(^|[?&])archived=1(&|$)/.test(window.location.search);
  var query = {};
  window.location.search.replace(/^\?/, '').split('&').forEach(function (pair) {
    var i = pair.indexOf('=');
    if (i < 1) { return; }
    try { query[decodeURIComponent(pair.slice(0, i))] = decodeURIComponent(pair.slice(i + 1).replace(/\+/g, ' ')); } catch (e) { /* a malformed escape is ignored */ }
  });
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
  // A2.8: render the server's liveness badges (label/tone/title) and the optional short detail. Values are
  // textContent/setAttribute only; an unknown word never carries a title.
  function renderLive(node, l) {
    clear(node);
    if (!l) { node.appendChild(el('span', 'No report yet', 'label')); return; }
    (l.badges || []).forEach(function (b) {
      var c = el('span', b.label, chipClass(b.tone));
      if (b.title) { c.setAttribute('title', b.title); }
      node.appendChild(c);
      node.appendChild(doc.createTextNode(' '));
    });
    if (l.text) { node.appendChild(el('span', l.text, 'label')); }
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

  // JS-3, A1.7: poll, pause while hidden, back off. A page script starts it with cirakDash.poll(config).
  var cfg = null, base = 0, url = null;
  var etag = null, fresh = null, timer = null, ctl = null, failures = 0, retryMs = 0, lastOk = Date.now();

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
      cfg.render(m);
      etag = fresh; // only after a successful render
      ok();
    }).catch(function (e) {
      done();
      if (e && e.name === 'AbortError') { return; } // hidden: not a failure
      fail(null);
    });
  }
  function startPoll(c) {
    cfg = c;
    base = c.base;
    url = c.url;
    var st = byId('status'), t = st ? st.querySelector('time') : null;
    if (t && t.getAttribute('datetime') && !isNaN(Date.parse(t.getAttribute('datetime')))) {
      lastOk = Date.parse(t.getAttribute('datetime'));
    }
    schedule(base);
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
  if (!doc.hidden) { tickTimer = setTimeout(tick, 30000); }

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

  window.cirakDash = Object.freeze({
    app: app, byId: byId, el: el, clear: clear, show: show, rel: rel, chipClass: chipClass,
    archived: archived, query: query, allowed: allowed, filterRows: filterRows, writeUrl: writeUrl, poll: startPoll,
    renderLive: renderLive
  });
})();
