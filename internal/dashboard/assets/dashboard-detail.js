// Cirak dashboard session-detail script (ADR-023 A1.6, D34). Loaded after dashboard.js on the session page only.
// GET only; server values go in through textContent or setAttribute; no markup is built from data.
(function () {
  'use strict';
  var D = window.cirakDash;
  if (!D) { return; }
  var doc = document, app = D.app, el = D.el, clear = D.clear, byId = D.byId, chipClass = D.chipClass;

  // parts: as the server renders them
  function addParts(node, ps) {
    (ps || []).forEach(function (p) {
      if (p.kind === 'chip') { node.appendChild(el('span', p.text, chipClass(p.tone))); }
      else if (p.kind === 'code') { node.appendChild(el('code', p.text)); }
      else if (p.kind === 'time') {
        var t = el('time', D.rel(p.iso));
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
    (m.executions || []).forEach(function (x) {
      var cells = app.querySelectorAll('td[data-live]');
      for (var i = 0; i < cells.length; i++) {
        if (cells[i].getAttribute('data-live') !== x.id) { continue; }
        var state = x.state || '';
        if (state === 'completed' || state === 'failed' || state === 'interrupted') {
          clear(cells[i]); cells[i].appendChild(el('span', 'Ended', 'label'));
        } else {
          D.renderLive(cells[i], x.liveness);
        }
        break;
      }
    });
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

  // JS-3: poll the JSON model of the session every 2 s
  if (app.getAttribute('data-poll') === 'detail') {
    var h = app.getAttribute('data-harness'), id = app.getAttribute('data-session'), url = null;
    if (h && id) { url = '/api/state/session/' + encodeURIComponent(h) + '/' + encodeURIComponent(id) + (D.archived ? '?archived=1' : ''); }
    D.poll({ base: 2000, url: url, render: renderDetail });
  }
})();
