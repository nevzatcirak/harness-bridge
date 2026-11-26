// Cirak dashboard audit-filter script (ADR-023 A1.6, D34). Loaded after dashboard.js on the audit page only.
// JS-4: a client-side filter over the rows already on the page; the state is in the URL. No request is made.
(function () {
  'use strict';
  var D = window.cirakDash;
  if (!D) { return; }
  var auditFilter = D.byId('audit-filter'), auditRows = D.byId('audit-rows');
  if (!auditFilter || !auditRows) { return; }
  auditFilter.value = (D.query.q || '').slice(0, 64);
  D.show(D.byId('filters'));
  function applyAudit() {
    var q = auditFilter.value.slice(0, 64);
    D.filterRows(auditRows.children, q.toLowerCase(), '', '');
    D.writeUrl(q, '', '');
  }
  auditFilter.addEventListener('input', applyAudit);
  applyAudit();
})();
