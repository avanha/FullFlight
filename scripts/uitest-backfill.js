// Behaviour of the backfill screen (static/backfill.html), run under node against a fake DOM and a model of the backfill API.
// Run: node scripts/uitest-backfill.js
const fs = require('fs'), vm = require('vm'), assert = require('assert'), path = require('path');
const html = fs.readFileSync(path.join(__dirname, '../internal/server/static/backfill.html'), 'utf8');
const src = html.match(/<script>\n(\/\/ ---- the backfill page[\s\S]*?)<\/script>/)[1];

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; this.className = ''; this.hidden = false; this.disabled = false;
    this.value = ''; this.checked = false; this.max = 1; this.title = ''; }
  append(...n) { this.children.push(...n); }
  replaceChildren(...n) { this.children = n; }
}
const all = (el, out = []) => { for (const c of el.children || []) { out.push(c); all(c, out); } return out; };
const rowsOf = world => world.byId['#rows'].children.map(tr => ({tr, cells: tr.children, box: tr.children[0].children[0], text: tr.children.slice(1).map(td => td.textContent)}));

const GiB = 2 ** 30;
function newWorld(search, setup) {
  const w = {byId: {}, calls: [], timers: []};
  w.document = {querySelector: s => w.byId[s] ||= new El(s), createElement: t => new El(t)};
  w.enabled = true;
  w.watches = [{hex: '780AB8', tail: 'B-LRM', ident: 'B-LRM'}, {hex: '7C4926', tail: 'VH-OQG', ident: 'VH-OQG'}, {hex: '780AB8', tail: 'B-LRM', ident: 'B-LRM'}];
  w.days = [{day: '2026-10-31', aircraft: 2, cached: true, bytes: 0}, {day: '2026-10-30', aircraft: 2, cached: false, bytes: 3.5e9}, {day: '2026-10-29', aircraft: 1, cached: false, bytes: 3.5e9},
    {day: '2026-10-28', aircraft: 21, cached: true, bytes: 0, done: true}, {day: '2026-10-27', aircraft: 1, cached: false, bytes: 0, done: true}];
  w.cacheBytes = 3.3e9; w.limit = 10e9;
  w.status = {running: false, days_done: 0, days_total: 0, bytes_done: 0, bytes_total: 0, positions_added: 0};
  w.fetch = async (url, opts = {}) => {
    const method = opts.method || 'GET';
    w.calls.push([method, url, opts.body ? JSON.parse(opts.body) : undefined]);
    const reply = (status, body) => ({status, ok: status < 400, statusText: 'x', json: async () => JSON.parse(JSON.stringify(body))});
    if (url === '/api/watches') return reply(200, w.watches);
    if (url === '/api/backfill' && method === 'GET') return reply(200, {enabled: w.enabled, cache_dir: '/cache', cache_bytes: w.cacheBytes, cache_limit_bytes: w.limit, status: w.status});
    if (url === '/api/backfill' && method === 'POST') { w.status = {...w.status, running: true, days_total: JSON.parse(opts.body).days.length, phase: 'downloading', day: JSON.parse(opts.body).days[0]}; return reply(202, w.status); }
    if (url === '/api/backfill' && method === 'DELETE') return reply(202, w.status);
    const m = url.match(/^\/api\/backfill\/plan\?hex=([^&]*)(?:&days=(.*))?$/);
    if (m) {
      const only = m[2] === undefined ? null : m[2].split(',').filter(Boolean);
      const days = w.days.map(d => ({...d, selected: !d.done && (only === null || only.includes(d.day))}));
      const sel = days.filter(d => d.selected), dl = sel.reduce((n, d) => n + d.bytes, 0);
      const tooBig = w.cacheBytes + dl > w.limit && sel.length > 0 && !w.evictable;
      return reply(200, {days, aircraft: sel.length ? 2 : 0, download_bytes: dl, cache_bytes: w.cacheBytes, cache_limit_bytes: w.limit,
        evict: w.cacheBytes + dl > w.limit && w.evictable ? ['2026-10-01'] : undefined, too_big: tooBig || undefined});
    }
    return reply(404, {error: 'not found'});
  };
  if (setup) setup(w);
  const ctx = {document: w.document, fetch: w.fetch, URLSearchParams, Date, Math, Set, JSON, Object, console, String,
    setTimeout: f => { w.timers.push(f); return w.timers.length; }, clearTimeout: () => {}, location: {search}};
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  w.ctx = ctx;
  w.settle = () => new Promise(r => setImmediate(r)).then(() => new Promise(r => setImmediate(r))).then(() => new Promise(r => setImmediate(r)));
  return w;
}

(async () => {
  // opened from the map with an aircraft selected: it is chosen, listed once, and "all" is there too
  let w = newWorld('?hex=780AB8'); await w.settle();
  const sc = w.byId['#scope'];
  assert.deepStrictEqual(sc.children.map(o => [o.value, o.textContent]), [['780AB8', 'B-LRM (780AB8)'], ['7C4926', 'VH-OQG (7C4926)'], ['', 'All tracked aircraft']]);
  assert.strictEqual(sc.value, '780AB8');
  assert.ok(w.calls.some(([m, u]) => u === '/api/backfill/plan?hex=780AB8'), JSON.stringify(w.calls.map(c => c[1])));
  assert.strictEqual(w.byId['#off'].hidden, true); assert.strictEqual(w.byId['#body'].hidden, false);
  assert.match(w.byId['#cache'].textContent, /Cache \/cache: 3\.3 GB of 10\.0 GB/);

  // every day is listed, with what it would cost, all ticked to start with
  let rows = rowsOf(w);
  assert.deepStrictEqual(rows.map(r => r.text), [['2026-10-31', '2', 'in the cache', '—', ''], ['2026-10-30', '2', 'to download', '≈3.5 GB', ''], ['2026-10-29', '1', 'to download', '≈3.5 GB', ''],
    ['2026-10-28', '21', 'in the cache', '—', 'searched for all 21 aircraft'], ['2026-10-27', '1', 'not in the cache', '—', 'searched for its aircraft']]);
  // a day already read is listed, greyed, and cannot be ticked, so the cache's contents can be seen
  assert.deepStrictEqual(rows.map(r => [r.box.checked, r.box.disabled, r.tr.className]), [[true, false, ''], [true, false, ''], [true, false, ''], [false, true, 'before'], [false, true, 'before']]);
  assert.strictEqual(w.byId['#tickall'].checked, true);
  assert.match(w.byId['#summary'].textContent, /^3 days chosen, for 2 aircraft: 7\.0 GB to download\. Cache 3\.3 GB of 10\.0 GB\. That does not fit in the cache limit\.$/, w.byId['#summary'].textContent);
  assert.strictEqual(w.byId['#start'].disabled, true, 'too big for the cache with nothing to give up');
  assert.strictEqual(w.byId['#summary'].className, 'bad');

  // untick the oldest: the totals follow, and the server is asked about exactly the chosen days
  w.calls.length = 0;
  rows[2].box.checked = false; await rows[2].box.onchange(); await w.settle();
  assert.ok(w.calls.some(([, u]) => u === '/api/backfill/plan?hex=780AB8&days=2026-10-31,2026-10-30'), JSON.stringify(w.calls.map(c => c[1])));
  assert.match(w.byId['#summary'].textContent, /^2 days chosen, for 2 aircraft: 3\.5 GB to download\./);
  assert.strictEqual(w.byId['#tickall'].checked, false);
  assert.strictEqual(w.byId['#start'].disabled, false);
  assert.strictEqual(rowsOf(w)[2].tr.className, 'off', 'the day left out is greyed');

  // none, cached only, all
  await w.byId['#selnone'].onclick(); await w.settle();
  assert.strictEqual(w.byId['#summary'].textContent, 'No days chosen.'); assert.strictEqual(w.byId['#start'].disabled, true);
  await w.byId['#selcached'].onclick(); await w.settle();
  assert.deepStrictEqual(rowsOf(w).map(r => r.box.checked), [true, false, false, false, false]);
  assert.match(w.byId['#summary'].textContent, /^1 day chosen, for 2 aircraft: 0\.0 GB to download/);
  await w.byId['#selall'].onclick(); await w.settle();
  assert.ok(rowsOf(w).slice(0, 3).every(r => r.box.checked), 'all the days left to read are ticked again');

  // a cache that would have to give up an old day says so, and may still start
  w.evictable = true; await w.byId['#selall'].onclick(); await w.settle();
  assert.match(w.byId['#summary'].textContent, /To make room it would delete the oldest cached day: 2026-10-01\./);
  assert.strictEqual(w.byId['#start'].disabled, false);

  // start with two of the three: the request carries the days chosen, in the scope chosen
  rows = rowsOf(w); rows[1].box.checked = false; await rows[1].box.onchange(); await w.settle();
  w.calls.length = 0;
  await w.byId['#start'].onclick(); await w.settle();
  const post = w.calls.find(([m]) => m === 'POST');
  assert.deepStrictEqual(post && post[2], {hex: '780AB8', days: ['2026-10-31', '2026-10-29']});
  assert.strictEqual(w.byId['#start'].hidden, true); assert.strictEqual(w.byId['#stop'].hidden, false); assert.strictEqual(sc.disabled, true, 'the aircraft cannot change mid-run');
  assert.ok(rowsOf(w).every(r => r.box.disabled), 'nor the days');
  assert.ok(w.timers.length, 'and it looks again in a couple of seconds');

  // downloading: progress on the row it is on, the day already read shows its yield
  w.status = {running: true, phase: 'downloading', day: '2026-10-29', days_done: 1, days_total: 2, bytes_done: 1.75e9, bytes_total: 3.5e9, positions_added: 812,
    days_read: ['2026-10-31'], days_read_added: [812], lines: ['downloading 2026-10-29 (3.3 GiB)']};
  await w.timers.pop()(); await w.settle();
  rows = rowsOf(w);
  assert.strictEqual(rows[0].text[4], '812 positions added'); assert.strictEqual(rows[0].tr.className, 'done');
  assert.strictEqual(rows[2].text[4], 'downloading 50%'); assert.strictEqual(rows[2].tr.className, 'busy');
  assert.strictEqual(w.byId['#bar'].hidden, false); assert.strictEqual(w.byId['#bar'].value, 1.75e9);
  assert.match(w.byId['#status'].textContent, /^downloading 2026-10-29 · day 2 of 2 · 1\.8 GB of 3\.5 GB · 812 positions added so far$/);
  assert.match(w.byId['#lines'].textContent, /downloading 2026-10-29/);

  // finished: what was read is no longer to do, and the screen says what it came to
  w.days = w.days.filter(d => d.day === '2026-10-30'); w.evictable = false;
  w.status = {running: false, days_done: 2, days_total: 2, positions_added: 900, finished: new Date().toISOString(), days_read: ['2026-10-31', '2026-10-29'], days_read_added: [812, 88]};
  w.calls.length = 0;
  await w.timers.pop()(); await w.settle();
  assert.strictEqual(w.byId['#start'].hidden, false); assert.strictEqual(w.byId['#stop'].hidden, true);
  assert.match(w.byId['#status'].textContent, /^Last run .*: 900 positions added over 2 of 2 days\.$/);
  assert.ok(w.calls.some(([, u]) => u.startsWith('/api/backfill/plan')), 'the plan is read again');
  assert.deepStrictEqual(rowsOf(w).map(r => r.text[0]), ['2026-10-30']);

  // a run that stopped on an error says why; Stop asks the server
  w.status = {...w.status, running: true, error: ''}; w.timers.length = 0;
  await w.byId['#start'].onclick(); await w.settle(); w.calls.length = 0;
  await w.byId['#stop'].onclick(); await w.settle();
  assert.ok(w.calls.some(([m]) => m === 'DELETE'));
  w.status = {running: false, days_done: 0, days_total: 1, positions_added: 0, error: '2026-10-30 has not been published by adsb.lol', finished: new Date().toISOString()};
  await w.timers.pop()(); await w.settle();
  assert.match(w.byId['#err'].textContent, /has not been published/);

  // a different aircraft: the plan is read for it and everything is ticked again
  w.calls.length = 0; w.days = [{day: '2026-10-28', aircraft: 1, cached: false, bytes: 3.5e9}];
  sc.value = '7C4926'; await sc.onchange(); await w.settle();
  assert.ok(w.calls.some(([, u]) => u === '/api/backfill/plan?hex=7C4926'));
  assert.deepStrictEqual(rowsOf(w).map(r => [r.text[0], r.box.checked]), [['2026-10-28', true]]);

  // opened with no aircraft: all of them
  w = newWorld(''); await w.settle();
  assert.strictEqual(w.byId['#scope'].value, '');
  assert.ok(w.calls.some(([, u]) => u === '/api/backfill/plan?hex='));

  // nothing to read
  w = newWorld('', x => { x.days = []; });
  await w.settle(); assert.strictEqual(w.byId['#empty'].hidden, false); assert.strictEqual(w.byId['#start'].disabled, true);

  // backfill off in the config: the screen says how to turn it on
  w = newWorld('', x => { x.enabled = false; });
  await w.settle(); assert.strictEqual(w.byId['#off'].hidden, false); assert.strictEqual(w.byId['#body'].hidden, true);
  console.log('backfill page checks passed');
})().catch(e => { console.error('FAILED:', e.message); if (process.env.UITEST_STACK) console.error(e.stack.split('\n').slice(1, 4).join('\n')); process.exit(1); });
