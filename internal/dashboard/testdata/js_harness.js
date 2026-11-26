'use strict';
// Test harness for the shipped dashboard scripts (ADR-023 A1.10 items 9 and 12, D34). It runs them, in the order given, in a
// vm context with a minimal fake DOM, a fake clock, a scripted fetch and a recording history, then prints
// one JSON document with what the scripts did. Usage: node js_harness.js <comma-separated script paths> <scenario.json>
// It is test code: it is never served and never embedded.
const fs = require('fs');
const vm = require('vm');

const [, , scriptPaths, scenarioPath] = process.argv;
const scenario = JSON.parse(fs.readFileSync(scenarioPath, 'utf8'));
const log = { fetches: [], replaceState: [], timers: [], aborted: 0, notes: [] };
const clock = { now: Date.parse(scenario.now || '2026-11-21T17:48:05Z'), timers: [], seq: 0 };

class El {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this.parent = null; this._text = ''; this.listeners = {}; this.hidden = false; this.value = ''; this.open = false; }
  get className() { return this.attrs['class'] || ''; }
  set className(v) { if (v === '' || v === undefined || v === null) { delete this.attrs['class']; } else { this.attrs['class'] = String(v); } }
  get firstChild() { return this.children[0] || null; }
  get textContent() { return this.children.length ? this.children.map(function (c) { return c.textContent; }).join('') : this._text; }
  set textContent(v) { this.children = []; this._text = String(v); }
  appendChild(c) { c.parent = this; this.children.push(c); return c; }
  removeChild(c) { this.children = this.children.filter(function (x) { return x !== c; }); return c; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; }
  hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); }
  fire(t, ev) { (this.listeners[t] || []).forEach(function (f) { f(ev || {}); }); }
  get options() { return this.children.filter(function (c) { return c.tagName === 'OPTION'; }); }
  walk(f) { f(this); this.children.forEach(function (c) { if (c.walk) { c.walk(f); } }); }
  querySelectorAll(sel) { const out = []; const self = this; this.walk(function (e) { if (e !== self && match(e, sel)) { out.push(e); } }); return out; }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
class Txt { constructor(t) { this.tagName = '#text'; this._t = String(t); this.parent = null; } get textContent() { return this._t; } }
function match(e, sel) {
  let m = /^([a-z]*)(?:\[([a-z-]+)(?:="([^"]*)")?\])?$/.exec(sel);
  if (!m) { return false; }
  if (m[1] && e.tagName !== m[1].toUpperCase()) { return false; }
  if (m[2]) { const v = e.getAttribute(m[2]); if (v === null || (m[3] !== undefined && v !== m[3])) { return false; } }
  return true;
}
function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }
function serialize(e) {
  if (e.tagName === '#text') { return esc(e.textContent); }
  const tag = e.tagName.toLowerCase();
  const a = Object.keys(e.attrs).map(function (k) { return ' ' + k + '="' + esc(e.attrs[k]) + '"'; }).join('');
  if (tag === 'br') { return '<br>'; }
  const inner = e.children.length ? e.children.map(serialize).join('') : esc(e._text);
  return '<' + tag + a + '>' + inner + '</' + tag + '>';
}

const ids = {};
const doc = new El('document');
doc.createElement = function (t) { return new El(t); };
doc.createTextNode = function (t) { return new Txt(t); };
doc.getElementById = function (id) { return ids[id] || null; };
doc.hidden = false;
function build(spec, parent) {
  const e = new El(spec.tag);
  (spec.attrs ? Object.keys(spec.attrs) : []).forEach(function (k) { e.setAttribute(k, spec.attrs[k]); });
  if (spec.id) { e.setAttribute('id', spec.id); ids[spec.id] = e; }
  if (spec.hidden) { e.hidden = true; e.setAttribute('hidden', ''); }
  if (spec.open) { e.open = true; }
  if (spec.text) { e.textContent = spec.text; }
  if (spec.value !== undefined) { e.value = spec.value; }
  (spec.children || []).forEach(function (c) { build(c, e); });
  if (parent) { parent.appendChild(e); }
  return e;
}
const root = build(scenario.dom, null);

const win = {
  location: { pathname: scenario.pathname || '/', search: scenario.search || '', reload: function () { log.reloads = (log.reloads || 0) + 1; } },
  listeners: {},
  addEventListener: function (t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); },
  fire: function (t, ev) { (this.listeners[t] || []).forEach(function (f) { f(ev || {}); }); },
  // navigator.clipboard is present only when the scenario asks for it; writes are recorded, and may be refused
  navigator: scenario.clipboard ? { clipboard: { writeText: function (t) { (log.copied = log.copied || []).push(t); return scenario.clipboard === 'refuse' ? Promise.reject(new Error('denied')) : Promise.resolve(); } } } : {},
  history: { replaceState: function (s, t, u) { log.replaceState.push(u); } },
  matchMedia: function (q) {
    const mq = { matches: !!scenario.narrow, media: q, listeners: [], addEventListener: function (t, f) { this.listeners.push(f); } };
    win.__mq = mq; return mq;
  },
};
const FakeDate = class extends Date { constructor(...a) { if (a.length === 0) { super(clock.now); } else { super(...a); } } static now() { return clock.now; } };

function fakeSetTimeout(fn, ms) {
  if (typeof fn !== 'function') { log.notes.push('string timer'); return 0; }
  const id = ++clock.seq; clock.timers.push({ id: id, at: clock.now + (ms || 0), fn: fn, ms: ms || 0 }); log.timers.push({ id: id, ms: ms || 0, at: clock.now }); return id;
}
function fakeClearTimeout(id) { clock.timers = clock.timers.filter(function (t) { return t.id !== id; }); }
async function flush() { for (let i = 0; i < 20; i++) { await Promise.resolve(); } }

class FakeAbortController { constructor() { const c = this; this.signal = { aborted: false, listeners: [], addEventListener: function (t, f) { this.listeners.push(f); } }; this.abort = function () { if (!c.signal.aborted) { c.signal.aborted = true; log.aborted++; c.signal.listeners.forEach(function (f) { f(); }); } }; } }

let script = scenario.responses || [];
const held = [];
function toResponse(resp) {
  const h = resp.headers || {};
  return { status: resp.status, ok: resp.status >= 200 && resp.status < 300, headers: { get: function (k) { const key = Object.keys(h).find(function (x) { return x.toLowerCase() === String(k).toLowerCase(); }); return key ? h[key] : null; } }, json: function () { return Promise.resolve(resp.body); } };
}
function fakeFetch(url, opts) {
  opts = opts || {};
  const rec = { url: url, method: opts.method, headers: opts.headers || {}, at: clock.now, signalAborted: false };
  log.fetches.push(rec);
  const resp = script.length ? script.shift() : { status: 200, body: {}, headers: {} };
  return new Promise(function (resolve, reject) {
    if (opts.signal) { opts.signal.addEventListener('abort', function () { rec.signalAborted = true; const e = new Error('aborted'); e.name = 'AbortError'; reject(e); }); }
    if (resp.hang) { return; }
    if (resp.hold) { held.push(resolve); return; } // answered later by a "release" step, whatever the tab state
    if (resp.reject) { reject(new Error('network')); return; }
    resolve(toResponse(resp));
  });
}

const ctx = vm.createContext({
  document: doc, window: win, location: win.location, history: win.history, setTimeout: fakeSetTimeout, clearTimeout: fakeClearTimeout,
  setInterval: function (fn, ms) { log.notes.push('setInterval ' + ms); return fakeSetTimeout(fn, ms); }, clearInterval: fakeClearTimeout,
  fetch: fakeFetch, AbortController: FakeAbortController, Date: FakeDate, Promise: Promise, encodeURIComponent: encodeURIComponent,
  decodeURIComponent: decodeURIComponent, parseInt: parseInt, parseFloat: parseFloat, isNaN: isNaN, Math: Math, Object: Object, Array: Array,
  String: String, Number: Number, RegExp: RegExp, JSON: JSON, Error: Error, URLSearchParams: URLSearchParams, console: { log: function () {} },
});
doc.addEventListener = El.prototype.addEventListener; doc.listeners = {}; doc.fire = El.prototype.fire;
doc.querySelectorAll = function (s) { return root.querySelectorAll(s); };
doc.querySelector = function (s) { return root.querySelector(s); };

async function advance(ms) {
  const end = clock.now + ms;
  for (;;) {
    clock.timers.sort(function (a, b) { return a.at - b.at || a.id - b.id; });
    const next = clock.timers[0];
    if (!next || next.at > end) { break; }
    clock.timers.shift(); clock.now = Math.max(clock.now, next.at); next.fn(); await flush();
  }
  clock.now = end; await flush();
}

(async function () {
  scriptPaths.split(',').forEach(function (p) { vm.runInContext(fs.readFileSync(p, 'utf8'), ctx, { filename: p.split('/').pop() }); });
  await flush();
  const out = { steps: [] };
  for (const step of scenario.steps || []) {
    if (step.advance) { await advance(step.advance); }
    if (step.hidden !== undefined) { doc.hidden = step.hidden; doc.fire('visibilitychange'); await flush(); }
    if (step.pagehide) { win.fire('pagehide', {}); await flush(); }
    if (step.pageshow) { win.fire('pageshow', { persisted: step.pageshow === 'persisted' }); await flush(); }
    if (step.click) { ids[step.click].fire('click'); await flush(); }
    if (step.hiddenSilent !== undefined) { doc.hidden = step.hiddenSilent; } // the flag changes but no event is delivered yet
    if (step.release) { held.shift()(toResponse(step.release)); await flush(); }
    if (step.respond) { script.push.apply(script, step.respond); }
    if (step.mq !== undefined && win.__mq) { win.__mq.matches = step.mq; win.__mq.listeners.forEach(function (f) { f({ matches: step.mq }); }); }
    if (step.input) { const el = ids[step.input.id]; el.value = step.input.value; el.fire('input'); el.fire('change'); }
    const snap = { fetches: log.fetches.length, pending: clock.timers.map(function (t) { return t.at - clock.now; }).sort(function (a, b) { return a - b; }), aborted: log.aborted, now: clock.now };
    (step.read || []).forEach(function (id) { const e = ids[id]; snap[id] = e ? { text: e.textContent, hidden: !!e.hidden, open: e.open, value: e.value, html: serialize(e) } : null; });
    out.steps.push(snap);
  }
  out.log = log;
  out.times = root.querySelectorAll('time').map(function (t) { return { text: t.textContent, datetime: t.getAttribute('datetime') }; });
  out.live = ids.live ? ids.live.children.map(serialize).join('') : '';
  out.statusInner = ids.status ? ids.status.children.map(serialize).join('') : '';
  out.rows = (ids.live ? ids.live.querySelectorAll('tr') : []).map(serialize);
  process.stdout.write(JSON.stringify(out));
})().catch(function (e) { process.stderr.write(String(e && e.stack || e)); process.exit(2); });
