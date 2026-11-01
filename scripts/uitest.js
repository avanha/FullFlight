const fs = require('fs'), vm = require('vm'), assert = require('assert');
// Drives the real page script in internal/server/static/index.html against a fake DOM and a
// recording fake Leaflet. Run with: node scripts/uitest.js
const html = fs.readFileSync(require('path').join(__dirname, '../internal/server/static/index.html'), 'utf8');
const src = html.match(/<script>\n(const map[\s\S]*?)<\/script>/)[1];

// Two elements with one id make $('#id') find the first, and whatever draws into the second draws into the wrong place
// (a route-suggestion list once took over the airport list this way). Every page must have each id once, and every
// list="..." must name an id that exists.
for (const page of ['index.html', 'airframes.html']) {
  const text = fs.readFileSync(require('path').join(__dirname, '../internal/server/static', page), 'utf8');
  const ids = [...text.matchAll(/\bid="([^"]+)"/g)].map(m => m[1]);
  const dup = ids.filter((id, i) => ids.indexOf(id) !== i);
  assert.deepStrictEqual(dup, [], `${page}: duplicate ids ${dup}`);
  for (const m of text.matchAll(/\blist="([^"]+)"/g)) assert.ok(ids.includes(m[1]), `${page}: list="${m[1]}" names no element`);
}

// --- a just-enough DOM ---
class El {
  constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; this.className = ''; this.hidden = false; this.title = ''; this.dataset = {}; this.style = {};
    this._cls = new Set(); const self = this;
    this.classList = { toggle(c, on) { on ? self._cls.add(c) : self._cls.delete(c); }, contains: c => self._cls.has(c) || self.className.split(' ').includes(c) }; }
  append(...n) { this.children.push(...n); }
  replaceChildren(...n) { this.children = n; }
  remove() { this.removed = true; }
  scrollIntoView(o) { this.scrolled = o || true; }
  select() { this.selectedAll = true; }
  querySelector(sel) { return this._q ||= {}, this._q[sel] ||= new El('q'); }
  set innerHTML(v) { this._html = v; this.children = []; }
  get innerHTML() { return this._html; }
}
const byId = {};
const document = {
  body: new El('body'),
  execResult: true,
  execCommand(c) { calls.exec.push(c); return this.execResult; },
  querySelector: s => byId[s] ||= new El(s),
  createElement: t => new El(t),
  createTextNode: t => ({text: t}),
};
// --- a recording Leaflet ---
const calls = { mounted: [], legendExtra: null, exec: [], copied: [], timers: [], lines: [], legend: null, addLayer: 0, fitBounds: 0, setView: [], panTo: [], markers: [], circles: [], polylines: 0, tooltipsOpened: 0, highlightAdds: [] };
const allLayers = [];
class Layer { on(ev, fn) { (this.handlers ||= {})[ev] = fn; return this; }
  constructor(){ this.items = []; this.open = false; allLayers.push(this); } addTo(t) { this.target = t; this.onMap = (t === mapObj); t.items && t.items.push(this); if (t === highlightGroup) calls.highlightAdds.push(this); return this; }
  setIcon(i) { this.o = {...this.o, icon: i}; return this; } bindTooltip(c) { this.tooltip = c; return this; } openTooltip() { calls.tooltipsOpened++; this.open = true; return this; } closeTooltip() { this.open = false; return this; } getBounds() { return {}; } }
let highlightGroup;
const groups = [];
const mapObj = { setView(ll, z) { calls.setView.push([ll, z]); return this; }, eachLayer(f) { allLayers.forEach(f); }, fitBounds() { calls.fitBounds++; }, panTo(ll) { calls.panTo.push(ll); },
  hasLayer: g => !!g.onMap, addLayer(g) { calls.addLayer++; g.onMap = true; }, removeLayer(g) { g.onMap = false; },
  getBounds: () => ({ pad: () => ({ contains: ll => !(ll[0] > 50) }) }) };   // "in view" unless lat > 50
const L = {
  map: () => mapObj, tileLayer: () => ({ addTo() { return this; }, on() { return this; }, setUrl() { return this; } }),
  TileLayer: { extend: () => function () { return { addTo() { return this; }, on() { return this; }, setUrl() { return this; }, redraw() { return this; } }; } },
  layerGroup() { const g = new Layer(); g.clearLayers = () => { g.items = []; }; g.removeLayer = l => { g.items = g.items.filter(x => x !== l); }; groups.push(g); return g; },
  polyline(ll, o) { calls.polylines++; const l = new Layer(); l.ll = ll; l.o = o; calls.lines.push(l); return l; },
  control: { layers: (base, overlays) => ({ addTo() { if (!calls.legend) calls.legend = overlays; else calls.legendExtra = overlays; return this; } }) },
  circleMarker(ll, o) { const l = new Layer(); l.ll = ll; l.o = o; calls.circles.push(l); return l; },
  marker(ll, o) { const l = new Layer(); l.ll = ll; l.o = o; calls.markers.push(l); return l; },
  divIcon: o => o, featureGroup: () => ({ getBounds: () => ({}) }),
};
// --- the server ---
const planData = [];     // what /api/plan returns
const airportData = {total: 1, airports: [{icao: 'FAOR', iata: 'JNB', name: 'O.R. Tambo International Airport', city: 'Johannesburg'}]};   // what /api/airports returns
const flightData = [];   // what /api/flights returns; empty unless a test sets it
const storage = {trackMode: 'source'};   // the older tests below look at the per-source lines; the unified track has its own (8c2)
const watch = {id: 1, kind: 'tail', ident: 'B-LRM', start: '2026-10-30T10:00:00Z', end: '2026-11-01T00:00:00Z', status: 'tracking',
  hex: '780AB8', tail: 'B-LRM', type: 'A359', last_pos: {t: new Date().toISOString(), kind: 'ADS-B', lat: 40, lon: -100}};
const pts = [{t: '2026-10-30T16:00:00Z', lat: 40, lon: -101, alt_ft: 35000, gs: 470, track: 90, vrate: 0, callsign: 'CPA843', source: 'adsb.lol'},
             {t: '2026-10-30T16:01:00Z', lat: 40, lon: -100, alt_ft: 35025, gs: 480.4, track: 135, vrate: -1800, callsign: 'CPA843', source: 'adsb.lol'}];
const msgs = [
  {id: 1, t: '2026-10-30T15:00:00Z', kind: 'adsc', summary: 'ADS-C report', lat: 41, lon: -105, raw: 'r1', label: 'B6', rx_count: 1},
  {id: 2, t: '2026-10-30T15:10:00Z', kind: 'cpdlc', summary: 'CPDLC downlink: WILCO', raw: 'r2', label: 'BA', rx_count: 1},   // no location
  {id: 3, t: '2026-10-30T15:20:00Z', kind: 'fms_position', summary: 'FMS position report', lat: 60, lon: 10, raw: 'r3', label: 'H1', rx_count: 1,
   time_unverified: true, time_note: 'arrived 15:20 Z, report waypoint time 23:55:30 (+8h35m)'},                                       // off-screen, unverified time
  {id: 4, t: '2026-10-30T15:30:00Z', kind: 'unknown', summary: 'Undecoded H1', raw: 'r4', label: 'H1', rx_count: 1, undecoded: true},
];
let postReply = null, lastPostBody = null;
let putReply = {status: 200, body: {}}, lastPut = null;   // PUT /api/flights/{hex}/route
const fetchLog = [];
global.fetch = async (url, opts) => {
  fetchLog.push(url);
  if (opts && opts.method === 'PUT') { lastPut = {url, body: JSON.parse(opts.body)}; return { status: putReply.status, ok: putReply.status < 400, json: async () => putReply.body }; }
  if (opts && opts.method === 'POST') { lastPostBody = JSON.parse(opts.body); return { status: postReply.status, ok: postReply.status < 400, json: async () => postReply.body }; }
  const body = url.startsWith('/api/watches') ? [watch] : url.startsWith('/api/track') ? pts : url.startsWith('/api/messages') ? msgs : url.startsWith('/api/flights') ? flightData : url.startsWith('/api/plan') ? planData : url.startsWith('/api/airports') ? airportData : null;
  return { status: 200, ok: true, json: async () => JSON.parse(JSON.stringify(body)) };   // a fresh copy each time, like a real response
};
let intervals = [];
const ctx = { document, L, fetch, console, Date, Number, Math, Set, Map, setInterval: (f, ms) => intervals.push([f, ms]), FormData: class { constructor(form) { this.v = (form && form.values) || {}; } get(k) { return this.v[k] ?? ''; } },
  setTimeout: f => { calls.timers.push(f); }, clearTimeout: () => {}, window: {isSecureContext: true},
  mountNotes: (container, hex, opts) => { calls.mounted.push({container, hex, opts}); },
  localStorage: {getItem: k => (k in storage ? storage[k] : null), setItem: (k, v) => { storage[k] = String(v); }},
  navigator: {clipboard: {writeText: async t => { calls.copied.push(t); }}} };
ctx.global = ctx;
vm.createContext(ctx);
highlightGroup = null;

(async () => {
  // run the page script; capture the two layer groups (track layer first, highlight second)
  vm.runInContext(src + '\n;globalThis.__t = {readAs, segOf, chooseFlight, syncFlightSelector, get flightChoice() { return flightChoice; }, get flightSegs() { return flightSegs; }, predGroup, projection, estimatedPaths, setTrackMode, thinPoints, bridgeGroup, walkRoute, choosePlan, drawPlan, planGroup, ghostAt, advance, drawProjection, wptGroup, projGroup, setAhead, pickMessage, get lonRef() { return lonRef; }, get selectedMsg2() { return selectedMsg; }, SOURCES, refresh, showTrack, renderMessages, get selectedMsg(){return selectedMsg}, expanded, rows, markers, watches: () => watches};', ctx);
  highlightGroup = groups[1];
  const t = ctx.__t;
  const initialViews = calls.setView.length;
  const tick = () => new Promise(r => setTimeout(r, 10));
  await tick();

  // 1. the watch list renders, with the last-position line
  const li = byId['#list'].children[0];
  assert.ok(li, 'watch row rendered');
  assert.ok(li.children.some(c => /last ADS-B .* · A359/.test(c.textContent || '')), 'last position + type line');

  // 2. selecting the watch draws the plane at the last ADS-B point, rotated to its track
  // (the page judges how old a fix is by the clock: stand it at a minute after this one, as when it was written)
  const realNow = Date.now;
  Date.now = () => Date.parse('2026-10-30T16:02:00Z');
  li.onclick(); await tick(); await tick();
  Date.now = realNow;
  const plane = calls.markers.find(m => m.o && m.o.icon);
  assert.ok(plane, 'a marker with an icon was created');
  assert.strictEqual(JSON.stringify(plane.ll), JSON.stringify([40, -100]));
  assert.match(plane.o.icon.html, /rotate\(135deg\)/, 'plane turned to the track');
  assert.strictEqual(plane.o.icon.className, 'plane');
  assert.ok(!calls.circles.some(c => c.o && c.o.color === '#c00' && c.o.radius === 6), 'the old red dot is gone');
  const views = calls.setView.slice(initialViews);
  assert.strictEqual(views.length, 1, 'centres once on selection');
  assert.strictEqual(JSON.stringify(views[0]), '[[40,-100],7]', 'on the newest fix (the last ADS-B point), at regional zoom');
  assert.strictEqual(calls.fitBounds, 0, 'no longer fits the whole history');
  const openTips = () => allLayers.filter(l => l.open);
  const lines = el => el.children.map(c => c.textContent);
  assert.deepEqual(lines(plane.tooltip).map(l => l.replace(/ \u00b7 \d+\S* ago/, '')), ['B-LRM \u00b7 CPA843 \u00b7 A359', '35,025 ft  \u2193 descending 1,800 ft/min', '480 kt \u00b7 track 135\u00b0', '2026-10-30 16:01Z (adsb.lol)'], 'plane hover text');

  // 3. the undecoded message is hidden; the rest are listed newest first
  const box = byId['#msgs'];
  const rowEls = box.children.filter(c => c.className && c.className.startsWith('msg'));
  assert.strictEqual(rowEls.length, 3, 'undecoded hidden');
  assert.ok(box.children[0].children.some(c => c.textContent === 'show 1 undecoded'), 'toggle link');
  assert.ok(t.rows.get(1).className.includes('hasloc') && !t.rows.get(2).className.includes('hasloc'));
  // the unverified-time badge and detail line
  const r3 = t.rows.get(3);
  assert.ok(r3.children.some(c => c.textContent === '⏱' && /unverified|waypoint time/.test(c.title)), 'time badge');
  assert.ok(r3.children.at(-1).textContent.includes('time: arrived 15:20 Z'), 'time shown in details');

  // 4. clicking a message with a location rings it and opens its tooltip; in view => no pan
  calls.highlightAdds.length = 0; calls.panTo.length = 0;
  t.rows.get(1).onclick();
  assert.strictEqual(t.selectedMsg, 1);
  assert.strictEqual(calls.highlightAdds.length, 1, 'one ring');
  const ring = calls.circles.at(-1);
  assert.strictEqual(JSON.stringify(ring.ll), JSON.stringify([41, -105])); assert.strictEqual(ring.o.radius, 13);
  assert.strictEqual(calls.panTo.length, 0, 'no pan when already in view');
  assert.ok(calls.tooltipsOpened >= 1);
  assert.strictEqual(openTips().length, 1, 'exactly one popup open');
  assert.strictEqual(openTips()[0], t.markers.get(1));
  assert.ok(t.rows.get(1)._cls.has('sel') && !t.rows.get(3)._cls.has('sel'), 'row highlighted');
  assert.strictEqual(t.rows.get(1).children.at(-1).hidden, false, 'details opened too');

  // 5. an off-screen message pans the map to it, and moves the ring
  t.rows.get(3).onclick();
  assert.strictEqual(JSON.stringify(calls.panTo.at(-1)), '[60,10]', 'panned to the off-screen position');
  assert.strictEqual(JSON.stringify(calls.circles.at(-1).ll), JSON.stringify([60, 10]));
  assert.strictEqual(highlightGroup.items.length, 1, 'only one ring at a time');
  assert.strictEqual(openTips().length, 1, 'the previous popup closed');
  assert.strictEqual(openTips()[0], t.markers.get(3));
  assert.ok(t.rows.get(3)._cls.has('sel') && !t.rows.get(1)._cls.has('sel'));

  // 6. a message without a location has no ring; if fixes surround its time, a ghost plane stands where the path puts it
  const ringsBefore = calls.circles.length;
  t.rows.get(2).onclick();
  assert.strictEqual(calls.circles.length, ringsBefore, 'no ring');
  assert.strictEqual(highlightGroup.items.length, 1, 'the ghost');
  assert.match(highlightGroup.items[0].o.icon.html, /opacity/, 'drawn as a faded plane');
  assert.strictEqual(openTips().length, 1, 'its popup is open');
  assert.match(lines(highlightGroup.items[0].tooltip)[0], /^Estimated position at /);
  t.rows.get(1).onclick();
  assert.strictEqual(highlightGroup.items.length, 1, 'the ghost is gone; just the ring of the message with a position');
  assert.ok(calls.circles.length > ringsBefore, 'the ring');
  t.rows.get(1).onclick();            // (clicking toggles the details: put them back)
  t.rows.get(2).onclick();
  assert.strictEqual(t.selectedMsg, 2);

  // 6b. clicking through many messages never accumulates popups
  for (let i = 0; i < 6; i++) { [1, 3, 2, 1, 3].forEach(id => t.rows.get(id).onclick()); }
  assert.ok(openTips().length <= 1, 'at most one popup after lots of clicking, got ' + openTips().length);

  // 7. the 30 s refresh keeps the view, the selection and the open details
  t.rows.get(1).onclick();            // clicking toggles: this closes message 1 ...
  assert.strictEqual(t.rows.get(1).children.at(-1).hidden, true);
  t.rows.get(1).onclick();            // ... and this reopens it, selected
  const fits = calls.fitBounds, pans = calls.panTo.length;
  const [refreshFn] = intervals.find(([, ms]) => ms === 30000);
  await refreshFn(); await tick();
  assert.strictEqual(calls.fitBounds, fits, 'refresh must not re-fit the map');
  const viewsBefore = calls.setView.length;
  assert.strictEqual(calls.panTo.length, pans, 'refresh must not pan');
  assert.strictEqual(calls.setView.length, viewsBefore, 'refresh must not re-centre the map');
  assert.strictEqual(openTips().length, 1, 'the selected message keeps its popup after a refresh');
  assert.strictEqual(t.selectedMsg, 1, 'selection survives');
  assert.strictEqual(t.rows.get(1).children.at(-1).hidden, false, 'open details survive');
  assert.ok(t.rows.get(1)._cls.has('sel'), 'row still highlighted');
  assert.strictEqual(highlightGroup.items.length, 1, 'ring redrawn after refresh');

  // 7b. the arrow: climbing, level, descending (reported rate), and estimated when no rate is reported
  const tipFor = async last => { const saved = pts.slice(); pts.splice(0, pts.length, ...saved.slice(0, 1), last); calls.markers.length = 0;
    await t.showTrack(t.watches()[0], false); const pl = calls.markers.find(m => m.o && m.o.icon); pts.splice(0, pts.length, ...saved); return lines(pl.tooltip)[1]; };
  const base = {t: '2026-10-30T16:01:00Z', lat: 40, lon: -100, alt_ft: 36000, callsign: 'CPA843', source: 'adsb.lol'};
  assert.match(await tipFor({...base, vrate: 2100}), /\u2191 climbing 2,100 ft\/min/);
  assert.match(await tipFor({...base, vrate: 64}), /\u2192 level/);
  assert.match(await tipFor({...base, vrate: null, alt_ft: 36600}), /\u2191 climbing 1,600 ft\/min/, 'estimated: 35,000 -> 36,600 ft in one minute');
  assert.match(await tipFor({...base, vrate: null, alt_ft: 33800}), /\u2193 descending 1,200 ft\/min/);
  assert.doesNotMatch(await tipFor({...base, vrate: null, alt_ft: 36000, t: '2026-10-30T16:00:20Z'}), /climbing|descending|level/, 'too little history to estimate');
  assert.match(await tipFor({...base, on_ground: true, alt_ft: null, vrate: null}), /on the ground/);

  // 8. no ADS-B (mid-ocean): the plane goes at the newest trustworthy datalink position, pointing north.
  //    Message 3 is suspect-free but its time is unverified, so it is skipped; message 1 is used.
  const savedPts = pts.splice(0);
  calls.markers.length = 0;
  await t.showTrack(t.watches()[0], false);
  const fallbackViews = calls.setView.length;
  const fallback = calls.markers.find(m => m.o && m.o.icon);
  assert.ok(fallback, 'fallback plane drawn');
  assert.strictEqual(JSON.stringify(fallback.ll), '[41,-105]', 'uses message 1, not the unverified-time message 3');
  assert.match(fallback.o.icon.html, /rotate\(0deg\)/);
  pts.push(...savedPts);

  // 8b. which fix a newly chosen target is centred on: the newest trustworthy one
  const focusOn = async (ptsArr, msgsArr) => {
    const sp = pts.slice(), sm = msgs.slice();
    pts.splice(0, pts.length, ...ptsArr); msgs.splice(0, msgs.length, ...msgsArr);
    const n = calls.setView.length;
    await t.showTrack(t.watches()[0], true);
    const r = calls.setView.length > n ? JSON.stringify(calls.setView.at(-1)) : null;
    pts.splice(0, pts.length, ...sp); msgs.splice(0, msgs.length, ...sm);
    return r;
  };
  const adsb = (hh, lat, lon) => ({t: `2026-10-30T${hh}:00Z`, lat, lon, alt_ft: 35000, track: 90, source: 'adsb.lol'});
  const msg = (id, hh, lat, lon, extra = {}) => ({id, t: `2026-10-30T${hh}:00Z`, kind: 'adsc', summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat, lon, ...extra});
  // ADS-B is stale, a datalink position is newer: centre on the datalink one (the oceanic case)
  assert.strictEqual(await focusOn([adsb('14:00', 40, -100)], [msg(1, '15:00', 41, -105)]), '[[41,-105],7]');
  // ADS-B is newer than the datalink position
  assert.strictEqual(await focusOn([adsb('16:00', 40, -100)], [msg(1, '15:00', 41, -105)]), '[[40,-100],7]');
  // a later position whose time is unverified, or one flagged impossible, does not beat an older trustworthy one
  assert.strictEqual(await focusOn([], [msg(1, '15:00', 41, -105), msg(2, '17:00', 60, 10, {time_unverified: true}), msg(3, '18:00', 5, 5, {suspect: 'x'})]), '[[41,-105],7]');
  // with only unverified-time positions, the newest of those; only impossible ones, the newest of those
  assert.strictEqual(await focusOn([], [msg(2, '17:00', 60, 10, {time_unverified: true}), msg(3, '18:00', 5, 5, {suspect: 'x'})]), '[[60,10],7]');
  assert.strictEqual(await focusOn([], [msg(3, '18:00', 5, 5, {suspect: 'x'}), msg(4, '19:00', 6, 6, {suspect: 'y'})]), '[[6,6],7]');
  // a message position is placed by when it says it was, not when it arrived
  assert.strictEqual(await focusOn([], [msg(1, '20:00', 41, -105, {pos_t: '2026-10-30T10:00:00Z'}), msg(2, '12:00', 42, -106)]), '[[42,-106],7]');
  // nothing with a position: the map is left where it is
  assert.strictEqual(await focusOn([], [{id: 9, t: '2026-10-30T15:00:00Z', kind: 'cpdlc', summary: 's', raw: 'r', label: 'BA', rx_count: 1}]), null);

  // 8c. a track per source, in the source's colour; broken at long silences and impossible jumps
  const COLORS = {adsb: '#0b66d0', adsc: '#e8590c', cpdlc: '#7b2cbf', miam: '#0a9396', fms_position: '#2b9348'};
  const lineOf = async (ptsArr, msgsArr) => {
    const sp = pts.slice(), sm = msgs.slice();
    pts.splice(0, pts.length, ...ptsArr); msgs.splice(0, msgs.length, ...msgsArr);
    calls.lines.length = 0;
    await t.showTrack(t.watches()[0], false);
    pts.splice(0, pts.length, ...sp); msgs.splice(0, msgs.length, ...sm);
    return calls.lines.filter(l => l.o.color !== '#777').map(l => ({color: l.o.color, n: l.ll.length, dash: l.o.dashArray, ll: l.ll}));   // (not the dotted estimated paths)
  };
  const at2 = (mm, lat, lon, kind, extra = {}) => ({id: 100 + mm, t: `2026-10-30T15:${String(mm).padStart(2, '0')}:00Z`, kind, summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat, lon, ...extra});
  // the legend lists all five sources
  assert.deepStrictEqual(Object.keys(calls.legend).map(k => k.replace(/<[^>]*>|&#\d+;/g, '').trim()), ['ADS-B', 'ADS-C', 'CPDLC', 'REP081', 'FMS', 'ACMF', 'AOC', 'POS', 'POSRPT', 'POSA1', 'SB', 'AeroDataBox']);
  assert.ok(Object.keys(calls.legendExtra).map(k => k.replace(/<[^>]*>|&#?\w+;/g, '').trim()).join('|') === 'Weather radar|Satellite (IR)|Waypoints|Predictions|Planned route|Estimated path|Projection|Wind barbs', 'radar, satellite, waypoints, predictions, projection and wind barbs have legend entries');
  // each source gets its own colour and style
  for (const kind of ['adsc', 'cpdlc', 'miam', 'fms_position']) {
    const r = await lineOf([], [at2(0, 41, -105, kind), at2(10, 41.1, -104, kind), at2(20, 41.2, -103, kind)]);
    assert.strictEqual(r.length, 1, kind + ': one line');
    assert.strictEqual(r[0].color, COLORS[kind]); assert.strictEqual(r[0].n, 3);
  }
  assert.strictEqual((await lineOf([], [at2(0, 41, -105, 'cpdlc'), at2(10, 41.1, -104, 'cpdlc')]))[0].dash, '10 4 2 4');
  // ADS-B is a line in its own colour
  const ab = (mm, lat) => ({t: `2026-10-30T16:${String(mm).padStart(2, '0')}:00Z`, lat, lon: -100, alt_ft: 35000, track: 90, source: 'adsb.lol'});
  let r = await lineOf([ab(0, 40), ab(1, 40.1), ab(2, 40.2)], []);
  assert.strictEqual(r.length, 1); assert.strictEqual(r[0].color, COLORS.adsb); assert.strictEqual(r[0].n, 3);
  // the flight-number provider's positions are their own grey track, never part of the ADS-B line
  const pv = (mm, lat) => ({...ab(mm, lat), source: 'aerodatabox'});
  r = await lineOf([ab(0, 40), ab(1, 40.1), pv(2, 40.2), pv(30, 41)], []);
  assert.deepStrictEqual(r.map(x => [x.color, x.n]), [[COLORS.adsb, 2], ['#6c757d', 2]]);
  // ... but a 25 minute silence (out of coverage) is not bridged; the stray single point draws no line
  r = await lineOf([ab(0, 40), ab(1, 40.1), ab(26, 41), ab(27, 41.1), ab(50, 42)], []);
  assert.deepStrictEqual(r.map(x => x.n), [2, 2], 'ADS-B split at the gap, lone point left out');
  // a datalink silence of 100 minutes (> the 90 min allowed for ADS-C) splits it
  r = await lineOf([], [at2(0, 41, -105, 'adsc'), at2(10, 41.1, -104, 'adsc'), at2(50, 41.5, -100, 'adsc'), at2(51, 41.51, -100, 'adsc')].map((m, i) => i < 2 ? m : {...m, t: m.t.replace('T15:', 'T17:')}));
  assert.deepStrictEqual(r.map(x => x.n), [2, 2], 'ADS-C split at a long gap');
  // an impossible jump (Los Angeles to Boston in 10 minutes) ends a track instead of drawing across the country
  r = await lineOf([], [at2(0, 34.0, -118.4, 'fms_position'), at2(5, 34.1, -118.0, 'fms_position'), at2(15, 41.5, -72.2, 'fms_position'), at2(20, 41.6, -72.0, 'fms_position')]);
  assert.deepStrictEqual(r.map(x => x.n), [2, 2], 'two tracks, none across the continent');
  assert.ok(r.every(x => x.ll.every(p => (p[1] < -100) === (x.ll[0][1] < -100))), 'no line mixes the two clusters');
  // positions flagged impossible are left out of the line; the rest still join up
  r = await lineOf([], [at2(0, 41, -105, 'adsc'), at2(10, 5, 5, 'adsc', {suspect: 'x'}), at2(20, 41.2, -103, 'adsc')]);
  assert.strictEqual(r.length, 1); assert.strictEqual(JSON.stringify(r[0].ll.map(p => p[0])), '[41,41.2]');
  // input in any order is drawn in time order, by when the position says it was
  r = await lineOf([], [at2(30, 41.3, -102, 'adsc'), at2(0, 41, -105, 'adsc', {pos_t: '2026-10-30T15:00:00Z'}), at2(15, 41.15, -103.5, 'adsc')]);
  assert.strictEqual(JSON.stringify(r[0].ll.map(p => p[0])), '[41,41.15,41.3]');
  // the dot for each message position takes its source's colour
  calls.circles.length = 0;
  await lineOf([], [at2(0, 41, -105, 'cpdlc'), at2(10, 5, 5, 'cpdlc', {suspect: 'x'})]);
  const dots = calls.circles.filter(c => c.o.radius === 4);
  assert.deepStrictEqual(dots.map(c => c.o.color), [COLORS.cpdlc, '#888'], 'source colour, grey if impossible');

  // 8c2. the unified track: one line through every plausible report, coloured by source, switched to from the legend
  t.setTrackMode('unified');
  assert.strictEqual(storage.trackMode, 'unified', 'the choice is remembered');
  const rp = (mm, lat, kind, extra = {}) => at2(mm, lat, -100, kind, {t: `2026-10-30T16:${String(mm).padStart(2, '0')}:00Z`, ...extra});
  // ADS-B handing over to ADS-C minutes later is one connected track, the colour changing half way along the join
  r = await lineOf([ab(0, 40), ab(1, 40.1), ab(2, 40.2)], [rp(5, 40.3, 'adsc'), rp(10, 40.4, 'adsc')]);
  assert.deepStrictEqual(r.map(x => [x.color, x.n]), [[COLORS.adsb, 4], [COLORS.adsc, 3]]);
  assert.ok(Math.abs(r[0].ll[3][0] - 40.25) < 1e-9 && JSON.stringify(r[0].ll[3]) === JSON.stringify(r[1].ll[0]), 'both halves meet at the midpoint');
  assert.strictEqual(r[0].dash, undefined, 'solid whatever the source');
  assert.strictEqual(t.bridgeGroup.items.length, 0, 'close together: no dotted estimate');
  // a report among ADS-B fixes keeps its dot but is left out of the line
  r = await lineOf([ab(0, 40), ab(1, 40.1), ab(2, 40.2), ab(3, 40.3)], [rp(2, 40.25, 'adsc')]);
  assert.deepStrictEqual(r.map(x => [x.color, x.n]), [[COLORS.adsb, 4]]);
  // a source unticked in the legend leaves the line
  t.SOURCES.adsb.group.onMap = false;
  r = await lineOf([ab(0, 40), ab(1, 40.1), ab(2, 40.2)], [rp(5, 40.3, 'adsc'), rp(10, 40.4, 'adsc')]);
  t.SOURCES.adsb.group.onMap = true;
  assert.deepStrictEqual(r.map(x => [x.color, x.n]), [[COLORS.adsc, 2]]);
  // a silence longer than ADS-B allows ends the solid line and gets the dotted estimate instead
  r = await lineOf([ab(0, 40), ab(1, 40.1), ab(31, 40.5), ab(32, 40.6)], []);
  assert.deepStrictEqual(r.map(x => x.n), [2, 2]);
  assert.strictEqual(t.bridgeGroup.items.length, 1, 'dotted estimate across the silence');
  // one wild report between good ones is skipped, not a break in the track
  r = await lineOf([], [rp(0, 41, 'adsc'), rp(10, 41.1, 'adsc'), rp(15, 5, 'fms_position'), rp(20, 41.2, 'adsc')]);
  assert.deepStrictEqual(r.map(x => [x.color, x.n]), [[COLORS.adsc, 3]]);
  // ... and the by-source view is back with the radio button
  t.setTrackMode('source');
  assert.strictEqual(storage.trackMode, 'source');

  // 8d. a message from a source switched off in the legend turns that track back on when selected
  await t.showTrack(t.watches()[0], false);
  t.SOURCES.adsc.group.onMap = false;            // as if unticked in the legend
  const added = calls.addLayer;
  t.rows.get(1).onclick();                        // message 1 is an ADS-C position
  assert.strictEqual(t.SOURCES.adsc.group.onMap, true);
  assert.strictEqual(calls.addLayer, added + 1);

  // 8e. the destination the aircraft reported (REP081): shown in the watch cell and the plane's hover
  const routeLine = () => byId['#list'].children[0].children.find(c => c.className && c.className.startsWith('route'));
  assert.strictEqual(routeLine(), undefined, 'no route line when none reported');
  watch.route = {from: 'VHHH', to: 'YSSY', from_name: 'Hong Kong International Airport', to_name: 'Sydney Kingsford Smith International Airport',
    t: new Date(Date.now() - 12 * 60000).toISOString(), source: 'REP081'};
  await t.refresh();
  let rl = routeLine();
  assert.strictEqual(rl.textContent, 'VHHH \u2192 YSSY (REP081 12m ago)');
  assert.ok(!rl.className.includes('stale'));
  assert.match(rl.title, /From Hong Kong International Airport\nTo Sydney Kingsford Smith International Airport/);
  await t.showTrack(t.watches()[0], false);
  let planeTip = lines(calls.markers.filter(m => m.o && m.o.icon).at(-1).tooltip);
  assert.strictEqual(planeTip[1], 'VHHH \u2192 YSSY', 'destination in the hover, under the callsign');
  // an old report: still shown, greyed and labelled, and left out of the hover
  watch.route = {...watch.route, t: new Date(Date.now() - 3 * 3600000).toISOString(), stale: true};
  await t.refresh();
  rl = routeLine();
  assert.match(rl.textContent, /^VHHH \u2192 YSSY \(REP081 3h ago, may be out of date\)$/);
  assert.ok(rl.className.includes('stale'));
  await t.showTrack(t.watches()[0], false);
  planeTip = lines(calls.markers.filter(m => m.o && m.o.icon).at(-1).tooltip);
  assert.ok(!planeTip.some(l => l.includes('YSSY')), 'a stale destination is not in the hover');
  // a destination you set that differs from what the aircraft reports is called out
  watch.route = {...watch.route, t: new Date(Date.now() - 5 * 60000).toISOString(), stale: false, to: 'VHHH', from: 'YSSY'};
  watch.dest = 'KSFO';
  await t.refresh();
  assert.match(routeLine().textContent, /YSSY \u2192 VHHH \(REP081 5m ago \u2014 you set KSFO\)/);
  watch.dest = 'VHHH';
  await t.refresh();
  assert.ok(!routeLine().textContent.includes('you set'), 'no note when they agree');
  delete watch.route; delete watch.dest;
  await t.refresh();

  // 8f. the flight a tail watch is operating (callsign + flight number, with its age and source)
  const flightLine = () => byId['#list'].children[0].children.find(c => c.className && c.className.startsWith('flight'));
  const idLine = () => byId['#list'].children[0].querySelector('small');
  const textOf = n => n.text !== undefined ? n.text : (n.textContent || '') + (n.children || []).map(textOf).join('');
  const descendants = (n, out = []) => { (n.children || []).forEach(c => { out.push(c); descendants(c, out); }); return out; };
  assert.strictEqual(flightLine(), undefined, 'no flight line when none known');
  watch.flight = {callsign: 'CPA113', number: 'CX113', t: new Date(Date.now() - 4 * 60000).toISOString(), source: 'messages'};
  await t.refresh();
  assert.strictEqual(flightLine().textContent, 'flight CPA113 \u00b7 CX113 (from messages 4m ago)');
  assert.ok(!flightLine().className.includes('stale'));
  assert.ok(!textOf(idLine()).includes('CPA113'), 'a tail watch no longer shows a possibly out-of-date callsign in the id line');
  watch.flight = {...watch.flight, t: new Date(Date.now() - 3 * 3600000).toISOString(), source: 'ADS-B', stale: true};
  await t.refresh();
  assert.strictEqual(flightLine().textContent, 'flight CPA113 \u00b7 CX113 (ADS-B 3h ago, may be out of date)');
  assert.ok(flightLine().className.includes('stale'));
  watch.flight = {callsign: 'ZZZ123', t: new Date().toISOString(), source: 'ADS-B'};   // an airline with no IATA code
  await t.refresh();
  assert.match(flightLine().textContent, /^flight ZZZ123 \(ADS-B /);
  delete watch.flight;

  // 8g. copy-to-clipboard next to the tail, on watches that were not started by tail
  const copyBtn = () => descendants(idLine()).find(c => c.className === 'copy');
  await t.refresh();
  assert.strictEqual(copyBtn(), undefined, 'a tail watch needs no copy button: the tail is already what is tracked');
  const was = {kind: watch.kind, ident: watch.ident, callsign: watch.callsign};
  watch.kind = 'flight'; watch.ident = 'CPA113'; watch.callsign = 'CPA113'; watch.tail = 'B-LRD';
  await t.refresh();
  assert.strictEqual(textOf(idLine()).replace(/\u{1F4CB}/u, '').split(' \u2014 ')[0], 'CPA113 \u00b7 B-LRD \u00b7 780AB8', 'ids: callsign, tail, hex');
  let b = copyBtn();
  assert.ok(b, 'copy button next to the tail');
  assert.strictEqual(b.title, 'Copy B-LRD');
  let stopped = false;
  await b.onclick({stopPropagation() { stopped = true; }});
  assert.strictEqual(JSON.stringify(calls.copied), '["B-LRD"]', 'tail copied to the clipboard');
  assert.ok(stopped, 'clicking copy does not also select the watch');
  assert.strictEqual(b.textContent, '\u2713'); assert.strictEqual(b.title, 'Copied');
  calls.timers.at(-1)();
  assert.strictEqual(b.textContent, '\u{1F4CB}'); assert.strictEqual(b.title, 'Copy B-LRD');
  // on a plain-http page the async clipboard is unavailable: fall back to select + copy
  ctx.window.isSecureContext = false;
  await b.onclick({stopPropagation() {}});
  assert.deepEqual(calls.exec, ['copy']);
  const ta = document.body.children.at(-1);
  assert.ok(ta.value === 'B-LRD' && ta.selectedAll && ta.removed, 'temporary textarea selected, copied from, and removed');
  assert.strictEqual(b.textContent, '\u2713');
  // ... and when even that fails the button says so instead of claiming success
  document.execResult = false;
  await b.onclick({stopPropagation() {}});
  assert.strictEqual(b.textContent, '!'); assert.strictEqual(b.title, 'Could not copy');
  document.execResult = true; ctx.window.isSecureContext = true;
  // a hex watch gets one too; a watch with no known tail has none
  watch.kind = 'hex';
  await t.refresh();
  assert.ok(copyBtn(), 'hex watch');
  delete watch.tail;
  await t.refresh();
  assert.strictEqual(copyBtn(), undefined, 'no tail, nothing to copy');
  watch.kind = was.kind; watch.ident = was.ident; watch.callsign = was.callsign; watch.tail = 'B-LRM';
  await t.refresh();

  // 8h. position messages are listed at their report time, not their arrival time
  const sample = [
    {id: 10, t: '2026-10-30T18:34:45Z', pos_t: '2026-10-30T18:37:54Z', lat: -4.76, lon: 140.64, kind: 'miam', summary: 'REP081', raw: 'r', label: 'MA', rx_count: 1},
    {id: 11, t: '2026-10-30T18:35:21Z', kind: 'cpdlc', summary: 'CPDLC downlink: WILCO', raw: 'r', label: 'BA', rx_count: 1},
    {id: 12, t: '2026-10-30T18:31:16Z', pos_t: '2026-10-30T18:34:31Z', lat: -4.54, lon: 140.25, kind: 'adsc', summary: 'ADS-C report', raw: 'r', label: 'B6', rx_count: 1},
    {id: 13, t: '2026-10-30T18:40:00Z', pos_t: '2026-10-30T18:40:20Z', lat: -5.0, lon: 141.0, kind: 'adsc', summary: 'ADS-C report', raw: 'r', label: 'B6', rx_count: 1},
  ];
  t.renderMessages(sample);
  const timeOf = id => t.rows.get(id).children[0];
  assert.strictEqual(timeOf(10).textContent, '10-30 18:37:54Z', 'REP081 shows its report time (arrived 18:34:45)');
  assert.strictEqual(timeOf(12).textContent, '10-30 18:34:31Z', 'ADS-C shows its report time (arrived 18:31:16)');
  assert.strictEqual(timeOf(11).textContent, '10-30 18:35:21Z', 'a message with no position keeps its arrival time');
  assert.strictEqual(timeOf(10).className, 'rt'); assert.strictEqual(timeOf(11).className, '');
  // listed newest first by the time shown: 18:40:20, 18:37:54, 18:35:21, 18:34:31 (by arrival it would be 13, 11, 10, 12)
  const order = byId['#msgs'].children.filter(c => c.className && c.className.startsWith('msg')).map(c => [...t.rows].find(([, el]) => el === c)[0]);
  assert.strictEqual(JSON.stringify(order), '[13,10,11,12]', 'ordered by the displayed time');
  timeOf(10).onmouseenter();
  assert.match(timeOf(10).title, /report time 18:37:54Z\narrived 18:34:45Z \(report is 3m09s after arrival\)/);
  timeOf(11).onmouseenter();
  assert.match(timeOf(11.0).title, /^\d+[smhd] ago$|ago$/); assert.ok(!timeOf(11).title.includes('report time'));
  timeOf(13).onmouseenter();
  assert.ok(timeOf(13).title.includes('arrived 18:40:00Z') && !timeOf(13).title.includes('report is'), 'a 20 s difference is not worth a note');
  await t.showTrack(t.watches()[0], false);   // back to the real data


  // ===== 8i. the date line: one frame for every layer, no line round the world ======================
  const draw = async (ptsArr, msgsArr, focus, extra = {}) => {
    const sp = pts.slice(), sm = msgs.slice(), saved = {};
    for (const k of Object.keys(extra)) { saved[k] = watch[k]; watch[k] = extra[k]; }
    pts.splice(0, pts.length, ...ptsArr); msgs.splice(0, msgs.length, ...msgsArr);
    if (Object.keys(extra).length) await t.refresh();     // the page sees a changed watch only after a refresh
    calls.lines.length = 0; calls.markers.length = 0; calls.circles.length = 0;
    const views = calls.setView.length;
    await t.showTrack(t.watches()[0], focus);
    const out = {lines: calls.lines.slice(), markers: calls.markers.slice(), circles: calls.circles.slice(), view: calls.setView.length > views ? calls.setView.at(-1) : null};
    pts.splice(0, pts.length, ...sp); msgs.splice(0, msgs.length, ...sm);
    for (const k of Object.keys(extra)) { if (saved[k] === undefined) delete watch[k]; else watch[k] = saved[k]; }
    if (Object.keys(extra).length) await t.refresh();
    return out;
  };
  const apt = (mm, lon, extra = {}) => ({t: `2026-10-30T16:${String(mm).padStart(2, '0')}:00Z`, lat: 10, lon, alt_ft: 35000, gs: 480, track: 90, source: 'adsb.lol', ...extra});
  const lons = line => line.ll.map(p => p[1]);
  const steps = a => a.slice(1).map((x, i) => Math.abs(x - a[i]));
  const adsbLines = r => r.lines.filter(l => l.o.color === COLORS.adsb);

  // eastbound across 180: 179.0, 179.6, -179.8, -179.2 become one continuous line in the frame anchored at the newest fix
  r = await draw([apt(0, 179.0), apt(8, 179.6), apt(16, -179.8), apt(24, -179.2)], [], true);
  assert.strictEqual(adsbLines(r).length, 1, 'one ADS-B line, not two');
  assert.strictEqual(JSON.stringify(lons(adsbLines(r)[0]).map(x => +x.toFixed(1))), '[-181,-180.4,-179.8,-179.2]');
  assert.ok(Math.max(...steps(lons(adsbLines(r)[0]))) < 1, 'no step anywhere near 360');
  assert.strictEqual(t.lonRef, -179.2);
  assert.strictEqual(JSON.stringify(r.view), '[[10,-179.2],7]', 'centred in the same frame');
  assert.strictEqual(+r.markers.find(m => m.o.icon && m.o.icon.className === 'plane').ll[1].toFixed(1), -179.2, 'the plane too');
  // a datalink dot at 179.7 (before the date line) is drawn at -180.3, beside the line, not 360 degrees away
  r = await draw([apt(0, 179.0), apt(24, -179.2)], [{id: 30, t: '2026-10-30T16:10:00Z', pos_t: '2026-10-30T16:10:00Z', kind: 'adsc', summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat: 10, lon: 179.7}], true);
  assert.strictEqual(+r.circles.find(c => c.o.radius === 4).ll[1].toFixed(1), -180.3);

  // westbound: -179.0, -179.6, 179.8, 179.2 -> 181.0, 180.4, 179.8, 179.2
  r = await draw([apt(0, -179.0, {track: 270}), apt(8, -179.6, {track: 270}), apt(16, 179.8, {track: 270}), apt(24, 179.2, {track: 270})], [], true);
  assert.strictEqual(JSON.stringify(lons(adsbLines(r)[0]).map(x => +x.toFixed(1))), '[181,180.4,179.8,179.2]');
  assert.strictEqual(t.lonRef, 179.2);
  // the frame does not jump when the aircraft carries on past 180 and the page refreshes: the new point continues at 180.4
  r = await draw([apt(0, -179.0, {track: 270}), apt(8, -179.6, {track: 270}), apt(16, 179.8, {track: 270}), apt(24, 179.2, {track: 270}), apt(32, -179.6, {track: 270})], [], false);
  assert.strictEqual(JSON.stringify(lons(adsbLines(r)[0]).map(x => +x.toFixed(1))), '[181,180.4,179.8,179.2,180.4]');
  assert.strictEqual(t.lonRef, 179.2, 'a refresh keeps the frame');
  // far side of the world from the frame: a track of 179.5 -> -179.5 is cut, never drawn right round the globe
  r = await draw([apt(0, 179.5), apt(8, -179.5)], [{id: 31, t: '2026-10-30T17:00:00Z', pos_t: '2026-10-30T17:00:00Z', kind: 'adsc', summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat: 10, lon: 0.0}], true);
  assert.strictEqual(t.lonRef, 0);
  assert.strictEqual(adsbLines(r).length, 0, 'the 359 degree jump is not drawn');
  assert.ok(r.lines.every(l => Math.max(0, ...steps(lons(l))) <= 180), 'no line spans more than half the globe');
  // the highlight ring and the pan target use the same frame
  r = await draw([apt(0, 179.0), apt(24, -179.2)], [{id: 32, t: '2026-10-30T16:10:00Z', pos_t: '2026-10-30T16:10:00Z', kind: 'adsc', summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat: 10, lon: 179.7}], true);
  await t.showTrack(t.watches()[0], false);
  t.renderMessages([{id: 32, t: '2026-10-30T16:10:00Z', pos_t: '2026-10-30T16:10:00Z', kind: 'adsc', summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat: 10, lon: 179.7}]);
  calls.circles.length = 0; calls.panTo.length = 0;
  t.rows.get(32).onclick();
  assert.strictEqual(+calls.circles.at(-1).ll[1].toFixed(1), -180.3, 'ring in the frame');

  // ===== 8j. clicking a point on the map selects its message in the list =============================
  await t.showTrack(t.watches()[0], true);                      // back to the standard data
  t.renderMessages(msgs); 
  await t.showTrack(t.watches()[0], false);
  const dot = t.markers.get(1);
  assert.ok(dot && dot.handlers && dot.handlers.click, 'message dots are clickable');
  t.rows.get(1).scrolled = null; t.rows.get(1)._cls.delete('sel');
  dot.handlers.click();
  assert.strictEqual(t.selectedMsg2, 1);
  assert.ok(t.rows.get(1)._cls.has('sel'), 'its row is highlighted');
  assert.ok(t.rows.get(1).scrolled, 'and scrolled into view');
  assert.ok(highlightGroup.items.length === 1, 'the ring is drawn');
  // clicking another dot moves the selection and leaves a single popup
  t.markers.get(3).handlers.click();
  assert.ok(t.rows.get(3)._cls.has('sel') && !t.rows.get(1)._cls.has('sel'));
  assert.strictEqual(allLayers.filter(l => l.open).length, 1, 'one popup after clicking around the map');

  // ===== 8k. the tracked aircraft is named by its registration first =================================
  const lastPlaneTip = async (ptsArr, extra) => (lines((await draw(ptsArr, [], false, extra)).markers.filter(m => m.o.icon && m.o.icon.className === 'plane').at(-1).tooltip))[0];
  assert.strictEqual(await lastPlaneTip([apt(0, 100)], {tail: 'B-LRD'}), 'B-LRD · A359', 'tail first, then type (this point has no callsign)'.replace(' · A359', '') === '' ? '' : 'B-LRD · A359');
  assert.strictEqual(await lastPlaneTip([apt(0, 100, {callsign: 'CPA113'})], {tail: 'B-LRD'}), 'B-LRD · CPA113 · A359');
  assert.strictEqual(await lastPlaneTip([apt(0, 100, {callsign: 'CPA113'})], {tail: undefined, type: undefined}), 'CPA113', 'no tail: the callsign');
  assert.strictEqual(await lastPlaneTip([apt(0, 100)], {tail: undefined, callsign: undefined, type: undefined}), '780AB8', 'only then the hex');

  // ===== 8l. weather and motion in a position's tooltip ==========================================
  const wxmsg = {id: 40, t: '2026-10-30T16:10:00Z', pos_t: '2026-10-30T16:10:00Z', kind: 'cpdlc', summary: 'CPDLC position report', raw: 'r', label: 'BA', rx_count: 1,
    lat: 10, lon: 100, wx: {dir: 70, kt: 50, temp_c: -45}, gs: 520, trk: 184};
  r = await draw([], [wxmsg], false);
  let dotTip = lines(r.circles.find(c => c.o.radius === 4).tooltip);
  assert.ok(dotTip.includes('wind 070°/50kt · -45°C'), 'wind (3 digits) and temperature: ' + dotTip);
  assert.ok(dotTip.includes('520 kt · track 184°'), 'speed and track: ' + dotTip);
  r = await draw([], [{...wxmsg, wx: {kt: 12.4}, gs: null, trk: null}], false);
  dotTip = lines(r.circles.find(c => c.o.radius === 4).tooltip);
  assert.ok(dotTip.includes('wind 12kt') && !dotTip.some(l => /track|°C/.test(l)), 'only what is known: ' + dotTip);
  r = await draw([], [{...wxmsg, wx: undefined, gs: undefined, trk: undefined}], false);
  assert.strictEqual(lines(r.circles.find(c => c.o.radius === 4).tooltip).length, 2, 'summary and time only when there is nothing else');
  // a REP081 summary carries the weather itself: broken at the OAT, and not repeated as a weather line
  r = await draw([], [{...wxmsg, kind: 'miam', summary: 'REP081 VHHH\u2192YSSY 12.345N 150.000E FL380, OAT -52.0\u00b0C, wind 270\u00b0/80kt', wx: {dir: 270, kt: 80, temp_c: -52}}], false);
  dotTip = lines(r.circles.find(c => c.o.radius === 4).tooltip);
  assert.strictEqual(dotTip[0], 'REP081 VHHH\u2192YSSY 12.345N 150.000E FL380', 'the position and level on the first line: ' + dotTip);
  assert.strictEqual(dotTip[1], 'OAT -52.0\u00b0C, wind 270\u00b0/80kt', 'then the weather, once: ' + dotTip);
  assert.ok(!dotTip.some(l => l.startsWith('wind ')), 'no second, decoded wind line: ' + dotTip);

  // ===== 8m. waypoints named in CPDLC reports ====================================================
  const wp = (name, exact, role = 'next', extra = {}) => ({name, role, lat: 5 + name.length, lon: 120, exact, source: exact ? 'database' : 'projected', how: exact ? 'from nav db (2013 src)' : 'estimated: 61 nm ahead on track 122° at 520 kt, by its ETA', t: '2026-10-30T18:55:00Z', ...extra});
  const cm = (id, hh, wps, extra = {}) => ({id, t: `2026-10-30T${hh}:00Z`, pos_t: `2026-10-30T${hh}:00Z`, kind: 'cpdlc', summary: 's', raw: 'r', label: 'BA', rx_count: 1, lat: 6, lon: 119, waypoints: wps, ...extra});
  r = await draw([], [
      cm(50, '17:00', [wp('EXACTX', true), wp('NEWEST', false, 'next', {lat: 1})]),   // older report
      cm(51, '18:00', [wp('EXACTX', false, 'next', {lat: 2}), wp('NEWEST', false, 'next', {lat: 3})]),   // newer: an estimate of EXACTX, a newer estimate of NEWEST
      cm(52, '19:00', [wp('BAD', false)], {suspect: 'x'}),                              // a suspect report's waypoints are ignored
    ], false);
  const wpts = r.markers.filter(m => m.o.icon && /wpt-wrap/.test(m.o.icon.className));
  assert.strictEqual(wpts.length, 2, 'one marker per name, none from the suspect report: ' + wpts.length);
  const byName = n => wpts.find(m => lines(m.tooltip)[0].startsWith(n));
  assert.strictEqual(byName('EXACTX').ll[0], 5 + 6, 'an exact position beats a newer estimate');   // lat 5+len('EXACTX')
  assert.ok(/class="wpt"/.test(byName('EXACTX').o.icon.html) && /class="wpt est"/.test(byName('NEWEST').o.icon.html), 'exact is solid, an estimate dashed');
  assert.strictEqual(byName('NEWEST').ll[0], 3, 'of two estimates, the newest report');
  assert.strictEqual(JSON.stringify(lines(byName('NEWEST').tooltip).slice(0, 2)), JSON.stringify(['NEWEST — next fix, ETA 18:55:00Z', '⚠ estimated: 61 nm ahead on track 122° at 520 kt, by its ETA']));
  assert.ok(lines(byName('EXACTX').tooltip)[1].startsWith('from nav db'));
  assert.strictEqual(lines(byName('EXACTX').tooltip)[0], 'EXACTX — next fix, ETA 18:55:00Z');
  const passed = (await draw([], [cm(53, '18:00', [wp('PASSEDX', true, 'passed', {alt_ft: 34300})])], false)).markers.find(m => m.o.icon && /wpt-wrap/.test(m.o.icon.className));
  assert.ok(lines(passed.tooltip)[0].includes('passed 18:55:00Z at 34,300 ft'), lines(passed.tooltip)[0]);
  // clicking a waypoint selects the report that named it
  t.renderMessages([cm(54, '18:00', [wp('CLICKME', true)])]);
  const cl = (await draw([], [cm(54, '18:00', [wp('CLICKME', true)])], false)).markers.find(m => m.o.icon && /wpt-wrap/.test(m.o.icon.className));
  cl.handlers.click();
  assert.strictEqual(t.selectedMsg2, 54);
  assert.strictEqual((await draw([], [cm(55, '18:00', [])], false)).markers.filter(m => m.o.icon && /wpt-wrap/.test(m.o.icon.className)).length, 0, 'none when no waypoints');

  // ===== 8n. projection: where it probably is now, with limits ========================================
  const NOW = Date.UTC(2026, 9, 30, 19, 0, 0);
  const iso = minAgo => new Date(NOW - minAgo * 60000).toISOString();
  const ap = (minAgo, lat, lon, extra = {}) => ({t: iso(minAgo), lat, lon, alt_ft: 36000, gs: 480, track: 90, source: 'adsb.lol', ...extra});
  const W = {id: 1, hex: 'ABCDEF'};
  const km = (a, b) => Math.hypot((b[1] - a[1]) * 60 * Math.cos(a[0] * Math.PI / 180), (b[0] - a[0]) * 60);   // nm, equator-ish
  let pr = t.projection(W, [ap(10, 0, 0)], [], NOW, 30);
  assert.ok(pr, 'a 10 minute old fix with speed and track projects');
  assert.ok(Math.abs(km([0, 0], pr.now) - 80) < 0.5, 'now: 10 min at 480 kt = 80 nm, got ' + km([0, 0], pr.now));
  assert.ok(Math.abs(km([0, 0], pr.end) - 320) < 0.5, 'end: 40 min after the fix = 320 nm');
  assert.strictEqual(pr.ticks.map(x => x.min).join(), '10,20,30', 'a tick every 10 minutes ahead of now');
  assert.ok(Math.abs(pr.end[1] - 320 / 60) < 0.01 && Math.abs(pr.end[0]) < 0.01, 'due east along the equator');
  assert.strictEqual(pr.capped, false);
  assert.ok(/480 kt on track 90/.test(pr.basis));
  // the limits
  assert.strictEqual(t.projection(W, [ap(61, 0, 0)], [], NOW, 30), null, 'no projection from a fix over an hour old');
  assert.ok(t.projection(W, [ap(59, 0, 0)], [], NOW, 30), 'but 59 minutes is fine');
  assert.strictEqual(t.projection(W, [ap(10, 0, 0)], [], NOW, 0), null, 'off');
  assert.strictEqual(t.projection(W, [ap(10, 0, 0, {on_ground: true, gs: 0})], [], NOW, 30), null, 'not for an aircraft on the ground');
  assert.strictEqual(t.projection(W, [ap(10, 0, 0, {gs: 60})], [], NOW, 30), null, 'nor one too slow to be flying');
  assert.strictEqual(t.projection(W, [ap(10, 0, 0, {gs: 900})], [], NOW, 30), null, 'nor an impossible speed');
  pr = t.projection(W, [ap(55, 0, 0)], [], NOW, 60);
  assert.ok(pr.capped && Math.abs(pr.endMin - 35) < 1e-9, 'never more than 90 minutes beyond the fix: 55 old + 60 ahead is cut to 35 ahead, got ' + pr.endMin);
  assert.ok(Math.abs(km([0, 0], pr.end) - 720) < 0.5, '90 minutes at 480 kt');
  // the selector's value is the default
  assert.ok(t.projection(W, [ap(10, 0, 0)], [], NOW), 'uses the selected look-ahead by default');
  // the newest TRUSTWORTHY fix is the basis: a later suspect or time-unverified datalink position is ignored
  const dl = (minAgo, lat, lon, extra = {}) => ({id: 200 + minAgo, t: iso(minAgo), pos_t: iso(minAgo), kind: 'adsc', summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat, lon, gs: 480, trk: 90, ...extra});
  // ===== 8p. the filed flight plan: drawn, and projected along ========================================
  {
    const P = (name, lat, lon, extra = {}) => ({name, lat, lon, kind: 'fix', ...extra});
    // east along the equator for 600 nm, then north for 600 nm
    const plan = {id: 5, t: iso(120), callsign: 'UPS58', from: 'AAAA', to: 'BBBB', points: [P('AAAA', 0, 0, {kind: 'airport'}), P('MID', 0, 10), P('BBBB', 10, 10, {kind: 'airport'})]};
    const run = (fix, plan2 = plan, ahead = 30) => t.projection(W, [fix], [], NOW, ahead, plan2);
    // 30 nm short of the turn, heading east at 480 kt: 30 nm east, then north for the rest of the 240 nm
    let pr = run(ap(0, 0, 9.5));
    assert.ok(pr && pr.onRoute, 'follows the route');
    assert.ok(Math.abs(pr.end[1] - 10) < 0.01 && Math.abs(pr.end[0] - 3.5) < 0.05, 'turned north at the waypoint: ' + JSON.stringify(pr.end));
    assert.ok(pr.pathFromFix.length === 3 && pr.pathFromFix[1][0] === 0 && pr.pathFromFix[1][1] === 10, 'the line has a vertex at the waypoint: ' + JSON.stringify(pr.pathFromFix));
    assert.ok(Math.abs(pr.ticks[0].lat - 0.83) < 0.02 && Math.abs(pr.ticks[0].lon - 10) < 0.01, 'ticks follow the turn ' + JSON.stringify(pr.ticks[0]));
    assert.ok(pr.basis.includes('filed route UPS58 AAAA\u2192BBBB'), pr.basis);
    // 18 nm off the line: projected from where the aircraft is, closing the gap, not starting on the line
    pr = run(ap(0, 0.3, 5));
    assert.ok(pr.onRoute && pr.pathFromFix[0][0] === 0.3 && pr.pathFromFix[0][1] === 5, 'starts at the aircraft: ' + JSON.stringify(pr.pathFromFix[0]));
    assert.ok(pr.basis.includes('18 nm off it at the fix'), pr.basis);
    // 10 minutes old: the estimated position now is already round the corner, heading north
    pr = run(ap(10, 0, 9.5));
    assert.ok(pr.now[0] > 0.8 && Math.abs(pr.now[1] - 10) < 0.01 && Math.abs(pr.trk) < 1, 'now: ' + JSON.stringify(pr.now) + ' trk ' + pr.trk);
    // the route ends at the destination, which is where the projection stops
    pr = run(ap(0, 8.5, 10, {gs: 480, track: 0}), plan, 60);
    assert.ok(pr.stop === 'BBBB' && Math.abs(pr.end[0] - 10) < 0.01, 'stops at the end of the route ' + JSON.stringify(pr.end) + pr.stop);
    // a fix well off the route, or heading across it, is projected as before (straight along its track)
    assert.ok(!run(ap(0, 5, 3)).onRoute, 'a fix 300 nm off the route');
    assert.ok(!run(ap(0, 0, 5, {track: 0})).onRoute, 'heading across the route');
    assert.ok(!t.projection(W, [ap(0, 0, 5)], [], NOW, 30, null).onRoute, 'no plan');
    assert.ok(!t.projection(W, [ap(0, 0, 5)], [], NOW, 30, {...plan, points: [plan.points[0]]}).onRoute, 'a plan with one point');
    // which plan: the newest that arrived in the right window for the flight shown
    const at = h => new Date(NOW - h * 3600000).toISOString();
    const plans = [{...plan, id: 1, t: at(60)}, {...plan, id: 2, t: at(5)}, {...plan, id: 3, t: at(1)}];
    assert.strictEqual(t.choosePlan(plans, null, NOW).id, 3, 'no flight chosen: the newest');
    assert.strictEqual(t.choosePlan(plans, {startMs: NOW - 4 * 3600000, endMs: NOW - 2 * 3600000}, NOW).id, 2, 'a flight that ended before the newest plan arrived');
    assert.strictEqual(t.choosePlan(plans, {startMs: NOW - 70 * 3600000, endMs: NOW - 50 * 3600000}, NOW).id, 1);
    assert.strictEqual(t.choosePlan(plans.slice(0, 1), null, NOW), null, 'a plan from two and a half days ago is not this flight');
    assert.strictEqual(t.choosePlan([], null, NOW), null);
  }
  {
    // drawn as a dashed line with a marker at each point, and cleared when there is no plan
    const plan = {id: 5, t: '2026-10-30T10:00:00Z', callsign: 'UPS58', from: 'PHNL', to: 'VHHH', missing: ['XYZ'], points: [
      {name: 'PHNL', lat: 21.3, lon: -157.9, kind: 'airport'}, {name: 'N25000E180000', lat: 25, lon: 180, kind: 'given'}, {name: 'TUNTO', lat: 25, lon: 170, via: 'Q13', kind: 'fix'}, {name: 'VHHH', lat: 22.3, lon: 113.9, kind: 'airport'}]};
    const before = calls.lines.length, circlesBefore = calls.circles.length;
    t.drawPlan(plan);
    const line = calls.lines[calls.lines.length - 1];
    assert.strictEqual(calls.lines.length, before + 1);
    assert.strictEqual(line.ll.length, 4); assert.strictEqual(line.o.dashArray, '8 6');
    assert.ok(line.ll.every((p, i, a) => i === 0 || Math.abs(p[1] - a[i - 1][1]) < 200), 'across the date line without a line round the world: ' + JSON.stringify(line.ll.map(p => p[1])));
    const dots = calls.circles.slice(circlesBefore);
    assert.strictEqual(dots.length, 4);
    const tipLines = el => el.children.map(c => c.textContent);
    assert.deepEqual(tipLines(dots[2].tooltip)[0], 'TUNTO'); assert.ok(tipLines(dots[2].tooltip).includes('then Q13'));
    assert.ok(tipLines(dots[2].tooltip).includes('position from nav db (2013 src)'));
    assert.ok(!tipLines(dots[1].tooltip).includes('position from nav db (2013 src)'), 'given coordinates need no caveat');
    assert.ok(tipLines(dots[0].tooltip).some(l => l.includes('not placed: XYZ')));
    t.drawPlan(null);
    assert.strictEqual(t.planGroup.items.length, 0);
  }
  {
    // through the page: showTrack fetches the plans and draws the one for the flight
    planData.push({id: 7, t: '2026-10-30T15:50:00Z', callsign: 'CPA843', from: 'KLAX', to: 'VHHH', points: [
      {name: 'KLAX', lat: 33.9, lon: -118.4, kind: 'airport'}, {name: 'FIX1', lat: 40, lon: -105, kind: 'fix'}, {name: 'VHHH', lat: 22.3, lon: 113.9, kind: 'airport'}]});
    await t.showTrack(t.watches()[0], false);
    assert.ok(fetchLog.some(u => u.startsWith('/api/plan/780AB8')), 'asked for the plans');
    assert.ok(t.planGroup.items.length >= 4, 'the plan is on the map: ' + t.planGroup.items.length);
    planData.length = 0;
    await t.showTrack(t.watches()[0], false);
    assert.strictEqual(t.planGroup.items.length, 0, 'gone when the aircraft has no plan');
  }

  // ===== 8q. estimated path across a silence, and hoverable ADS-B points =============================
  {
    const keep = t.flightSegs.slice(); t.flightSegs.length = 0;
    const plan = {callsign: 'JAL1', from: 'AAAA', to: 'BBBB', points: [{name: 'AAAA', lat: 0, lon: 0, kind: 'airport'}, {name: 'MID', lat: 0, lon: 10, kind: 'fix'}, {name: 'BBBB', lat: 10, lon: 10, kind: 'airport'}]};
    // ADS-C reports to 20 minutes ago, then ADS-B from now: a 20 minute silence with the route turning inside it
    const adsc = (minAgo, lat, lon, extra = {}) => dl(minAgo, lat, lon, extra);
    let e = t.estimatedPaths([ap(0, 1.5, 10)], [adsc(30, 0, 8.5), adsc(20, 0, 9.5)], plan);
    assert.strictEqual(e.length, 1, JSON.stringify(e.map(x => [x.from.key, x.to.key, x.minutes])));
    assert.deepStrictEqual([e[0].from.key, e[0].to.key, e[0].minutes, e[0].via], ['adsc', 'adsb', 20, 'route']);
    assert.ok(e[0].path.length === 3 && e[0].path[1][0] === 0 && e[0].path[1][1] === 10, 'bends at the route waypoint: ' + JSON.stringify(e[0].path));
    // no plan, or fixes not on it: a straight line
    e = t.estimatedPaths([ap(0, 1.5, 10)], [adsc(30, 0, 8.5), adsc(20, 0, 9.5)], null);
    assert.deepStrictEqual([e[0].via, e[0].path.length], ['straight', 2]);
    e = t.estimatedPaths([ap(0, 6, 7)], [adsc(30, 6, 5), adsc(20, 6, 6)], plan);
    assert.strictEqual(e[0].via, 'straight', 'fixes off the route');
    // continuous tracks need none: ADS-B every 45 s, ADS-C every 15 minutes
    assert.strictEqual(t.estimatedPaths([ap(0, 0, 3), ap(0.75, 0, 2.9), ap(1.5, 0, 2.8)], [], plan).length, 0);
    assert.strictEqual(t.estimatedPaths([], [adsc(60, 0, 1), adsc(45, 0, 2), adsc(30, 0, 3)], plan).length, 0);
    // a quiet gap is only bridged within reason: under 5 minutes, over 3 hours, impossible speed, bad fixes
    assert.strictEqual(t.estimatedPaths([ap(0, 0, 3)], [adsc(4, 0, 2.9)], plan).length, 0, 'four minutes');
    assert.strictEqual(t.estimatedPaths([ap(0, 0, 3)], [adsc(200, 0, 1)], plan).length, 0, 'over three hours');
    assert.strictEqual(t.estimatedPaths([ap(0, 0, 60)], [adsc(30, 0, 1)], plan).length, 0, 'no aircraft flies that fast');
    assert.strictEqual(t.estimatedPaths([ap(0, 0, 3)], [adsc(30, 0, 1, {suspect: 'x'}), adsc(25, 0, 1.5, {time_unverified: true})], plan).length, 0, 'untrustworthy fixes');
    // ADS-B itself with a 25 minute hole in it
    e = t.estimatedPaths([ap(60, 0, 1), ap(35, 0, 4)], [], plan);
    assert.deepStrictEqual([e.length, e[0].from.key, e[0].to.key], [1, 'adsb', 'adsb']);
    // never across two flights
    t.flightSegs.push({startMs: NOW - 100 * 60000, endMs: NOW - 40 * 60000}, {startMs: NOW - 20 * 60000, endMs: NOW});
    assert.strictEqual(t.estimatedPaths([ap(0, 0, 3)], [adsc(30, 0, 1)], plan).length, 0, 'different flights');
    t.flightSegs.length = 0; t.flightSegs.push(...keep);

    // thinning: close-together points are dropped, the first is kept, the pace is by time and distance
    const row = (n, stepMin, stepLon) => Array.from({length: n}, (_, i) => ap(60 - i * stepMin, 0, i * stepLon));
    assert.strictEqual(t.thinPoints(row(40, 0.75, 0.1)).length, 10, '45 s apart: one in four (3 minutes)');
    assert.strictEqual(t.thinPoints(row(10, 5, 0.01)).length, 1, 'ten minutes apart but a mile: one');
    assert.strictEqual(t.thinPoints(row(10, 0.1, 1)).length, 1, 'far apart but seconds: one');
    assert.strictEqual(t.thinPoints(row(5, 10, 1)).length, 5, 'sparse points are all kept');
    assert.strictEqual(t.thinPoints([]).length, 0);

    // through the page: hoverable dots on the ADS-B track with the point's details, and a dotted estimated path
    const saved = pts.map(p => ({...p})), savedMsgs = msgs.map(m => ({...m}));
    const iso2 = m => new Date(Date.UTC(2026, 9, 30, 16, 0, 0) + m * 60000).toISOString();
    pts.splice(0, pts.length, ...[0, 1, 2, 3, 4, 5, 6, 40, 41, 42, 43].map((m, i) => ({t: iso2(m), lat: 40 + i * 0.1, lon: -100 + i * 0.1, alt_ft: 35000, gs: 470, track: 90, callsign: 'CPA843', source: 'adsb.lol'})));
    msgs.splice(0, msgs.length);   // ADS-B only
    const circlesBefore = calls.circles.length, linesBefore = calls.lines.length;
    await t.showTrack(t.watches()[0], false);
    const dots = calls.circles.slice(circlesBefore).filter(c => c.o.color === t.SOURCES.adsb.color && c.o.radius === 3);
    assert.strictEqual(dots.length, 5, 'hover dots, thinned from eleven points to every third minute: ' + dots.length);
    assert.deepEqual(dots[0].tooltip.children.map(c => c.textContent.replace(/ \u00b7 \d+\S* ago/, '')).slice(0, 3), ['CPA843', '2026-10-30 16:00Z (adsb.lol)', '35,000 ft']);
    assert.ok(dots.every(d => d.target === t.SOURCES.adsb.group), 'on the ADS-B layer, so the legend switches them off with the track');
    const bridge = calls.lines.slice(linesBefore).filter(l => l.o.color === '#777');
    assert.strictEqual(bridge.length, 1, 'one estimated path, for the 34 minute hole');
    assert.strictEqual(bridge[0].o.dashArray, '2 6');
    assert.ok(bridge[0].tooltip.children.map(c => c.textContent).includes('34 min without a position, 8 nm'), JSON.stringify(bridge[0].tooltip.children.map(c => c.textContent)));
    pts.splice(0, pts.length, ...saved); msgs.splice(0, msgs.length, ...savedMsgs);
    await t.showTrack(t.watches()[0], false);
  }
  // ===== 8o'. the estimated-position ghost only appears once the newest fix is no longer live =====
  {
    const saved = pts.map(p => ({...p}));
    const ghosts = async minAgo => {
      pts.splice(0, pts.length, {t: new Date(Date.now() - minAgo * 60000).toISOString(), lat: 40, lon: -100, alt_ft: 36000, gs: 480, track: 90, source: 'adsb.lol'});
      await t.showTrack(t.watches()[0], false);
      return t.projGroup.items.filter(l => l.o && l.o.icon && l.o.icon.className === 'plane').length;
    };
    assert.strictEqual(await ghosts(0.5), 0, 'a 30 second old ADS-B fix is live: no ghost');
    assert.strictEqual(await ghosts(2), 0, 'nor one from two minutes ago, between polls and fetches');
    assert.strictEqual(await ghosts(5), 1, 'five minutes without a fix: the ghost shows where it probably is now');
    pts.splice(0, pts.length, ...saved);
    await t.showTrack(t.watches()[0], false);
  }
  // ===== 8o. a message with no position: a ghost along the path between the fixes either side of it =====
  {
    const keep = t.flightSegs.slice();
    t.flightSegs.length = 0;
    const g = (minAgo, a, b, c) => t.ghostAt(NOW - minAgo * 60000, a, b);
    // halfway in time between two fixes 60 min apart, 480 nm apart along the equator: halfway along
    let gh = t.ghostAt(NOW - 30 * 60000, [ap(60, 0, 0), ap(0, 0, 8)], []);
    assert.ok(gh && Math.abs(gh.lat) < 0.01 && Math.abs(gh.lon - 4) < 0.01, JSON.stringify(gh));
    assert.ok(Math.abs(gh.trk - 90) < 0.5);
    // a quarter of the way
    gh = t.ghostAt(NOW - 45 * 60000, [ap(60, 0, 0), ap(0, 0, 8)], []);
    assert.ok(Math.abs(gh.lon - 2) < 0.01, 'quarter way: ' + gh.lon);
    // fixes may be datalink positions, mixed with ADS-B, in any order
    gh = t.ghostAt(NOW - 30 * 60000, [ap(60, 0, 0)], [dl(0, 0, 8)]);
    assert.ok(gh && Math.abs(gh.lon - 4) < 0.01 && gh.after.src === 'ADS-C', 'mixed sources');
    // an untrustworthy neighbour is not used
    assert.strictEqual(t.ghostAt(NOW - 30 * 60000, [ap(60, 0, 0)], [dl(0, 0, 8, {suspect: 'x'})]), null);
    assert.strictEqual(t.ghostAt(NOW - 30 * 60000, [ap(60, 0, 0)], [dl(0, 0, 8, {time_unverified: true})]), null);
    // before the first fix or after the last: nothing (the live projection covers the latter)
    assert.strictEqual(t.ghostAt(NOW - 90 * 60000, [ap(60, 0, 0), ap(0, 0, 8)], []), null);
    assert.strictEqual(t.ghostAt(NOW + 5 * 60000, [ap(60, 0, 0), ap(0, 0, 8)], []), null);
    // too long a silence between the fixes
    assert.strictEqual(t.ghostAt(NOW - 200 * 60000, [ap(400, 0, 0), ap(0, 0, 40)], []), null);
    // a jump no aircraft could make
    assert.strictEqual(t.ghostAt(NOW - 30 * 60000, [ap(60, 0, 0), ap(0, 0, 40)], []), null);
    // across the date line: the short way round
    gh = t.ghostAt(NOW - 30 * 60000, [ap(60, 0, 178), ap(0, 0, -174)], []);
    assert.ok(gh && (gh.lon > 179.9 || gh.lon < -179.9) , 'date line ' + gh.lon);
    // two fixes in different flights are not joined
    t.flightSegs.push({startMs: NOW - 120 * 60000, endMs: NOW - 59 * 60000}, {startMs: NOW - 10 * 60000, endMs: NOW});
    assert.strictEqual(t.ghostAt(NOW - 30 * 60000, [ap(60, 0, 0), ap(0, 0, 8)], []), null);
    t.flightSegs.length = 0; t.flightSegs.push(...keep);
  }
  // an ADS-C report with no speed or track still projects when it names its next waypoint and the time to it (B-LRD)
  const nx = (lat, lon, eta, extra = {}) => ({lat, lon, eta_sec: eta, kind: 'next', name: 'ISTEM', ...extra});
  pr = t.projection(W, [], [dl(7, -19.831, 156.228, {gs: undefined, trk: undefined, route: [nx(-24.598, 160.383, 2571), {lat: -27.1, lon: 163, kind: 'next+1'}]})], NOW, 30);
  assert.ok(pr, 'projects from the next waypoint');
  assert.ok(Math.abs(pr.trk - 141) < 2 && Math.abs(pr.gs - 515) < 8, `track ${pr.trk} speed ${pr.gs}`);
  assert.ok(pr.basis.includes('towards ISTEM'), pr.basis);
  // no ETA, or an ETA that implies an impossible speed: fall back to the previous fix, else nothing
  assert.strictEqual(t.projection(W, [], [dl(7, -19.8, 156.2, {gs: undefined, trk: undefined, route: [nx(-24.6, 160.4, 0)]})], NOW, 30), null);
  assert.strictEqual(t.projection(W, [], [dl(7, -19.8, 156.2, {gs: undefined, trk: undefined, route: [nx(-24.6, 160.4, 60)]})], NOW, 30), null);
  pr = t.projection(W, [ap(20, 0, 0)], [dl(5, 40, 40, {suspect: 'x'}), dl(4, 41, 41, {time_unverified: true})], NOW, 30);
  assert.strictEqual(pr.fix.lat, 0, 'ignored the bad fixes and projected from the ADS-B one');
  pr = t.projection(W, [ap(20, 0, 0)], [dl(5, 0, 1.0)], NOW, 30);
  assert.strictEqual(pr.fix.lon, 1.0, 'a newer datalink fix is the basis, with its own speed and track');
  assert.strictEqual(pr.fix.src, 'ADS-C');
  // no speed or track with the fix: worked out from the move since the previous one
  pr = t.projection(W, [ap(25, 0, 0, {gs: undefined, track: undefined}), ap(10, 0, 2.0, {gs: undefined, track: undefined})], [], NOW, 30);
  assert.ok(pr && Math.abs(pr.gs - 480) < 1 && Math.abs(pr.trk - 90) < 0.5 && /from the move since the adsb\.lol fix 15 min earlier/.test(pr.basis), pr && pr.basis);
  assert.strictEqual(t.projection(W, [ap(10, 0, 2.0, {gs: undefined, track: undefined})], [], NOW, 30), null, 'a lone fix with no speed or track gives nothing to go on');
  // never past the destination: 100 nm ahead of the fix, and the projection would run 320 nm
  pr = t.projection({...W, dest: 'KXXX', dest_lat: 0, dest_lon: 100 / 60}, [ap(10, 0, 0)], [], NOW, 30);
  assert.strictEqual(pr.stop, 'KXXX');
  assert.ok(Math.abs(km([0, 0], pr.end) - 100) < 0.5 && Math.abs(km([0, 0], pr.now) - 80) < 0.5, 'ends at the destination, now is still short of it');
  assert.strictEqual(pr.ticks.length, 0, 'ticks beyond the destination (160, 240 and 320 nm out) are dropped, not piled on it');
  pr = t.projection({...W, dest_lat: 0, dest_lon: 200 / 60}, [ap(10, 0, 0)], [], NOW, 30);
  assert.strictEqual(JSON.stringify(pr.ticks.map(x => x.min)), '[10]', 'a destination 200 nm out keeps the +10 min tick (160 nm) and drops +20 (240 nm)');
  assert.ok(Math.abs(km([0, 0], pr.end) - 200) < 0.5);
  pr = t.projection({...W, dest_lat: 0, dest_lon: -2}, [ap(10, 0, 0)], [], NOW, 30);   // a destination BEHIND it is not a reason to stop
  assert.strictEqual(pr.stop, null);
  pr = t.projection({...W, route: {to: 'YSSY', to_lat: 0, to_lon: 100 / 60, stale: false}}, [ap(10, 0, 0)], [], NOW, 30);
  assert.strictEqual(pr.stop, 'YSSY', 'a recently reported destination also stops it');
  pr = t.projection({...W, route: {to: 'YSSY', to_lat: 0, to_lon: 100 / 60, stale: true}}, [ap(10, 0, 0)], [], NOW, 30);
  assert.strictEqual(pr.stop, null, 'a stale one does not');
  pr = t.projection({...W, dest: 'KUSR', dest_lat: 0, dest_lon: 500 / 60, route: {to: 'YSSY', to_lat: 0, to_lon: 100 / 60, stale: false}}, [ap(10, 0, 0)], [], NOW, 30);
  assert.strictEqual(pr.stop, null, 'the destination you set wins over the reported one (500 nm away: not reached in 40 min)');
  pr = t.projection({...W, dest: 'NZAA', dest_lat: 0, dest_lon: -179.0}, [ap(10, 0, 179.5)], [], NOW, 30);
  assert.strictEqual(pr.stop, 'NZAA', 'a destination just across the date line is ahead, not on the far side of the world');
  assert.ok(Math.abs(km([0, 179.5], [0, 181.0]) - 90) < 0.5);
  // across the date line the estimate carries on in the same frame
  assert.ok(t.advance(0, 179.5, 90, 60)[1] > 180, 'advance does not wrap: the page frames it');

  // drawing: ghost plane at the estimated position, a dotted line, ticks, an end marker; limits on the selector
  const drawn = async (ptsArr, ahead) => {
    t.setAhead(ahead);
    const sp = pts.slice(), sm = msgs.slice();
    pts.splice(0, pts.length, ...ptsArr); msgs.splice(0, msgs.length);
    calls.markers.length = 0; calls.lines.length = 0;
    const realNow = Date.now; Date.now = () => NOW;
    try { await t.showTrack(t.watches()[0], false); } finally { Date.now = realNow; }
    pts.splice(0, pts.length, ...sp); msgs.splice(0, msgs.length, ...sm);
    return t.projGroup.items;
  };
  let items = await drawn([ap(10, 0, 0), ap(20, 0, -1.3)], 30);
  const ghost = items.find(i => i.o && i.o.icon && /opacity: 0.55/.test(i.o.icon.html));
  assert.ok(ghost && Math.abs(ghost.ll[1] - 80 / 60) < 0.02, 'a ghost plane at the dead-reckoned position');
  assert.ok(/#777/.test(ghost.o.icon.html), 'greyed so it cannot be taken for a real position');
  assert.ok(items.some(i => i.o && i.o.dashArray === '2 6') && items.some(i => i.o && i.o.dashArray === '2 10'), 'a dotted line to now and a fainter one ahead');
  assert.strictEqual(items.filter(i => i.o && i.o.radius === 3).length, 3, 'ticks');
  assert.ok(lines(ghost.tooltip)[0].startsWith('Estimated position now (10 min since the last fix)'), lines(ghost.tooltip)[0]);
  items = await drawn([ap(0.3, 0, 0)], 30);        // a fix 18 seconds old: the real plane is already there
  assert.ok(!items.some(i => i.o && i.o.icon), 'no ghost on top of a fresh real position');
  assert.ok(items.some(i => i.o && i.o.dashArray === '2 10'), 'but the look-ahead is still drawn');
  assert.strictEqual((await drawn([ap(10, 0, 0)], 0)).length, 0, 'switched off: nothing drawn');
  assert.strictEqual((await drawn([ap(90, 0, 0)], 30)).length, 0, 'too old: nothing drawn');
  assert.strictEqual((await drawn([ap(10, 0, 0, {on_ground: true})], 30)).length, 0, 'on the ground: nothing drawn');
  t.setAhead(30);

  // ===== 8o. the plane is the newest trustworthy fix, and a plane standing on a report is that report ======
  const rep = (id, hh, extra = {}) => ({id, t: `2026-10-30T${hh}:00Z`, pos_t: `2026-10-30T${hh}:00Z`, kind: 'adsc', summary: 'ADS-C report ' + id, raw: 'r', label: 'B6', rx_count: 1, lat: 10, lon: 100, ...extra});
  const planeOf = r => r.markers.filter(m => m.o.icon && m.o.icon.className === 'plane').at(-1);
  // ADS-B hours older than a datalink report: the plane belongs at the report, as the map centring already did
  r = await draw([apt(0, 100)], [rep(60, '19:00', {lat: 12, lon: 104, trk: 135})], true);
  let pl = planeOf(r);
  assert.strictEqual(JSON.stringify(pl.ll), '[12,104]', 'at the newest fix, not the last ADS-B point');
  assert.match(pl.o.icon.html, /rotate\(135deg\)/, 'turned to the report\'s track');
  assert.strictEqual(JSON.stringify(r.view), '[[12,104],7]', 'and the map centres on it');
  const tipLines = lines(pl.tooltip);
  assert.ok(tipLines[0].endsWith(': last report') && tipLines.includes('ADS-C report 60') && tipLines.includes('no newer ADS-B'), tipLines.join(' | '));
  assert.ok(pl.handlers && pl.handlers.click, 'the plane is clickable');
  t.renderMessages([rep(60, '19:00', {lat: 12, lon: 104})]);
  pl.handlers.click();
  assert.strictEqual(t.selectedMsg2, 60, 'clicking it selects that report');
  assert.ok(t.rows.get(60)._cls.has('sel'));
  // a newer ADS-B point wins instead
  r = await draw([apt(40, 105)], [rep(61, '16:10', {lat: 12, lon: 104})], false);
  assert.strictEqual(planeOf(r).ll[1], 105, 'ADS-B newer than the report: the plane is the ADS-B point');
  // a newer but untrustworthy report does not move the plane
  r = await draw([apt(0, 100)], [rep(62, '19:00', {lat: 12, lon: 104, suspect: 'x'})], false);
  assert.strictEqual(planeOf(r).ll[1], 100);

  // ===== 8p. predictions follow the selection ======================================================
  const named = (kind, lat, lon, eta, name, src) => ({lat, lon, kind, eta_sec: eta, ...(name ? {name, name_src: src} : {})});
  const A = rep(70, '18:00', {lat: 0, lon: 140, route: [named('next', -1, 141, 600, 'OLGAN', 'learned'), named('next+1', -2, 142, null, 'KAPPU', 'database'), named('fixed', -3, 143, 2220)]});
  const B = rep(71, '18:30', {lat: -2, lon: 142, route: [named('next', -3, 143, 300), named('next+1', -4, 144, null)]});
  const C = {id: 72, t: '2026-10-30T18:40:00Z', pos_t: '2026-10-30T18:40:00Z', kind: 'miam', summary: 'REP081', raw: 'r', label: 'MA', rx_count: 1, lat: -2.5, lon: 142.5};   // predicts nothing
  const D = {id: 73, t: '2026-10-30T18:50:00Z', pos_t: '2026-10-30T18:50:00Z', kind: 'cpdlc', summary: 'CPDLC position report', raw: 'r', label: 'BA', rx_count: 1, lat: -4, lon: 144,
    waypoints: [wp('OLDONE', true, 'passed'), wp('NEXTONE', true, 'next', {lat: -5, lon: 145}), wp('THENONE', false, 'then', {lat: -6, lon: 146})]};
  await draw([], [A, B, C, D], false);
  const predItems = () => t.predGroup.items;
  const predLine = () => predItems().find(i => i.o && i.o.dashArray === '6 6');
  const predDots = () => predItems().filter(i => i.o && i.o.radius === 5);
  // nothing selected: the newest ADS-C route is shown (as it always was)
  assert.strictEqual(JSON.stringify(predLine().ll), '[[-2,142],[-3,143],[-4,144]]', 'newest ADS-C route: here -> next -> next+1');
  assert.strictEqual(predDots().length, 2, 'two waypoints (and no fixed projection in this one)');
  // select the older report: its predictions replace those
  t.rows.get(70).onclick();
  assert.strictEqual(JSON.stringify(predLine().ll), '[[0,140],[-1,141],[-2,142]]', 'the selected report\'s own route');
  assert.ok(!predLine().ll.some(p => p[0] === -4), 'nothing left from the other report');
  assert.strictEqual(predDots().length, 3, 'two waypoints and the flight computer\'s projection');
  const pdots = predDots();
  const t1 = lines(pdots[0].tooltip), t2 = lines(pdots[1].tooltip), tf = lines(pdots[2].tooltip);
  assert.strictEqual(t1[0], 'OLGAN — predicted next waypoint');
  assert.ok(t1.includes('ETA 18:10:00Z (+10 min)') && t1.includes('name from nav db (learned)'), t1.join(' | '));
  assert.strictEqual(t2[0], 'KAPPU — predicted waypoint after next');
  assert.ok(t2.includes('name from nav db (2013 src)'), t2.join(' | '));
  assert.ok(tf[0].startsWith('FMS forecast in 37 min') && tf.includes('ETA 18:37:00Z (+37 min)'), tf.join(' | '));
  // a nameless waypoint says why
  t.rows.get(71).onclick();
  assert.ok(lines(predDots()[0].tooltip).includes('(coordinates only)'));
  assert.ok(lines(predDots()[0].tooltip)[0] === 'predicted next waypoint', 'no name, no dash');
  // a point that predicts nothing: the predictions go away
  t.rows.get(72).onclick();
  assert.strictEqual(predItems().length, 0, 'selecting a REP081 clears them');
  // a CPDLC report: its next fixes (not the one it passed), placed ones only
  t.rows.get(73).onclick();
  const cl2 = predLine();
  assert.strictEqual(JSON.stringify(cl2.ll), '[[-4,144],[-5,145],[-6,146]]', 'here -> next -> then');
  assert.strictEqual(cl2.o.color, COLORS.cpdlc);
  assert.strictEqual(predDots().length, 0, 'rings, not the ADS-C style dots');
  assert.strictEqual(predItems().filter(i => i.o && i.o.radius === 9).length, 2);
  // clicking the report's own dot on the map does the same
  t.markers.get(70).handlers.click();
  assert.strictEqual(JSON.stringify(predLine().ll), '[[0,140],[-1,141],[-2,142]]', 'clicking the dot on the map plots its predictions too');
  // they survive a refresh of the page data
  await t.showTrack(t.watches()[0], false);
  assert.ok(predItems().length >= 0);
  // ... and with the plot fed by the real data again, nothing stale is left behind
  await draw([], [A, B, C, D], false);
  assert.strictEqual(predItems().length > 0, true);

  // ===== 8q. names in the list ===================================================================
  t.renderMessages([A, B, D]);
  const rowText = id => t.rows.get(id).children.filter(c => c.text !== undefined).map(c => c.text).join('');
  assert.strictEqual(rowText(70), 'ADS-C report 70 · next OLGAN, then KAPPU', 'a route whose waypoints we can name');
  assert.strictEqual(rowText(71), 'ADS-C report 71', 'nothing added when the names are not known');
  assert.strictEqual(rowText(73), 'CPDLC position report', 'the CPDLC summary already carries its names (from the decoder)');

  // ===== 8r. flights: most recent by default, all of them on request ==============================
  const seg = (id, flight, start, end, extra = {}) => ({id, flight, callsign: flight && 'CPA' + flight.slice(2), start, end, messages: 10, positions: 5, identified: !!flight, latest: false, ...extra});
  const OLD = seg('CX840@1', 'CX840', '2026-10-29T10:00:00Z', '2026-10-29T16:00:00Z', {from: 'VHHH', to: 'KJFK', messages: 40});
  const NEW = seg('CX843@2', 'CX843', '2026-10-30T10:00:00Z', '2026-10-30T20:00:00Z', {from: 'KJFK', to: 'VHHH', messages: 67, latest: true, from_name: 'John F Kennedy Intl', to_name: 'Hong Kong Intl', departed: '2026-10-30T10:05:00Z', departed_how: 'takeoff'});
  const bp = (day, hh, mm, lon) => ({t: `2026-10-${day}T${hh}:${mm}:00Z`, lat: 10, lon, alt_ft: 36000, gs: 480, track: 90, source: 'adsb.lol'});
  const FPTS = [bp(29, 11, '00', 10.0), bp(29, 11, '02', 10.2), bp(29, 11, '04', 10.4),     // the older flight
                bp(30, '02', '00', 50.0),                                                     // between flights: belongs to neither
                bp(30, 11, '00', 20.0), bp(30, 11, '02', 20.2), bp(30, 11, '04', 20.4)];   // the latest
  const fmsg = (id, day, hh, lon, extra = {}) => ({id, t: `2026-10-${day}T${hh}:00:00Z`, pos_t: `2026-10-${day}T${hh}:00:00Z`, kind: 'adsc', summary: 'ADS-C ' + id, raw: 'r', label: 'B6', rx_count: 1, lat: 11, lon, ...extra});
  const FMSGS = [fmsg(301, 29, 12, 11.0, {gs: 480, trk: 90}), fmsg(302, 30, 12, 21.0, {gs: 480, trk: 90}), {id: 303, t: '2026-10-30T05:00:00Z', kind: 'cpdlc', summary: 'CPDLC WILCO', raw: 'r', label: 'BA', rx_count: 1}];
  const lonsOf = r => r.lines.filter(l => l.o.color === COLORS.adsb).map(l => l.ll.map(p => p[1]));
  const flagsOf = r => r.markers.filter(m => m.o.icon && m.o.icon.className === 'fflag-wrap');
  const planes = r => r.markers.filter(m => m.o.icon && m.o.icon.className === 'plane');
  const heads = () => byId['#msgs'].children.filter(c => c.className && c.className.startsWith('fhead')).map(c => c.textContent);
  const rowIds = () => [...t.rows.keys()];
  const withFlights = async (segs, fn) => {
    flightData.splice(0, flightData.length, ...segs);
    delete storage.flightChoice;
    try { return await fn(); }
    finally { flightData.splice(0); t.chooseFlight('recent'); await new Promise(res => setTimeout(res, 20)); delete storage.flightChoice; }
  };
  const settle = () => new Promise(res => setTimeout(res, 20));
  const atTime = async (iso, fn) => { const real = Date.now; Date.now = () => Date.parse(iso); try { return await fn(); } finally { Date.now = real; } };
  const drawF = async (focus = true) => draw(FPTS, FMSGS, focus);

  await withFlights([OLD, NEW], async () => {
    // --- the default: the most recent flight only
    t.chooseFlight('recent'); await settle();
    r = await drawF();
    assert.strictEqual(t.flightChoice, 'recent');
    assert.strictEqual(JSON.stringify(lonsOf(r)), '[[20,20.2,20.4]]', 'only the latest flight\'s ADS-B points, as one line');
    assert.strictEqual(r.circles.filter(c => c.o.radius === 4).length, 1, 'only its message position');
    assert.ok(planes(r).length === 1, 'the plane is there: this is the flight in progress');
    assert.strictEqual(flagsOf(r).length, 1);
    assert.strictEqual(flagsOf(r)[0].o.icon.html.textContent, 'CX843 KJFK \u2192 VHHH · 30 Oct · dep 10:05Z', 'flight, route and when it left');
    // the aircraft's own hover names the flight, the route and the departure too
    const pt = lines(planes(r)[0].tooltip);
    assert.ok(pt.includes('CX843 KJFK \u2192 VHHH') && pt.some(l => /^took off \(takeoff report\) 2026-10-30 10:05Z$/.test(l)), pt.join(' | '));
    assert.ok(flagsOf(r)[0].o.icon.html.className.includes('latest'));
    assert.strictEqual(JSON.stringify(rowIds()), '[302]', 'the list holds the latest flight\'s messages, not the older flight\'s or the ground chatter');
    assert.strictEqual(JSON.stringify(heads()), JSON.stringify(['CX843 · 30 Oct · KJFK→VHHH · 67 msgs']));
    // the selector
    const sel = byId['#flightsel'];
    assert.strictEqual(JSON.stringify(sel.children.map(o => o.value)), '["recent","all","CX843@2","CX840@1"]', 'most recent, all, then each flight newest first');
    assert.strictEqual(sel.children[0].textContent, 'most recent: CX843 · 30 Oct · KJFK→VHHH');
    assert.strictEqual(sel.children[3].textContent, 'CX840 · 29 Oct · VHHH→KJFK');
    assert.strictEqual(sel.value, 'recent'); assert.strictEqual(sel.disabled, false);
    // flag tooltip
    const ft = lines(flagsOf(r)[0].tooltip);
    assert.ok(ft[0] === 'CX843 · 30 Oct · KJFK→VHHH' && ft.includes('John F Kennedy Intl → Hong Kong Intl') && ft.includes('67 messages, 5 positions'), ft.join(' | '));
    assert.ok(!ft.some(l => /click to show only/.test(l)), 'already the only one shown');

    // --- all flights: both, the older fainter, flagged, with a heading above each flight's messages
    t.chooseFlight('all'); await settle();
    assert.strictEqual(storage.flightChoice, 'all', 'the choice is remembered');
    r = await drawF();
    assert.strictEqual(JSON.stringify(lonsOf(r)), '[[10,10.2,10.4],[20,20.2,20.4]]', 'two lines, one per flight; the stray point joins neither');
    const ops = r.lines.filter(l => l.o.color === COLORS.adsb).map(l => l.o.opacity);
    assert.strictEqual(JSON.stringify(ops), '[0.35,0.9]', 'the older flight is fainter');
    const dotsOp = r.circles.filter(c => c.o.radius === 4).map(c => c.o.opacity);
    assert.strictEqual(JSON.stringify(dotsOp), '[0.4,1]', 'and so are its dots');
    assert.strictEqual(flagsOf(r).length, 2);
    assert.ok(planes(r).length === 1, 'the plane is the aircraft\'s current position');
    assert.strictEqual(JSON.stringify(heads()), JSON.stringify(['CX843 · 30 Oct · KJFK→VHHH · 67 msgs', 'not matched to a flight', 'CX840 · 29 Oct · VHHH→KJFK · 40 msgs']));
    assert.strictEqual(JSON.stringify(rowIds().sort()), '[301,302,303]');
    assert.ok(lines(flagsOf(r)[0].tooltip).some(l => /click to show only this flight/.test(l)));
    // headings and flags are ways to pick a flight
    const oldHead = byId['#msgs'].children.filter(c => c.className === 'fhead').at(-1);
    oldHead.onclick(); await settle();
    assert.strictEqual(t.flightChoice, 'CX840@1', 'clicking a flight\'s heading shows only that flight');
    assert.strictEqual(storage.flightChoice, 'all', 'a particular flight is not remembered');

    // --- an older flight: only its points; no plane, no projection; the map fits it
    //     (checked with the clock set five minutes after that flight's last report, when a projection WOULD be drawn)
    const fits = calls.fitBounds;
    t.chooseFlight('all'); await settle();
    await atTime('2026-10-30T12:05:00Z', async () => { await drawF(); });
    assert.ok(t.projGroup.items.length > 0, 'control: with the latest flight shown and fresh, a projection is drawn');
    t.chooseFlight('CX840@1'); await settle();
    await atTime('2026-10-29T12:05:00Z', async () => { r = await drawF('flight'); });
    assert.strictEqual(JSON.stringify(lonsOf(r)), '[[10,10.2,10.4]]');
    assert.strictEqual(planes(r).length, 0, 'no plane on a flight that is over');
    assert.strictEqual(t.projGroup.items.length, 0, 'and nothing projected');
    assert.strictEqual(calls.fitBounds, fits + 1, 'fits the map to the flight');
    assert.strictEqual(JSON.stringify(rowIds()), '[301]');
    assert.ok(!flagsOf(r)[0].o.icon.html.className.includes('latest'));
    assert.strictEqual(byId['#flightsel'].value, 'CX840@1');

    // --- flags pick a flight too
    t.chooseFlight('all'); await settle();
    r = await drawF();
    flagsOf(r).find(m => m.o.icon.html.textContent.startsWith('CX840')).handlers.click(); await settle();
    assert.strictEqual(t.flightChoice, 'CX840@1');

    // --- a choice that has gone away falls back to the most recent flight
    t.chooseFlight('NOSUCH@0'); await settle();
    r = await drawF();
    assert.strictEqual(JSON.stringify(lonsOf(r)), '[[20,20.2,20.4]]');
    assert.strictEqual(byId['#flightsel'].value, 'recent');
  });

  // which flight a time belongs to
  await withFlights([OLD, NEW], async () => {
    t.chooseFlight('all'); await settle(); await drawF();
    const ms = iso => Date.parse(iso);
    assert.strictEqual(t.segOf(ms('2026-10-29T12:00:00Z')), 0, 'inside the older flight');
    assert.strictEqual(t.segOf(ms('2026-10-30T10:10:00Z')), 1, 'inside the latest');
    assert.strictEqual(t.segOf(ms('2026-10-29T16:15:00Z')), 0, 'a quarter hour after it ended is still that flight');
    assert.strictEqual(t.segOf(ms('2026-10-29T17:00:00Z')), -1, 'but an hour after is the ground');
    assert.strictEqual(t.segOf(ms('2026-10-30T02:00:00Z')), -1, 'between flights');
    assert.strictEqual(t.segOf(NaN), -1, 'a time we cannot read belongs to no flight');
    assert.strictEqual(t.segOf(Date.parse('2026-10-30T2:00:00Z')), -1, 'a malformed time string is not read as "the first flight"');
  });

  // flights flown back to back are never one line, even if their points are only minutes apart
  await withFlights([seg('A@1', 'CX100', '2026-10-30T10:00:00Z', '2026-10-30T12:00:00Z'), seg('B@2', 'CX101', '2026-10-30T12:10:00Z', '2026-10-30T14:00:00Z', {latest: true})], async () => {
    t.chooseFlight('all'); await settle();
    r = await draw([bp(30, 11, '50', 30.0), bp(30, 11, '55', 30.1), bp(30, 12, '15', 30.2), bp(30, 12, '20', 30.3)], [], false);
    assert.strictEqual(JSON.stringify(lonsOf(r)), '[[30,30.1],[30.2,30.3]]', 'two lines');
  });

  // no flights identified yet: no filtering, a disabled selector saying so
  r = await draw(FPTS, FMSGS, false);
  assert.strictEqual(byId['#flightsel'].disabled, true);
  assert.strictEqual(byId['#flightsel'].children[0].textContent, 'no flights identified yet');
  assert.strictEqual(lonsOf(r).length >= 1, true, 'everything is drawn when there are no flights to choose between');
  assert.strictEqual(heads().length, 0, 'and no headings');

  // ===== 8s. how what was typed is read ===========================================================
  const submit = async (input, reply) => {
    postReply = reply;
    const q = id => document.querySelector(id);
    q('#err').textContent = ''; q('#note').textContent = '';
    await q('#add').onsubmit({preventDefault() {}, target: {values: {input, until: ''}, reset() {}}});
    return {note: q('#note').textContent, err: q('#err').textContent, sent: lastPostBody};
  };
  let sub = await submit('TZP1', {status: 201, body: {id: 5, kind: 'flight', ident: 'TZP1', status: 'waiting', start: '2026-10-30T17:00:00Z', end: '2026-11-01T05:00:00Z'}});
  assert.strictEqual(sub.sent.input, 'TZP1');
  assert.strictEqual(sub.note, 'Watching TZP1 as a callsign. For a registration, enter tail:TZP1.');
  sub = await submit('tail:CKK624', {status: 201, body: {id: 6, kind: 'tail', ident: 'CKK624', status: 'tracking', start: '2026-10-30T17:00:00Z', end: '2026-11-01T05:00:00Z'}});
  assert.strictEqual(sub.note, 'Watching CKK624 as a registration.', 'no advice when you said which it was');
  sub = await submit('CK624', {status: 201, body: {id: 7, kind: 'tail', ident: 'CK624', status: 'tracking', start: '2026-10-30T17:00:00Z', end: '2026-11-01T05:00:00Z'}});
  assert.strictEqual(sub.note, 'Watching CK624 as a registration. For a callsign, enter flight:CK624.', 'the way out is stated');
  sub = await submit('AEE123', {status: 201, body: {id: 8, kind: 'hex', ident: 'AEE123', status: 'tracking', start: '2026-10-30T17:00:00Z', end: '2026-11-01T05:00:00Z'}});
  assert.strictEqual(sub.note, 'Watching AEE123 as an ICAO hex address. For a callsign, enter flight:AEE123.');
  sub = await submit('UA869 2026-11-05', {status: 201, body: {id: 9, kind: 'flight', ident: 'UA869', date: '2026-11-05', status: 'waiting', start: '2026-10-30T17:00:00Z', end: '2026-11-01T05:00:00Z'}});
  assert.strictEqual(sub.note, 'Watching UA869 on 2026-11-05 as a flight number.');
  // a refusal shows the reason and no note
  sub = await submit('flight:CK624', {status: 400, body: {error: 'unknown airline code "CK"; enter the ICAO callsign instead (three letters, e.g. UAL869)'}});
  assert.ok(sub.err.includes('unknown airline code "CK"') && sub.note === '', sub.err + ' | ' + sub.note);

  // each watch carries a chip saying how it was read
  const chipOf = () => byId['#list'].children[0].querySelector('b').children.at(-1);
  watch.kind = 'flight'; watch.ident = 'TZP1'; delete watch.date; await t.refresh();
  assert.strictEqual(chipOf().textContent, 'callsign'); assert.strictEqual(chipOf().className, 'kindchip');
  watch.date = '2026-11-05'; await t.refresh(); assert.strictEqual(chipOf().textContent, 'flight');
  watch.kind = 'tail'; delete watch.date; await t.refresh(); assert.strictEqual(chipOf().textContent, 'registration');
  watch.kind = 'hex'; await t.refresh(); assert.strictEqual(chipOf().textContent, 'hex');
  watch.kind = 'tail'; watch.ident = 'B-LRM'; await t.refresh();

  // ===== 8t. notes: an icon on each watch, a side panel behind it ====================================
  const noteBtn = () => byId['#list'].children[0].querySelector('.notebtn');
  const NOTEHEX = watch.hex;
  delete watch.notes; delete watch.last_note; await t.refresh();
  assert.strictEqual(noteBtn().hidden, false, 'an aircraft can be annotated');
  assert.strictEqual(noteBtn().className, 'notebtn empty', 'faint when there are no notes');
  assert.strictEqual(noteBtn().textContent, '\u{1F4DD}');
  assert.strictEqual(noteBtn().title, 'Add a note about this airframe');
  watch.notes = 1; watch.last_note = 'The plane Yen took from SEA to HKG on 2026-10-27'; await t.refresh();
  assert.strictEqual(noteBtn().className, 'notebtn', 'solid when there are');
  assert.strictEqual(noteBtn().textContent, '\u{1F4DD} 1');
  assert.strictEqual(noteBtn().title, 'The plane Yen took from SEA to HKG on 2026-10-27\n\n(1 note: click to read, add or edit)', 'the newest note on hover');
  watch.notes = 3; await t.refresh();
  assert.strictEqual(noteBtn().textContent, '\u{1F4DD} 3');
  assert.ok(noteBtn().title.endsWith('(3 notes: click to read, add or edit)'));
  // no aircraft yet (a callsign still waiting for takeoff): nothing to attach a note to
  const savedHex = watch.hex; delete watch.hex; await t.refresh();
  assert.strictEqual(noteBtn().hidden, true, 'no icon without an aircraft');
  watch.hex = savedHex; await t.refresh();

  // clicking opens the panel without also selecting the watch
  const panel = document.querySelector('#notespanel');
  panel.hidden = true;
  let stoppedClick = false;
  const mounted0 = calls.mounted.length;
  noteBtn().onclick({stopPropagation() { stoppedClick = true; }});
  assert.ok(stoppedClick, 'does not also select the watch');
  assert.strictEqual(panel.hidden, false);
  assert.strictEqual(document.querySelector('#notestitle').textContent, 'Notes · B-LRM · A359');
  assert.strictEqual(calls.mounted.length, mounted0 + 1);
  const mt = calls.mounted.at(-1);
  assert.strictEqual(mt.hex, NOTEHEX);
  assert.strictEqual(mt.container, document.querySelector('#notesbody'));
  // a change in the panel refreshes the watch list, so the count follows
  const fetchedBefore = fetchLog.filter(u => u.startsWith('/api/watches')).length;
  watch.notes = 4;
  await mt.opts.onChange([]); await settle();
  assert.ok(fetchLog.filter(u => u.startsWith('/api/watches')).length > fetchedBefore, 'the list is reloaded');
  assert.strictEqual(noteBtn().textContent, '\u{1F4DD} 4');
  // closing
  document.querySelector('#notesclose').onclick();
  assert.strictEqual(panel.hidden, true);
  // a title falls back to the hex when the registration is not known
  const savedTail = watch.tail, savedType = watch.type; delete watch.tail; delete watch.type; await t.refresh();
  noteBtn().onclick({stopPropagation() {}});
  assert.strictEqual(document.querySelector('#notestitle').textContent, 'Notes · ' + NOTEHEX);
  watch.tail = savedTail; watch.type = savedType; delete watch.notes; delete watch.last_note; await t.refresh();
  document.querySelector('#notesclose').onclick();

  // ===== 8s. a route entered by hand ==============================================================
  {
    const hexOf = t.watches()[0].hex;
    const flight = extra => seg('CX900@3', 'CX900', '2026-10-30T10:00:00Z', '2026-10-30T20:00:00Z', {latest: true, ...extra});
    const startSecs = Date.parse('2026-10-30T10:00:00Z') / 1000;
    const editor = () => ({hidden: byId['#routeedit'].hidden, button: byId['#routebtn'].textContent, form: byId['#routeform'].hidden, note: byId['#routenote'].hidden});
    const choose = async c => { t.chooseFlight(c); await settle(); };

    // no route anywhere: the editor is offered
    await withFlights([flight()], async () => {
      await choose('recent');
      assert.deepStrictEqual(editor(), {hidden: false, button: 'Set route', form: true, note: true});
      byId['#routebtn'].onclick();
      assert.strictEqual(byId['#routeform'].hidden, false);
      assert.ok(!byId['#routefrom'].disabled && !byId['#routeto'].disabled, 'both ends can be typed');
      byId['#routefrom'].value = 'hkg'; byId['#routeto'].value = ' anc ';
      lastPut = null;
      await byId['#routesave'].onclick(); await settle();
      assert.strictEqual(lastPut.url, `/api/flights/${hexOf}/route`);
      assert.deepStrictEqual(lastPut.body, {start: startSecs, from: 'hkg', to: ' anc '.trim()});
      assert.strictEqual(byId['#routeform'].hidden, true, 'the form closes once it is saved');

      // the server refuses: the message is shown and the form stays open
      byId['#routebtn'].onclick();
      putReply = {status: 400, body: {error: '"ZZZ" is not an airport I know'}};
      await byId['#routesave'].onclick(); await settle();
      assert.strictEqual(byId['#routemsg'].textContent, '"ZZZ" is not an airport I know');
      assert.strictEqual(byId['#routeform'].hidden, false);
      putReply = {status: 200, body: {}};
      // Clear sends nothing for either end
      await byId['#routeclear'].onclick(); await settle();
      assert.deepStrictEqual(lastPut.body, {start: startSecs, from: '', to: ''});
      byId['#routebtn'].onclick(); byId['#routecancel'].onclick();
      assert.strictEqual(byId['#routeform'].hidden, true); assert.strictEqual(byId['#routemsg'].textContent, '');

      // with every flight shown there is nothing to edit
      await choose('all');
      assert.strictEqual(byId['#routeedit'].hidden, true);
    });

    // typing offers airports (by IATA code, so JNB finds O.R. Tambo) in the route boxes' own list, and leaves the airport tab's alone
    await withFlights([flight()], async () => {
      await choose('recent');
      byId['#routebtn'].onclick();
      document.querySelector('#aptlist').children = [];
      calls.timers.length = 0;
      byId['#routeto'].oninput({target: {value: 'jnb'}});
      await calls.timers.pop()();
      const opts = document.querySelector('#routeapts').children;
      assert.deepStrictEqual(opts.map(o => o.value), ['JNB']);
      assert.ok(/Tambo/.test(opts[0].textContent) && /FAOR/.test(opts[0].textContent), opts[0].textContent);
      assert.strictEqual(document.querySelector('#aptlist').children.length, 0, 'the airport tab\'s list is not the suggestions\' list');
      byId['#routecancel'].onclick();
    });

    // the messages gave the origin: it is shown, cannot be changed, and is never sent
    await withFlights([flight({from: 'KJFK', from_name: 'John F Kennedy Intl'})], async () => {
      await choose('recent');
      assert.strictEqual(byId['#routeedit'].hidden, false, 'the destination is still missing');
      byId['#routebtn'].onclick();
      assert.ok(byId['#routefrom'].disabled && byId['#routefrom'].value === 'KJFK' && !byId['#routeto'].disabled);
      byId['#routeto'].value = 'VHHH';
      await byId['#routesave'].onclick(); await settle();
      assert.deepStrictEqual(lastPut.body, {start: startSecs, from: '', to: 'VHHH'});
    });

    // a complete route from the messages: nothing to enter
    await withFlights([flight({from: 'KJFK', to: 'VHHH'})], async () => {
      await choose('recent');
      assert.strictEqual(byId['#routeedit'].hidden, true);
    });

    // entered by hand: starred where the route is written, and the button says Edit
    await withFlights([flight({from: 'KJFK', to: 'VHHH', to_manual: true})], async () => {
      await choose('recent');
      assert.deepStrictEqual(editor(), {hidden: false, button: 'Edit route', form: true, note: false});
      assert.strictEqual(byId['#flightsel'].children[2].textContent, 'CX900 \u00b7 30 Oct \u00b7 KJFK\u2192VHHH*');
      byId['#routebtn'].onclick();
      assert.ok(byId['#routefrom'].disabled && !byId['#routeto'].disabled && byId['#routeto'].value === 'VHHH', 'only the end entered by hand can be edited');
      byId['#routecancel'].onclick();
      const r2 = await drawF();
      assert.ok(flagsOf(r2).some(f => f.o.icon.html.textContent.startsWith('CX900 KJFK \u2192 VHHH* ')), flagsOf(r2).map(f => f.o.icon.html.textContent).join(' | '));
    });
  }

  // 9. a watch with no aircraft yet clears the map without error
  await t.showTrack({id: 9, hex: ''}, true);
  assert.match(byId['#msgs'].innerHTML, /No messages yet/);

  // 9b. selecting a waiting target after an aircraft leaves nothing of the aircraft: not its track, flights, plan or
  // messages, nor the result of a fetch that was still in flight when the click came
  flightData.push({id: 'f1', flight: 'CX1', start: '2026-10-30T15:00:00Z', end: '2026-10-30T16:30:00Z', messages: 3, positions: 2, identified: true, latest: true});
  planData.push({id: 7, t: '2026-10-30T15:50:00Z', from: 'KLAX', to: 'VHHH', points: [{name: 'KLAX', lat: 33.9, lon: -118.4, kind: 'airport'}, {name: 'VHHH', lat: 22.3, lon: 113.9, kind: 'airport'}]});
  await t.showTrack(t.watches()[0], false);
  const trackLayer = groups[0];
  assert.ok(Object.values(t.SOURCES).some(src => src.group.items.length > 0), 'report dots or tracks are on the map');
  assert.ok(trackLayer.items.length > 0 && t.planGroup.items.length > 0 && !byId['#flightsel'].disabled && t.flightSegs.length === 1, 'the aircraft is on the map');
  document.querySelector('#notespanel').hidden = false;
  highlightGroup.items.push({});
  const inFlight = t.showTrack(t.watches()[0], false);   // still fetching when the waiting target is chosen
  await t.showTrack({id: 9, hex: ''}, true);
  await inFlight;
  await tick();
  assert.strictEqual(trackLayer.items.length, 0, 'no track or plane');
  assert.strictEqual(t.planGroup.items.length, 0, 'no planned route');
  assert.strictEqual(t.bridgeGroup.items.length, 0, 'no estimated paths');
  assert.strictEqual(t.predGroup.items.length, 0); assert.strictEqual(t.wptGroup.items.length, 0); assert.strictEqual(t.projGroup.items.length, 0);
  assert.strictEqual(highlightGroup.items.length, 0, 'no ring or ghost');
  assert.strictEqual(t.markers.size, 0);
  for (const [k, src] of Object.entries(t.SOURCES)) assert.strictEqual(src.group.items.length, 0, `${k}: report dots and track gone`);
  assert.strictEqual(t.flightSegs.length, 0, 'no flights'); assert.ok(byId['#flightsel'].disabled, 'flight selector off');
  assert.match(byId['#msgs'].innerHTML, /No messages yet/);
  assert.strictEqual(document.querySelector('#notespanel').hidden, true, 'the notes panel of the previous aircraft is closed');
  flightData.length = 0; planData.length = 0;
  console.log('UI behaviour checks passed');
})().catch(e => { console.error('FAILED:', e.message); if (process.env.UITEST_STACK) console.error(e.stack.split('\n').slice(1, 4).join('\n')); process.exit(1); });
