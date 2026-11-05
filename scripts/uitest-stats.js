// Behaviour of the stats screen (static/stats.html), run under node against a fake DOM and a model of the stats API.
// Run: node scripts/uitest-stats.js
const fs = require('fs'), vm = require('vm'), assert = require('assert'), path = require('path');
const html = fs.readFileSync(path.join(__dirname, '../internal/server/static/stats.html'), 'utf8');
const src = html.match(/<script>\n(\/\/ ---- the stats page[\s\S]*?)<\/script>/)[1];

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; this.className = ''; this.hidden = false; this.style = {}; }
  append(...n) { this.children.push(...n); }
  replaceChildren(...n) { this.children = n; }
}
const NOW = Date.parse('2026-11-05T12:00:00Z');
const iso = ms => new Date(ms).toISOString();
const body = () => ({
  version: 'v0.9.0', now: iso(NOW), started: iso(NOW - (2 * 86400 + 3 * 3600) * 1000), oldest: '2026-10-23T18:01:21Z', db_bytes: 262144000, db_free_bytes: 5242880,
  tables: [{name: 'message', rows: 26401, bytes: 150000000}, {name: 'position', rows: 90000, bytes: 4096}],
  holdings: {Messages: 26401, Messages24h: 1200, Messages7d: 9000, Positions: 90000, Positions24h: 5000, Positions7d: 40000,
    MessagesBy: {vdl: 20000, 'aero-acars': 6000, '': 401}, PositionsBy: {'adsb.lol': 80000, history: 10000}, Decoded: 20000, Undecoded: 6401, Aircraft: 26,
    Watches: {tracking: 2, done: 30}, Airframes: 44, Notes: 3, Stations: 120, WxTiles: 10, WxBytes: 2048,
    Days: [0, 1, 2, 3, 4, 5, 6].map(i => ({Day: `2026-10-${30 + i > 31 ? '0' + (30 + i - 31) : 30 + i}`, Messages: 100 * i, Positions: 50 * i}))},
  requests: [
    {host: 'aerodatabox.p.rapidapi.com', today: 3, yesterday: 0, week: 12, month: 40, total: 44, failed: 1, limited: 1, since: '2026-10-26', note: 'a flight lookup is probably 2 of the plan\'s units'},
    {host: 'api.airframes.io', today: 500, yesterday: 480, week: 3400, month: 9000, total: 12000, failed: 0, limited: 0, since: '2026-10-23'}],
  holds: [{name: 'aerodatabox', until: iso(NOW + 3600e3), reason: 'MONTHLY quota'}],
  caches: [{name: 'OpenStreetMap tiles', bytes: 3 * 1048576, files: 40, limit: 100 * 1048576, dir: 'data/tiles'}],
  busiest: [{hex: 'A2551D', tail: 'N24993', messages: 900, positions: 7000}, {hex: 'ABCDEF', messages: 5, positions: 0}],
});

function newWorld(setup) {
  const w = {byId: {}, calls: [], body: body(), failWith: null, timers: []};
  w.document = {querySelector: s => w.byId[s] ||= new El(s), createElement: t => new El(t)};
  w.fetch = async url => {
    w.calls.push(url);
    if (w.failWith) return {ok: false, status: 500, statusText: 'x', json: async () => ({error: w.failWith})};
    return {ok: true, status: 200, json: async () => JSON.parse(JSON.stringify(w.body))};
  };
  if (setup) setup(w);
  const ctx = {document: w.document, fetch: w.fetch, Date, Math, JSON, Object, String, Number, Promise, console, Array,
    setInterval: (fn, ms) => { w.timers.push([fn, ms]); return w.timers.length; }};
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  w.settle = async () => { for (let i = 0; i < 4; i++) await new Promise(r => setImmediate(r)); };
  return w;
}
const rows = (w, id) => w.byId[id].children.map(tr => tr.children.map(td => td.textContent));

(async () => {
  let w = newWorld(); await w.settle();
  assert.deepStrictEqual(w.calls, ['/api/stats']);
  const tiles = w.byId['#tiles'].children.map(t => t.children.map(c => c.textContent));
  assert.deepStrictEqual(tiles[0], ['v0.9.0', 'running 2d 3h']);
  assert.deepStrictEqual(tiles[1], ['250 MiB', 'database, 5.0 MiB free inside']);
  assert.deepStrictEqual(tiles[2], ['26,401', 'messages, 1,200 in the last day']);
  assert.deepStrictEqual(tiles[5], ['2026-10-23', 'oldest data']);
  assert.match(w.byId['#updated'].textContent, /updated 12:00Z/);

  // the request counters: a row a host, busiest first, what a call costs where we know, and refusals picked out
  assert.deepStrictEqual(w.byId['#reqheads'].children[0].children.map(th => th.textContent), ['Host', 'Today', 'Yesterday', 'Last 7 days', 'This month', 'Total', 'Failed', 'Refused (429)', 'Since']);
  assert.deepStrictEqual(rows(w, '#reqrows')[0], ['aerodatabox.p.rapidapi.com', '3', '0', '12', '40', '44', '1', '1', '2026-10-26']);
  assert.match(w.byId['#reqrows'].children[0].children[0].children[0].textContent, /2 of the plan's units/, 'what a call costs, under the host');
  assert.deepStrictEqual(rows(w, '#reqrows')[1], ['api.airframes.io', '500', '480', '3,400', '9,000', '12,000', '0', '0', '2026-10-23']);
  assert.match(w.byId['#reqrows'].children[0].children[7].className, /bad/, 'a 429 is flagged');
  assert.ok(!/bad/.test(w.byId['#reqrows'].children[1].children[7].className));
  assert.strictEqual(w.byId['#noreq'].hidden, true);
  assert.deepStrictEqual(w.byId['#holds'].children.map(h => h.textContent), ['aerodatabox is on hold until 13:00Z: MONTHLY quota']);

  // what we hold
  assert.deepStrictEqual(rows(w, '#holdrows'), [['Total', '26,401', '90,000'], ['Last 24 hours', '1,200', '5,000'], ['Last 7 days', '9,000', '40,000'], ['Messages we could read', '20,000 (76%)', '']]);
  assert.strictEqual(rows(w, '#dayrows').length, 7);
  assert.deepStrictEqual(rows(w, '#bylink'), [['vdl', '20,000'], ['aero-acars', '6,000'], ['(none)', '401']]);
  assert.deepStrictEqual(rows(w, '#bysource'), [['adsb.lol', '80,000'], ['history', '10,000']]);
  assert.deepStrictEqual(rows(w, '#watches').slice(0, 2), [['done', '30'], ['tracking', '2']]);
  assert.deepStrictEqual(rows(w, '#busyrows'), [['N24993', '900', '7,000'], ['ABCDEF', '5', '0']]);
  const disk = rows(w, '#diskrows');
  assert.deepStrictEqual(disk[0], ['table message', '26,401', '143 MiB']);
  assert.deepStrictEqual(disk[2], ['OpenStreetMap tiles', '40', '3.0 MiB of 100 MiB']);
  assert.deepStrictEqual(disk[3], ['radar tiles recorded', '10', '2.0 KiB']);

  // it refreshes itself while open, and picks up new numbers
  assert.deepStrictEqual(w.timers.map(t => t[1]), [30000]);
  w.body.requests[0].today = 4; w.body.holds = [];
  await w.timers[0][0](); await w.settle();
  assert.strictEqual(rows(w, '#reqrows')[0][1], '4'); assert.strictEqual(w.byId['#holds'].children.length, 0);

  // nothing counted yet, nothing held: the page still draws
  w = newWorld(x => { x.body.requests = []; x.body.holds = []; x.body.busiest = []; x.body.caches = []; x.body.holdings.WxTiles = 0; x.body.holdings.MessagesBy = {}; });
  await w.settle();
  assert.strictEqual(w.byId['#noreq'].hidden, false); assert.strictEqual(w.byId['#reqrows'].children.length, 0);
  assert.deepStrictEqual(rows(w, '#bylink'), [['none yet', '']]);
  assert.strictEqual(rows(w, '#diskrows').length, 2, 'only the tables');

  // a failure is said, and the next good load clears it
  w = newWorld(x => { x.failWith = 'database is locked'; }); await w.settle();
  assert.strictEqual(w.byId['#err'].textContent, 'database is locked');
  w.failWith = null; await w.timers[0][0](); await w.settle();
  assert.strictEqual(w.byId['#err'].textContent, '');
  console.log('stats page checks passed');
})().catch(e => { console.error('FAILED:', e.message); if (process.env.UITEST_STACK) console.error(e.stack.split('\n').slice(1, 4).join('\n')); process.exit(1); });
