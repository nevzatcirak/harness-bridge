// Cirak dashboard sessions-list script (ADR-023 A1.6, D34). Loaded after dashboard.js on the sessions page only.
// GET only; server values go in through textContent or setAttribute; no markup is built from data.
(function () {
  'use strict';
  var D = window.cirakDash;
  if (!D) { return; }
  var doc = document, el = D.el, clear = D.clear, byId = D.byId;
  function safeHref(href) { return typeof href === 'string' && href.indexOf('/session/') === 0 ? href : ''; }

  // JS-4: filters, state in the URL
  var query = D.query, archived = D.archived;
  var fq = byId('f-q'), fstate = byId('f-state'), fharness = byId('f-harness');
  function applySessionFilter() {
    var live = byId('live');
    var body = live ? live.querySelector('tbody') : null;
    var q = fq.value.slice(0, 64), state = fstate.value, harness = fharness.value;
    if (body) { D.filterRows(body.children, q.toLowerCase(), state, harness); }
    D.writeUrl(q, state, harness);
  }
  if (fq && fstate && fharness) {
    fq.value = (query.q || '').slice(0, 64);
    fstate.value = D.allowed(fstate, query.state || '');
    fharness.value = D.allowed(fharness, query.harness || '');
    D.show(byId('filters'));
    [fq, fstate, fharness].forEach(function (c) { c.addEventListener('input', applySessionFilter); c.addEventListener('change', applySessionFilter); });
    applySessionFilter();
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
    var chip = el('span', s.state_label, D.chipClass(s.state_tone));
    chip.setAttribute('title', s.state);
    cell(tr).appendChild(chip);
    cell(tr, s.guard_text);
    var cur = cell(tr);
    if (s.current_execution) { cur.appendChild(el('code', s.current_execution)); } else { cur.textContent = 'None'; }
    var last = cell(tr);
    if (s.last_activity_iso) {
      var t = el('time', D.rel(s.last_activity_iso));
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
    var t = el('time', D.rel(iso));
    t.setAttribute('datetime', iso);
    t.setAttribute('title', iso.slice(0, 10) + ' ' + iso.slice(11, 19) + 'Z');
    status.appendChild(t);
  }

  // JS-3: poll the JSON model of the list every 5 s
  if (D.app.getAttribute('data-poll') === 'list') {
    D.poll({
      base: 5000,
      url: '/api/state' + (archived ? '?archived=1' : ''),
      render: function (m) {
        var live = byId('live');
        if (live) { renderList(m, live); setStatus(m); }
      }
    });
  }
})();
