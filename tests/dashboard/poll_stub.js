// Exercises internal/dashboard/assets/dashboard.js against a minimal stub DOM under
// node (no browser). Run: node tests/dashboard/poll_stub.js
// It is a manual check, not part of `go test`: the real-browser run stays UNVERIFIED.
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

const js = fs.readFileSync(path.join(__dirname, '..', '..', 'internal', 'dashboard', 'assets', 'dashboard.js'), 'utf8');

class El {
  constructor(tag) { this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this._t = ''; this.className = ''; this.hidden = false; this.listeners = {}; this.parent = null; this.value = ''; }
  get firstChild() { return this.children[0] || null; }
  appendChild(c) { this.children.push(c); c.parent = this; return c; }
  removeChild(c) { this.children = this.children.filter((x) => x !== c); return c; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  hasAttribute(k) { return k in this.attrs; }
  addEventListener(n, f) { (this.listeners[n] = this.listeners[n] || []).push(f); }
  set textContent(v) { this._t = String(v); this.children = []; }
  get textContent() { return this._t + this.children.map((c) => c.textContent).join(''); }
  all(pred, out = []) { this.children.forEach((c) => { if (pred(c)) out.push(c); c.all(pred, out); }); return out; }
  querySelector(sel) {
    const m = /^dd\[data-f="(\w+)"\]$/.exec(sel);
    assert(m, 'unsupported selector ' + sel);
    return this.all((c) => c.tagName === 'DD' && c.attrs['data-f'] === m[1])[0] || null;
  }
}

function harness(app, extra, opts) {
  opts = opts || {};
  const byId = Object.assign({ app }, extra || {});
  const timers = [];
  const fetches = [];
  const reloads = [];
  const doc = {
    hidden: false,
    getElementById: (id) => byId[id] || null,
    createElement: (t) => new El(t),
    addEventListener: () => {},
  };
  const responses = opts.responses || [];
  const sandbox = {
    document: doc,
    window: { location: { reload: () => reloads.push(1) } },
    setTimeout: (f, ms) => { timers.push({ f, ms }); return timers.length; },
    clearTimeout: () => {},
    encodeURIComponent,
    fetch: (url, init) => {
      fetches.push({ url, init });
      const r = responses.shift();
      if (r instanceof Error) { return Promise.reject(r); }
      return Promise.resolve({ status: r.status, ok: r.status >= 200 && r.status < 300, headers: { get: (k) => (k === 'ETag' ? r.etag || null : null) }, json: () => Promise.resolve(r.body) });
    },
    console,
  };
  vm.runInNewContext(js, sandbox);
  const tick = async () => { const t = timers.pop(); assert(t, 'no timer armed'); t.f(); for (let i = 0; i < 12; i++) { await Promise.resolve(); } return t.ms; };
  return { timers, fetches, reloads, doc, tick };
}

function app(attrs) { const a = new El('main'); Object.keys(attrs).forEach((k) => a.setAttribute(k, attrs[k])); return a; }

(async () => {
  // 1. list page
  {
    const live = new El('div');
    const hostile = '<img src=x onerror=alert(1)>';
    const h = harness(app({ 'data-poll': 'list' }), { live }, { responses: [
      { status: 200, etag: '"e1"', body: { kind: 'list', message: '2 sessions.', sessions: [
        { harness: 'opencode', id: 'ses_a', title: hostile, root: '/r', state: 'running', guard: { held: true, state: 'running' }, current_execution: 'exec_1', last_activity: 'now', href: '/session/opencode/ses_a' },
        { harness: 'evil"><b>', id: 'x', title: 't', root: '/', state: 'idle', guard: { held: false }, last_activity: '-', href: 'javascript:alert(1)' } ] } },
      { status: 304 },
      new Error('network down'),
      { status: 503 },
    ] });
    assert.strictEqual(h.timers.length, 1);
    assert.strictEqual(h.timers[0].ms, 5000, 'the list polls every 5 s');
    let ms = await h.tick();
    assert.strictEqual(ms, 5000);
    assert.strictEqual(h.fetches[0].url, '/api/state');
    assert.strictEqual(h.fetches[0].init.method, 'GET');
    assert(!('If-None-Match' in h.fetches[0].init.headers), 'no validator on the first poll');
    const tds = live.all((c) => c.tagName === 'TD');
    assert(tds.some((c) => c.textContent === hostile), 'the hostile title is shown as text');
    const tags = new Set(live.all(() => true).map((c) => c.tagName));
    ['IMG', 'B', 'SCRIPT'].forEach((t) => assert(!tags.has(t), 'markup was built from data: ' + t));
    const links = live.all((c) => c.tagName === 'A');
    assert.strictEqual(links.length, 1, 'only the /session/ link is a link');
    assert.strictEqual(links[0].attrs.href, '/session/opencode/ses_a');
    assert(live.all((c) => c.className === 'badge harness-other').length === 1, 'an unknown harness gets the neutral badge class');
    await h.tick();
    assert.strictEqual(h.fetches[1].init.headers['If-None-Match'], '"e1"', 'the second poll sends the ETag');
    const before = live.children.length;
    await h.tick();            // network error: nothing changes, the loop continues
    await h.tick();            // 503: nothing changes
    assert.strictEqual(live.children.length, before);
    assert.strictEqual(h.fetches.length, 4);
    assert.strictEqual(h.timers.length, 1, 'the loop keeps one timer armed after failures');
  }
  // 2. detail page
  {
    const tbody = new El('tbody');
    const a = app({ 'data-poll': 'detail', 'data-harness': 'opencode', 'data-session': 'ses_x/../y' });
    ['title', 'root', 'state', 'guard', 'current', 'last'].forEach((k) => { const dd = new El('dd'); dd.setAttribute('data-f', k); a.appendChild(dd); });
    const h = harness(a, { 'exec-rows': tbody }, { responses: [
      { status: 200, etag: '"d1"', body: { kind: 'detail', session: { title: 'T', root: '/r', state: 'running', guard: { held: true, state: 'running' }, current_execution: 'exec_2', last_activity: 'now' },
        executions: [{ id: 'exec_1', state: 'completed', started: 's', updated: 'u', ended: 'e', current: false }, { id: 'exec_2', state: 'running', started: 's', updated: 'u', ended: '-', current: true }] } },
      { status: 200, etag: '"d2"', body: { kind: 'detail', session: { title: 'T', root: '/r', state: 'idle', guard: { held: false }, last_activity: 'later' }, executions: [] } },
    ] });
    assert.strictEqual(h.timers[0].ms, 2000, 'the detail polls every 2 s while visible');
    await h.tick();
    assert.strictEqual(h.fetches[0].url, '/api/state/session/opencode/ses_x%2F..%2Fy', 'the ID is encoded into one path segment');
    assert.strictEqual(a.querySelector('dd[data-f="state"]').textContent, 'running');
    assert.strictEqual(a.querySelector('dd[data-f="guard"]').textContent, 'held (running)');
    assert.strictEqual(tbody.children.length, 2);
    assert(tbody.children[1].textContent.indexOf('exec_2 (current)') === 0);
    h.doc.hidden = true;
    await h.tick();
    assert.strictEqual(h.timers[0].ms, 10000, 'a hidden tab polls every 10 s');
    assert.strictEqual(a.querySelector('dd[data-f="state"]').textContent, 'idle');
    assert.strictEqual(a.querySelector('dd[data-f="current"]').textContent, '-');
    assert.strictEqual(tbody.children.length, 0);
  }
  // 3. detail rendered without executions: a table appears only on a reload
  {
    const a = app({ 'data-poll': 'detail', 'data-harness': 'opencode', 'data-session': 'ses_x' });
    const h = harness(a, {}, { responses: [{ status: 200, etag: '"d"', body: { kind: 'detail', session: {}, executions: [{ id: 'e', state: 'running', started: '', updated: '', ended: '-', current: true }] } }] });
    await h.tick();
    assert.strictEqual(h.reloads.length, 1);
  }
  // 4. pages that do not poll start nothing; a missing ID starts nothing
  {
    assert.strictEqual(harness(app({}), {}).timers.length, 0);
    assert.strictEqual(harness(app({ 'data-poll': 'detail', 'data-harness': 'opencode' }), {}).timers.length, 0);
    assert.strictEqual(harness(app({ 'data-poll': 'nope' }), {}).timers.length, 0);
  }
  // 5. audit filter (browser side only, no request)
  {
    const input = new El('input');
    const rows = new El('tbody');
    ['interrupt accepted exec_1', 'guard_release refused', 'prompt_submission new'].forEach((t) => { const tr = new El('tr'); tr.textContent = t; rows.appendChild(tr); });
    const h = harness(app({}), { 'audit-filter': input, 'audit-rows': rows });
    input.value = 'GUARD';
    input.listeners.input.forEach((f) => f());
    assert.deepStrictEqual(rows.children.map((r) => r.hidden), [true, false, true]);
    input.value = '';
    input.listeners.input.forEach((f) => f());
    assert.deepStrictEqual(rows.children.map((r) => r.hidden), [false, false, false]);
    assert.strictEqual(h.fetches.length, 0);
  }
  console.log('poll_stub: all checks passed');
})().catch((e) => { console.error(e); process.exit(1); });
