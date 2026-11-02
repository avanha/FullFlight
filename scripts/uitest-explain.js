// Behaviour of the explain screen (static/explain.html), run under node against a fake DOM and a model of the explain API.
// Run: node scripts/uitest-explain.js
const fs = require('fs'), vm = require('vm'), assert = require('assert'), path = require('path');
const html = fs.readFileSync(path.join(__dirname, '../internal/server/static/explain.html'), 'utf8');
const src = html.match(/<script>\n(\/\/ ---- the explain page[\s\S]*?)<\/script>/)[1];

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; this.className = ''; this.hidden = false; this.value = '96'; this.open = false; }
  append(...n) { this.children.push(...n); }
  replaceChildren(...n) { this.children = n; }
  scrollIntoView(o) { this.scrolled = o; }
}
const text = e => e.children.map(c => c.textContent !== undefined && !c.children.length ? c.textContent : text(c)).join('');

const flight = (id, f, extra = {}) => ({id, flight: f, callsign: 'QFA' + f.slice(2), start: '2026-11-01T04:54:05Z', end: '2026-11-01T14:46:41Z', messages: 207, positions: 1243,
  from: 'YSSY', to: 'WSSS', identified: true, ...extra});
const QF1a = flight('QF1@1', 'QF1', {route_source: 'airline report', departed: '2026-11-01T06:24:00Z', departed_how: 'takeoff',
  why: [{stage: 'runs', rule: 'run', from: '2026-11-01T04:54:05Z', to: '2026-11-01T14:46:41Z', detail: '1326 sighting(s) of QF1'},
        {stage: 'stops', rule: 'unseen-stop', from: '2026-11-01T14:49:00Z', to: '2026-11-01T16:35:26Z', detail: 'a stop at WSSS that no fix showed'}]});
const QF1b = flight('QF1@2', 'QF1', {from: 'WSSS', from_seen: true, to: 'EGLL', to_manual: true, latest: true, start: '2026-11-01T16:35:26Z', end: '2026-11-02T05:36:43Z',
  why: [{stage: 'origin', rule: 'seen-origin', from: '2026-11-01T16:35:26Z', to: '2026-11-01T16:35:26Z', detail: 'origin WSSS taken from the climb-out'}]});
const QF64 = flight('QF64@0', 'QF64', {from: 'FAOR', to: 'YSSY', start: '2026-10-31T16:24:51Z', end: '2026-11-01T04:01:19Z'});

function newWorld(search, reply) {
  const w = {byId: {}, calls: []};
  w.document = {querySelector: s => w.byId[s] ||= new El(s), createElement: t => new El(t), createTextNode: t => ({textContent: t, children: []})};
  w.fetch = async url => {
    w.calls.push(url);
    const r = reply(url);
    return {status: r.status || 200, ok: (r.status || 200) < 400, statusText: 'x', json: async () => r.body};
  };
  const ctx = {document: w.document, fetch: w.fetch, URLSearchParams, Date, Math, JSON, String, location: {search}};
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  w.settle = () => new Promise(r => setImmediate(r)).then(() => new Promise(r => setImmediate(r)));
  return w;
}

(async () => {
  const ok = body => () => ({body});

  // opened from the map on one flight: every flight is listed, newest first, and only that one is open and marked
  let w = newWorld('?hex=7c4926&flight=QF1%402', ok({flights: [QF64, QF1a, QF1b], unplaced: [{stage: 'runs', rule: 'dropped', from: '2026-10-30T19:34:36Z', to: '2026-10-30T19:56:37Z', detail: '4 sighting(s) of QF1 and no position'}]}));
  await w.settle();
  assert.deepStrictEqual(w.calls, ['/api/flights/7C4926/explain?hours=96']);
  assert.strictEqual(w.byId['#who'].textContent, '7C4926');
  let cards = w.byId['#flights'].children;
  assert.strictEqual(cards.length, 3);
  assert.deepStrictEqual(cards.map(c => c.children[0].children[0].textContent), ['QF1 · QFA1', 'QF1 · QFA1', 'QF64 · QFA64']);
  assert.deepStrictEqual(cards.map(c => c.open), [true, false, false]);
  assert.deepStrictEqual(cards.map(c => c.className), ['want', '', '']);
  assert.ok(cards[0].scrolled, 'scrolled to the chosen flight');
  assert.match(text(cards[0].children[0]), /WSSS~ → EGLL\*/, 'the marks for a seen origin and an entered destination');
  assert.match(text(cards[0].children[0]), /latest/);
  assert.match(text(cards[1].children[0]), /YSSY → WSSS.*left 11-01 06:24Z \(takeoff\).*route from airline report/);
  // a flight's notes, in the order they were made, each with the span it covers
  const rows = c => c.children[1].children[0].children.map(tr => tr.children.map(td => td.textContent));
  assert.deepStrictEqual(rows(cards[0]), [['11-01 16:35Z', 'origin', 'seen-origin', 'origin WSSS taken from the climb-out']]);
  assert.deepStrictEqual(rows(cards[1]).map(r => r.slice(1, 3)), [['runs', 'run'], ['stops', 'unseen-stop']]);
  assert.strictEqual(rows(cards[1])[1][0], '11-01 14:49Z to 11-01 16:35Z');
  assert.match(cards[2].children[1].textContent, /Nothing was done/);
  // notes that belong to no flight are listed apart
  assert.strictEqual(w.byId['#loose'].hidden, false);
  assert.strictEqual(w.byId['#looserows'].children.length, 1);

  // no flight asked for: all are open; a longer look-back asks again
  w = newWorld('?hex=7C4926', ok({flights: [QF1a, QF1b]})); await w.settle();
  assert.deepStrictEqual(w.byId['#flights'].children.map(c => c.open), [true, true]);
  assert.strictEqual(w.byId['#loose'].hidden, true);
  w.byId['#hours'].value = '720'; await w.byId['#hours'].onchange(); await w.settle();
  assert.strictEqual(w.calls[1], '/api/flights/7C4926/explain?hours=720');

  // nothing found, a refusal, and no aircraft given are said so
  w = newWorld('?hex=7C4926', ok({flights: []})); await w.settle();
  assert.strictEqual(w.byId['#empty'].hidden, false);
  w = newWorld('?hex=7C4926', () => ({status: 500, body: {error: 'database is locked'}})); await w.settle();
  assert.strictEqual(w.byId['#err'].textContent, 'database is locked');
  w = newWorld('', ok({})); await w.settle();
  assert.match(w.byId['#err'].textContent, /No aircraft given/);
  assert.deepStrictEqual(w.calls, []);
  console.log('uitest-explain: ok');
})().catch(e => { console.error(e); process.exit(1); });
