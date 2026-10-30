const fs = require('fs'), vm = require('vm'), assert = require('assert');
// Drives the real page script in internal/server/static/index.html against a fake DOM and a
// recording fake Leaflet. Run with: node scripts/uitest.js
const html = fs.readFileSync(require('path').join(__dirname, '../internal/server/static/index.html'), 'utf8');
const src = html.match(/<script>\n(const map[\s\S]*?)<\/script>/)[1];

// --- a just-enough DOM ---
class El {
  constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; this.className = ''; this.hidden = false; this.title = ''; this.dataset = {};
    this._cls = new Set(); const self = this;
    this.classList = { toggle(c, on) { on ? self._cls.add(c) : self._cls.delete(c); }, contains: c => self._cls.has(c) || self.className.split(' ').includes(c) }; }
  append(...n) { this.children.push(...n); }
  replaceChildren(...n) { this.children = n; }
  querySelector(sel) { return this._q ||= {}, this._q[sel] ||= new El('q'); }
  set innerHTML(v) { this._html = v; this.children = []; }
  get innerHTML() { return this._html; }
}
const byId = {};
const document = {
  querySelector: s => byId[s] ||= new El(s),
  createElement: t => new El(t),
  createTextNode: t => ({text: t}),
};
// --- a recording Leaflet ---
const calls = { lines: [], legend: null, addLayer: 0, fitBounds: 0, setView: [], panTo: [], markers: [], circles: [], polylines: 0, tooltipsOpened: 0, highlightAdds: [] };
const allLayers = [];
class Layer { constructor(){ this.items = []; this.open = false; allLayers.push(this); } addTo(t) { this.target = t; this.onMap = (t === mapObj); t.items && t.items.push(this); if (t === highlightGroup) calls.highlightAdds.push(this); return this; }
  bindTooltip(c) { this.tooltip = c; return this; } openTooltip() { calls.tooltipsOpened++; this.open = true; return this; } closeTooltip() { this.open = false; return this; } getBounds() { return {}; } }
let highlightGroup;
const groups = [];
const mapObj = { setView(ll, z) { calls.setView.push([ll, z]); return this; }, eachLayer(f) { allLayers.forEach(f); }, fitBounds() { calls.fitBounds++; }, panTo(ll) { calls.panTo.push(ll); },
  hasLayer: g => !!g.onMap, addLayer(g) { calls.addLayer++; g.onMap = true; }, removeLayer(g) { g.onMap = false; },
  getBounds: () => ({ pad: () => ({ contains: ll => !(ll[0] > 50) }) }) };   // "in view" unless lat > 50
const L = {
  map: () => mapObj, tileLayer: () => ({ addTo() {} }),
  layerGroup() { const g = new Layer(); g.clearLayers = () => { g.items = []; }; groups.push(g); return g; },
  polyline(ll, o) { calls.polylines++; const l = new Layer(); l.ll = ll; l.o = o; calls.lines.push(l); return l; },
  control: { layers: (base, overlays) => ({ addTo() { calls.legend = overlays; return this; } }) },
  circleMarker(ll, o) { const l = new Layer(); l.ll = ll; l.o = o; calls.circles.push(l); return l; },
  marker(ll, o) { const l = new Layer(); l.ll = ll; l.o = o; calls.markers.push(l); return l; },
  divIcon: o => o, featureGroup: () => ({ getBounds: () => ({}) }),
};
// --- the server ---
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
global.fetch = async url => {
  const body = url.startsWith('/api/watches') ? [watch] : url.startsWith('/api/track') ? pts : url.startsWith('/api/messages') ? msgs : null;
  return { status: 200, ok: true, json: async () => body };
};
let intervals = [];
const ctx = { document, L, fetch, console, Date, Number, Math, Set, Map, setInterval: (f, ms) => intervals.push([f, ms]), FormData: class {} };
ctx.global = ctx;
vm.createContext(ctx);
highlightGroup = null;

(async () => {
  // run the page script; capture the two layer groups (track layer first, highlight second)
  vm.runInContext(src + '\n;globalThis.__t = {SOURCES, refresh, showTrack, renderMessages, get selectedMsg(){return selectedMsg}, expanded, rows, markers, watches: () => watches};', ctx);
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
  li.onclick(); await tick(); await tick();
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
  assert.deepEqual(lines(plane.tooltip), ['CPA843 \u00b7 A359', '35,025 ft  \u2193 descending 1,800 ft/min', '480 kt \u00b7 track 135\u00b0', '2026-10-30 16:01Z (adsb.lol)'], 'plane hover text');

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

  // 6. a message without a location clears the ring
  t.rows.get(2).onclick();
  assert.strictEqual(highlightGroup.items.length, 0, 'ring cleared');
  assert.strictEqual(openTips().length, 0, 'all popups closed');
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
    return calls.lines.map(l => ({color: l.o.color, n: l.ll.length, dash: l.o.dashArray, ll: l.ll}));
  };
  const at2 = (mm, lat, lon, kind, extra = {}) => ({id: 100 + mm, t: `2026-10-30T15:${String(mm).padStart(2, '0')}:00Z`, kind, summary: 's', raw: 'r', label: 'B6', rx_count: 1, lat, lon, ...extra});
  // the legend lists all five sources
  assert.deepStrictEqual(Object.keys(calls.legend).map(k => k.replace(/<[^>]*>|&#\d+;/g, '').trim()), ['ADS-B', 'ADS-C', 'CPDLC', 'REP081', 'FMS']);
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

  // 8d. a message from a source switched off in the legend turns that track back on when selected
  await t.showTrack(t.watches()[0], false);
  t.SOURCES.adsc.group.onMap = false;            // as if unticked in the legend
  const added = calls.addLayer;
  t.rows.get(1).onclick();                        // message 1 is an ADS-C position
  assert.strictEqual(t.SOURCES.adsc.group.onMap, true);
  assert.strictEqual(calls.addLayer, added + 1);

  // 9. a watch with no aircraft yet clears the map without error
  await t.showTrack({id: 9, hex: ''}, true);
  assert.match(byId['#msgs'].innerHTML, /No messages yet/);
  console.log('UI behaviour checks passed');
})().catch(e => { console.error('FAILED:', e.message); process.exit(1); });
