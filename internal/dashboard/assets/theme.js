// Cirak theme (ADR-023 A1.6 JS-2, D34): blocking, in the head; sets data-theme from storage before first paint.
(function () {
  'use strict';
  var K = 'cirak-theme', d = document, r = d.documentElement, v = null;
  try {
    v = window.localStorage.getItem(K);
    if (v !== 'dark' && v !== 'light') { if (v !== null) { window.localStorage.removeItem(K); } v = null; }
  } catch (e) { v = null; }
  if (v) { r.setAttribute('data-theme', v); }
  d.addEventListener('DOMContentLoaded', function () {
    var b = d.getElementById('theme');
    if (!b) { return; }
    b.hidden = false; b.removeAttribute('hidden');
    b.addEventListener('click', function () {
      var c = r.getAttribute('data-theme') || (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'), n = c === 'light' ? 'dark' : 'light';
      r.setAttribute('data-theme', n);
      try { window.localStorage.setItem(K, n); } catch (e) { /* not kept */ }
    });
  });
})();
