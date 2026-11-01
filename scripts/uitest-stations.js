// Behaviour of the stations screen (static/stations.html), run under node against a fake DOM, a recording fake Leaflet and a model of
// the stations API. Run: node scripts/uitest-stations.js
const fs = require('fs'), vm = require('vm'), assert = require('assert'), path = require('path');
const html = fs.readFileSync(path.join(__dirname, '../internal/server/static/stations.html'), 'utf8');
const src = html.match(/<script>\n(\/\/ ---- the stations page[\s\S]*?)<\/script>/)[1];

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; this.className = ''; this.hidden = false; this.disabled = false; this.value = ''; this.checked = false; this.title = ''; }
  append(...n) { this.children.push(...n); }
  replaceChildren(...n) { this.children = n; }
  scrollIntoView() { this.scrolled = true; }
}
const secs = s => s;
const NOW = Date.now();
const iso = ms => new Date(ms).toISOString();
const station = (o) => ({ident: 'X', messages: 1, aircraft: 1, first: iso(NOW - 864e5), last: iso(NOW - 3600e3), transports: {vdl: 1}, timed: 0, late: 0, late_frac: 0, median_s: 0, p90_s: 0, shortest_s: 0,
  verdict: 'unknown', known: false, ...o});
const LATE = station({ident: 'EB-LZIB', messages: 235, aircraft: 3, timed: 108, late: 108, late_frac: 1, median_s: 106000, p90_s: 190000, shortest_s: 37749, verdict: 'late', known: true, lat: 48.1486, lon: 17.10775,
  fuzzed: true, country: 'Slovakia', country_code: 'SK', application: 'dumpvdl2', source_type: 'vdl', flagged: true, flagged_reason: 'Rate anomaly detected', nearest_airport: 'LZIB'});
const PROMPT = station({ident: 'AM-LSZH-VDL2', messages: 90, aircraft: 2, timed: 38, late: 0, late_frac: 0, median_s: 4, p90_s: 9, shortest_s: 2, verdict: 'prompt', known: true, lat: 47.4, lon: 8.5, country: 'Switzerland', application: 'dumpvdl2', source_type: 'vdl'});
const SOME = station({ident: 'SK-KLAX-VDLM2', messages: 150, timed: 66, late: 17, late_frac: 17 / 66, median_s: 12, p90_s: 20000, shortest_s: 8, verdict: 'sometimes', known: true, lat: 33.9, lon: -118.4, country: 'United States', application: 'dumpvdl2', source_type: 'vdl'});
const SAT = station({ident: 'TBG-CBAND-APAC', messages: 400, aircraft: 5, transports: {'aero-acars': 400}, timed: 365, late: 0, late_frac: 0, median_s: 20, p90_s: 40, shortest_s: 3, verdict: 'prompt', known: true,
  lat: -32.2, lon: 147, country: 'Australia', application: 'jaero-c-band-acars', source_type: 'aero-acars', flagged: true});
const NEW = station({ident: 'NEW-1', messages: 2, transports: {acars: 2}, timed: 0});   // nothing known about it, and nothing timed

function newWorld(setup) {
  const w = {byId: {}, calls: [], maps: [], markers: [], tileUrls: []};
  w.document = {querySelector: s => w.byId[s] ||= new El(s), createElement: t => new El(t)};
  w.body = {fetched: iso(NOW - 3600e3), can_fetch: true, stations: [SAT, LATE, SOME, PROMPT, NEW]};
  w.refusal = null;
  w.fetch = async (url, opts = {}) => {
    const method = opts.method || 'GET';
    w.calls.push([method, url]);
    const reply = (status, body) => ({status, ok: status < 400, statusText: 'x', json: async () => JSON.parse(JSON.stringify(body))});
    if (url === '/api/stations') return reply(200, w.body);
    if (url === '/api/stations/refresh' && method === 'POST') return w.refusal ? reply(w.refusal.code, {error: w.refusal.error}) : reply(200, {...w.body, fetched: iso(NOW)});
    return reply(404, {error: 'nf'});
  };
  const Layer = class { constructor(ll, o) { this.ll = ll; this.o = o; this.handlers = {}; } addTo(g) { g.items.push(this); w.markers.push(this); return this; }
    bindTooltip(t) { this.tip = t; return this; } on(ev, fn) { this.handlers[ev] = fn; return this; } getLatLng() { return this.ll; } openTooltip() { this.opened = true; return this; } };
  const map = {fits: [], views: [], setView(ll, z) { this.views.push([ll, z]); return this; }, fitBounds(b, o) { this.fits.push([b, o]); }, getZoom: () => 3};
  w.map = map;
  const L = {map: () => map, tileLayer: (u) => { w.tileUrls.push(u); return {addTo() { return this; }}; },
    layerGroup: () => { const g = {items: [], addTo() { return this; }, clearLayers() { this.items = []; }}; w.group = g; return g; },
    circleMarker: (ll, o) => new Layer(ll, o)};
  if (setup) setup(w);
  const ctx = {document: w.document, fetch: w.fetch, L, Date, Math, Set, Map, JSON, Object, String, Promise, console, Number};
  vm.createContext(ctx);
  vm.runInContext(src, ctx);
  w.ctx = ctx;
  w.settle = async () => { for (let i = 0; i < 4; i++) await new Promise(r => setImmediate(r)); };
  return w;
}
const rows = w => w.byId['#rows'].children;
const cells = tr => tr.children.map(td => td.textContent);
const names = w => rows(w).map(tr => tr.children[0].textContent);

(async () => {
  let w = newWorld(); await w.settle();
  assert.ok(w.calls.some(([m, u]) => m === 'GET' && u === '/api/stations'));
  // the busiest station first, every station listed, the columns filled in
  assert.deepStrictEqual(names(w), ['TBG-CBAND-APAC', 'EB-LZIB', 'SK-KLAX-VDLM2', 'AM-LSZH-VDL2', 'NEW-1']);
  assert.deepStrictEqual(w.byId['#heads'].children.map(th => th.textContent), ['Station', 'Where', 'Link', 'Heard', 'Aircraft', 'Last heard', 'Timed', 'Late', 'Median lag', 'Shortest lag', 'Delivery']);
  const late = rows(w)[1];
  assert.deepStrictEqual(cells(late).slice(0, 10), ['EB-LZIB', 'Slovakia · near LZIB', 'VHF (VDL2) · dumpvdl2', '235', '3', '60m ago', '108', '100%', '29h', '10h']);
  const chip = late.children[10].children[0];
  assert.ok(chip.className === 'chip late' && chip.textContent === 'late' && /108 of 108 messages.*median 29h.*shortest 10h/.test(chip.title), chip.className + ' ' + chip.title);
  assert.ok(/Rate anomaly detected/.test(late.children[10].children[1].title), 'airframes.io\'s flag, with its reason');
  assert.strictEqual(late.children[1].title.startsWith('approximate'), true, 'a blurred position says so');
  assert.strictEqual(rows(w)[4].children[0].children[0].textContent, 'position not fetched', 'a station airframes.io has not told us about');
  assert.strictEqual(rows(w)[4].children[10].children[0].textContent, '?');
  assert.strictEqual(cells(rows(w)[4])[7], '—');
  assert.match(w.byId['#fetched'].textContent, /positions and details fetched 60m ago/);
  assert.strictEqual(w.byId['#count'].textContent, '5 of 5 stations · 4 with a position');
  assert.deepStrictEqual(w.byId['#transport'].children.map(o => o.value), ['', 'acars', 'aero-acars', 'vdl']);
  assert.strictEqual(w.byId['#refresh'].hidden, false);

  // the map: a dot for each station with a position, coloured by how it delivers, sized by how much it carries, fitted to them
  assert.strictEqual(w.group.items.length, 4);
  const dot = id => w.group.items.find(m => m.tip.startsWith(id));
  assert.strictEqual(dot('EB-LZIB').o.fillColor, '#c0392b'); assert.strictEqual(dot('AM-LSZH-VDL2').o.fillColor, '#2b8a3e'); assert.strictEqual(dot('SK-KLAX-VDLM2').o.fillColor, '#d98a00');
  assert.ok(dot('TBG-CBAND-APAC').o.radius > dot('AM-LSZH-VDL2').o.radius, 'the busier station is the bigger dot');
  assert.match(dot('EB-LZIB').tip, /position approximate/); assert.match(dot('EB-LZIB').tip, /delivery: late, median lag 29h/);
  assert.strictEqual(w.map.fits.length, 1); assert.strictEqual(w.map.fits[0][0].length, 4);
  assert.deepStrictEqual(w.tileUrls, ['https://tile.openstreetmap.org/{z}/{x}/{y}.png']);

  // choosing a dot selects its row; choosing a row goes to its dot
  dot('SK-KLAX-VDLM2').handlers.click();
  assert.deepStrictEqual(rows(w).map(tr => tr.className), ['', '', 'sel', '', '']);
  assert.strictEqual(rows(w)[2].scrolled, true, 'and brings it into view');
  assert.strictEqual(dot('SK-KLAX-VDLM2').o.weight, 3, 'the chosen dot is outlined');
  w.map.views.length = 0;
  rows(w)[1].onclick();
  assert.deepStrictEqual(rows(w).map(tr => tr.className), ['', 'sel', '', '', '']);
  assert.strictEqual(JSON.stringify(w.map.views[0][0]), '[48.1486,17.10775]', 'the map goes to the station');
  assert.strictEqual(dot('EB-LZIB').opened, true);
  rows(w)[4].onclick();   // a station with no position: selected, and the map stays where it is
  assert.strictEqual(rows(w)[4].className, 'sel');

  // sorting: a click on a column sorts by it, biggest first for numbers; a second click reverses
  w.byId['#heads'].children[7].onclick();
  assert.deepStrictEqual(names(w), ['EB-LZIB', 'SK-KLAX-VDLM2', 'AM-LSZH-VDL2', 'TBG-CBAND-APAC', 'NEW-1'], 'by the share of its messages that are late');
  w.byId['#heads'].children[7].onclick();
  assert.strictEqual(names(w)[names(w).length - 1], 'EB-LZIB');
  w.byId['#heads'].children[0].onclick();
  assert.deepStrictEqual(names(w), ['AM-LSZH-VDL2', 'EB-LZIB', 'NEW-1', 'SK-KLAX-VDLM2', 'TBG-CBAND-APAC'], 'by name, A first');
  w.byId['#heads'].children[3].onclick();
  assert.strictEqual(names(w)[0], 'TBG-CBAND-APAC');

  // filters
  w.byId['#verdict'].value = 'late'; w.byId['#verdict'].onchange();
  assert.deepStrictEqual(names(w), ['EB-LZIB']); assert.strictEqual(w.group.items.length, 1);
  assert.strictEqual(w.byId['#count'].textContent, '1 of 5 stations');
  w.byId['#verdict'].value = ''; w.byId['#verdict'].onchange();
  w.byId['#transport'].value = 'aero-acars'; w.byId['#transport'].onchange();
  assert.deepStrictEqual(names(w), ['TBG-CBAND-APAC']);
  w.byId['#transport'].value = ''; w.byId['#transport'].onchange();
  w.byId['#withpos'].checked = true; w.byId['#withpos'].onchange();
  assert.ok(!names(w).includes('NEW-1') && names(w).length === 4);
  w.byId['#withpos'].checked = false; w.byId['#withpos'].onchange();
  w.byId['#q'].value = 'slovak'; w.byId['#q'].oninput();
  assert.deepStrictEqual(names(w), ['EB-LZIB'], 'by country');
  w.byId['#q'].value = 'lsz'; w.byId['#q'].oninput();
  assert.deepStrictEqual(names(w), ['AM-LSZH-VDL2'], 'by name');
  w.byId['#q'].value = 'nothing like this'; w.byId['#q'].oninput();
  assert.strictEqual(w.byId['#empty'].hidden, false);
  w.byId['#q'].value = ''; w.byId['#q'].oninput();

  // fetching the positions: the button asks the server and shows what came back; a refusal is said as it is
  w.calls.length = 0; w.byId['#fetched'].textContent = '';
  await w.byId['#refresh'].onclick(); await w.settle();
  assert.deepStrictEqual(w.calls, [['POST', '/api/stations/refresh']]);
  assert.match(w.byId['#fetched'].textContent, /fetched 0s ago/); assert.strictEqual(w.byId['#refresh'].disabled, false);
  w.refusal = {code: 429, error: 'the list was fetched 5m0s ago; it changes slowly, so it is fetched at most every 6h0m0s'};
  await w.byId['#refresh'].onclick(); await w.settle();
  assert.match(w.byId['#err'].textContent, /fetched at most every 6h/);

  // positions never fetched: the table still works and says why there are no dots
  w = newWorld(x => { x.body = {can_fetch: true, stations: [{...LATE, known: false, lat: undefined, lon: undefined, fuzzed: false, flagged: false, country: undefined, nearest_airport: undefined}]}; });
  await w.settle();
  assert.strictEqual(w.group.items.length, 0); assert.match(w.byId['#fetched'].textContent, /positions not fetched yet/);
  assert.strictEqual(rows(w)[0].children[0].children[0].textContent, 'position not fetched');
  assert.strictEqual(w.map.fits.length, 0, 'nothing to fit the map to');

  // no source of positions configured: no button
  w = newWorld(x => { x.body = {can_fetch: false, stations: [LATE]}; }); await w.settle();
  assert.strictEqual(w.byId['#refresh'].hidden, true);
  console.log('stations page checks passed');
})().catch(e => { console.error('FAILED:', e.message); if (process.env.UITEST_STACK) console.error(e.stack.split('\n').slice(1, 4).join('\n')); process.exit(1); });
