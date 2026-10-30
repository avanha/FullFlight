// Behaviour of the notes component (static/notes.js) and the airframes page (static/airframes.html),
// run under node against a fake DOM and an in-memory copy of the notes API. Run: node scripts/uitest-notes.js
const fs = require('fs'), vm = require('vm'), assert = require('assert'), path = require('path');
const static_ = path.join(__dirname, '../internal/server/static');
const notesSrc = fs.readFileSync(path.join(static_, 'notes.js'), 'utf8');
const airframesHtml = fs.readFileSync(path.join(static_, 'airframes.html'), 'utf8');
const pageSrc = airframesHtml.match(/<script>\n(\/\/ ---- the airframes page[\s\S]*?)<\/script>/)[1];

class El {
  constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; this.className = ''; this.hidden = false; this.disabled = false;
    this.title = ''; this.value = ''; this.checked = false; this.style = {}; this._q = {}; }
  append(...n) { this.children.push(...n); }
  replaceChildren(...n) { this.children = n; }
  querySelector(sel) { return this._q[sel] ||= new El('q'); }
}
const all = (el, out = []) => { for (const c of el.children || []) { out.push(c); all(c, out); } return out; };
const withClass = (el, cls) => all(el).filter(c => (c.className || '').split(' ').includes(cls));
const text = el => (el.textContent || '') + (el.children || []).map(text).join('');

function newWorld() {
  const w = {byId: {}, calls: [], notes: {}, nextId: 1, clock: Date.parse('2026-10-30T12:00:00Z'), confirmAnswer: true, intervals: []};
  w.document = {querySelector: s => w.byId[s] ||= new El(s), createElement: t => new El(t), createTextNode: t => ({textContent: t, children: []})};
  w.airframes = [];
  w.fetch = async (url, opts = {}) => {
    const method = opts.method || 'GET';
    w.calls.push([method, url, opts.body ? JSON.parse(opts.body) : undefined]);
    const reply = (status, body) => ({status, ok: status < 400, statusText: 'x', json: async () => JSON.parse(JSON.stringify(body ?? null))});
    if (url === '/api/airframes') return reply(200, w.airframes);
    const m = url.match(/^\/api\/airframes\/([0-9A-F]{6})\/notes(?:\/(\d+))?$/);
    if (!m) return reply(404, {error: 'not found'});
    const [, hex, id] = m, list = (w.notes[hex] ||= []);
    if (method === 'GET') return reply(200, list.slice().sort((a, b) => b.id - a.id));
    if (method === 'POST') {
      const t = (JSON.parse(opts.body).note || '').trim();
      if (!t) return reply(400, {error: 'a note cannot be empty'});
      const n = {id: w.nextId++, hex, note: t, created: new Date(w.clock).toISOString(), updated: new Date(w.clock).toISOString()};
      list.push(n);
      return reply(201, n);
    }
    const n = list.find(x => x.id === Number(id));
    if (!n) return reply(404, {error: 'no such note'});
    if (method === 'PUT') { n.note = JSON.parse(opts.body).note.trim(); n.updated = new Date(w.clock).toISOString(); return reply(200, n); }
    if (method === 'DELETE') { list.splice(list.indexOf(n), 1); return {status: 204, ok: true}; }
  };
  w.ctx = vm.createContext({document: w.document, fetch: (...a) => w.fetch(...a), console, Date, Math, Number, JSON, Promise, Set, Map, String, Array,
    setInterval: (f, ms) => w.intervals.push([f, ms]), confirm: () => w.confirmAnswer});
  vm.runInContext(notesSrc, w.ctx);
  return w;
}
const tick = () => new Promise(r => setTimeout(r, 5));

(async () => {
  // ================= the notes component =================
  let w = newWorld();
  const box = new El('div');
  let changed = [];
  vm.runInContext('globalThis.__m = mountNotes', w.ctx);
  const handle = w.ctx.__m(box, '780AB8', {onChange: notes => changed.push(notes.length)});
  await tick();
  assert.deepStrictEqual(w.calls[0], ['GET', '/api/airframes/780AB8/notes', undefined], 'loads the airframe\'s notes');
  assert.ok(text(box).includes('No notes yet.'));
  const ta = () => withClass(box, 'noteinput')[0];
  const addBtn = () => withClass(box, 'notebtn2')[0];
  assert.strictEqual(addBtn().disabled, true, 'nothing to add yet');
  assert.strictEqual(ta().maxLength, 4000);

  // add a note
  ta().value = '  The plane Yen took from SEA to HKG on 2026-10-27  '; ta().oninput();
  assert.strictEqual(addBtn().disabled, false);
  await addBtn().onclick(); await tick();
  assert.deepStrictEqual(w.calls.at(-2).slice(0, 2), ['POST', '/api/airframes/780AB8/notes']);
  assert.strictEqual(w.calls.at(-2)[2].note, '  The plane Yen took from SEA to HKG on 2026-10-27  ');
  assert.ok(!text(box).includes('No notes yet.'));
  assert.strictEqual(withClass(box, 'notebody')[0].textContent, 'The plane Yen took from SEA to HKG on 2026-10-27');
  assert.strictEqual(ta().value, '', 'the box is cleared');
  assert.strictEqual(addBtn().disabled, true);
  assert.deepStrictEqual(changed, [1], 'the page is told');
  assert.ok(withClass(box, 'notewhen')[0].textContent === '2026-10-30 12:00Z', 'when it was written: ' + withClass(box, 'notewhen')[0].textContent);

  // Ctrl+Enter adds too; newest first
  w.clock += 3600e3;
  ta().value = 'Second note\nwith a line break'; ta().oninput();
  let prevented = false;
  await ta().onkeydown({ctrlKey: true, key: 'Enter', preventDefault() { prevented = true; }}); await tick();
  assert.ok(prevented);
  assert.deepStrictEqual(withClass(box, 'notebody').map(b => b.textContent), ['Second note\nwith a line break', 'The plane Yen took from SEA to HKG on 2026-10-27'], 'newest first, line breaks kept');
  assert.deepStrictEqual(changed, [1, 2]);
  // plain Enter does not add (it makes a new line)
  ta().value = 'draft'; ta().oninput();
  await ta().onkeydown({key: 'Enter', preventDefault() {}}); await tick();
  assert.strictEqual(withClass(box, 'notebody').length, 2);

  // a draft survives starting an edit elsewhere
  const editButtons = () => withClass(box, 'notelink').filter(b => b.textContent === 'Edit');
  editButtons()[1].onclick();                       // edit the older note
  assert.strictEqual(ta().value, 'draft', 'the unsent draft is still in the add box');
  const editArea = withClass(box, 'editing')[0];
  assert.ok(editArea, 'the note is now being edited');
  const editTa = withClass(editArea, 'noteinput')[0];
  assert.strictEqual(editTa.value, 'The plane Yen took from SEA to HKG on 2026-10-27', 'prefilled with the old text');
  // cancel changes nothing
  const before = w.calls.length;
  withClass(editArea, 'notelink').find(b => b.textContent === 'Cancel').onclick();
  assert.strictEqual(w.calls.length, before, 'cancelling makes no request');
  assert.strictEqual(withClass(box, 'editing').length, 0);
  // edit and save
  w.clock += 3600e3;
  editButtons()[1].onclick();
  const ed2 = withClass(withClass(box, 'editing')[0], 'noteinput')[0];
  ed2.value = 'Corrected: Yen flew it on 2026-10-28'; ed2.oninput();
  await withClass(withClass(box, 'editing')[0], 'notebtn2')[0].onclick(); await tick();
  assert.deepStrictEqual(w.calls.find(c => c[0] === 'PUT').slice(0, 2), ['PUT', '/api/airframes/780AB8/notes/1']);
  assert.deepStrictEqual(withClass(box, 'notebody').map(b => b.textContent), ['Second note\nwith a line break', 'Corrected: Yen flew it on 2026-10-28']);
  assert.ok(withClass(box, 'notewhen')[1].textContent.includes('edited 2026-10-30 14:00Z'), withClass(box, 'notewhen')[1].textContent);
  assert.ok(!withClass(box, 'notewhen')[0].textContent.includes('edited'), 'an unedited note says nothing of edits');
  assert.strictEqual(withClass(box, 'editing').length, 0);
  assert.strictEqual(ta().value, 'draft', 'and the draft is still there');

  // delete: asks first
  const deletes = () => withClass(box, 'notelink').filter(b => b.textContent === 'Delete');
  w.confirmAnswer = false;
  const n0 = w.calls.length;
  deletes()[0].onclick(); await tick();
  assert.strictEqual(w.calls.length, n0, 'declined: nothing happens');
  w.confirmAnswer = true;
  await deletes()[0].onclick(); await tick();
  assert.ok(w.calls.some(c => c[0] === 'DELETE' && c[1] === '/api/airframes/780AB8/notes/2'));
  assert.deepStrictEqual(withClass(box, 'notebody').map(b => b.textContent), ['Corrected: Yen flew it on 2026-10-28']);
  assert.strictEqual(changed.at(-1), 1);

  // a refusal is shown, and the text is not lost
  ta().value = '   '; ta().oninput();
  assert.strictEqual(addBtn().disabled, true, 'blank cannot be sent from the page');
  addBtn().disabled = false;                        // force it anyway
  await addBtn().onclick(); await tick();
  assert.strictEqual(withClass(box, 'noteerr')[0].textContent, 'a note cannot be empty');
  // the next success clears the message
  ta().value = 'fine'; ta().oninput();
  await addBtn().onclick(); await tick();
  assert.strictEqual(withClass(box, 'noteerr')[0].textContent, '');

  // what you write is text, never markup
  ta().value = '<img src=x onerror=alert(1)> & <b>bold</b>'; ta().oninput();
  await addBtn().onclick(); await tick();
  assert.ok(withClass(box, 'notebody').some(b => b.textContent === '<img src=x onerror=alert(1)> & <b>bold</b>'));
  assert.ok(!/innerHTML/.test(notesSrc), 'notes.js never builds markup from user text');

  // another airframe's notes are separate
  const box2 = new El('div');
  w.ctx.__m(box2, '780AA4', {}); await tick();
  assert.ok(text(box2).includes('No notes yet.'));
  assert.ok(w.calls.every(c => !c[1].includes('780AA4') || c[1].startsWith('/api/airframes/780AA4/')));

  // ================= the airframes page =================
  w = newWorld();
  const iso = h => new Date(w.clock - h * 3600e3).toISOString();
  w.airframes = [
    {hex: '780AB8', tail: 'B-LRM', tails: [{tail: 'B-LRM'}, {tail: 'B-OLD'}], type: 'A359', last_seen: iso(2), last_adsb: iso(2), last_datalink: iso(40), first_seen: iso(500), watched: true, notes: 2, last_note: 'Second note about the bird strike', messages: 400, positions: 100},
    {hex: '780AA4', tail: 'B-LRD', tails: [{tail: 'B-LRD'}], type: 'A359', last_seen: iso(30), first_seen: iso(400), notes: 0, messages: 500, positions: 50},
    {hex: 'ABB601', notes: 1, last_note: 'Seen once at Denver', messages: 0, positions: 0},    // known only from a note: no tail, never seen
  ];
  w.ctx.Date = Date;
  Object.assign(w.ctx, {Date: class extends Date { static now() { return w.clock; } }});
  const q = id => w.document.querySelector(id);
  q('#notespanel').hidden = true; q('#empty').hidden = true; q('#sort').value = 'seen';   // as the page's markup has them
  vm.runInContext(pageSrc, w.ctx);
  await tick();
  const rows = () => q('#rows').children;
  const cell = (tr, i) => tr.children[i];
  assert.strictEqual(rows().length, 3);
  assert.strictEqual(q('#count').textContent, '3 of 3 airframes · 2 with notes');
  // default order is the most recently seen first; an airframe never seen goes last
  assert.deepStrictEqual(rows().map(r => cell(r, 1).textContent), ['780AB8', '780AA4', 'ABB601']);
  let r0 = rows()[0];
  assert.strictEqual(text(cell(r0, 0)), 'B-LRMalso B-OLD', 'current registration, and the others it has had');
  assert.strictEqual(cell(r0, 2).textContent, 'A359');
  assert.strictEqual(cell(r0, 3).textContent, '2h ago');
  assert.strictEqual(cell(r0, 3).title, 'Last heard 2026-10-30 10:00Z\nADS-B: 2026-10-30 10:00Z\nDatalink: 2026-10-28 20:00Z', 'ADS-B and datalink times in the tooltip');
  assert.strictEqual(cell(rows()[2], 3).textContent, 'never heard');
  assert.strictEqual(cell(r0, 5).textContent, 'watching');
  assert.strictEqual(cell(r0, 6).children[0].textContent, '\u{1F4DD} 2 notes');
  assert.strictEqual(cell(r0, 6).children[1].textContent, 'Second note about the bird strike', 'a preview of the newest note');
  assert.strictEqual(text(cell(rows()[1], 6).children[0]), '\u{1F4DD} add a note');
  assert.ok(cell(rows()[1], 6).children[0].className.includes('empty'));
  assert.strictEqual(cell(rows()[1], 5).textContent, '', 'not watching');
  assert.strictEqual(text(cell(rows()[2], 0)), '—', 'no registration known');
  assert.strictEqual(cell(rows()[2], 3).textContent, 'never heard');
  assert.strictEqual(cell(rows()[2], 6).children[0].textContent, '\u{1F4DD} 1 note', 'singular');

  // search: registration (any it has had), hex, type, note text
  const search = async s => { q('#q').value = s; q('#q').oninput(); return rows().map(r => cell(r, 1).textContent); };
  assert.deepStrictEqual(await search('lrd'), ['780AA4']);
  assert.deepStrictEqual(await search('b-old'), ['780AB8'], 'an earlier registration finds it');
  assert.deepStrictEqual(await search('bird'), ['780AB8'], 'the text of a note');
  assert.deepStrictEqual(await search('abb6'), ['ABB601']);
  assert.deepStrictEqual(await search('a359'), ['780AB8', '780AA4']);
  assert.deepStrictEqual(await search('zzzz'), []);
  assert.strictEqual(q('#empty').hidden, false); assert.strictEqual(q('#empty').textContent, 'No airframe matches.');
  assert.strictEqual(q('#count').textContent, '0 of 3 airframes · 2 with notes');
  await search('');
  assert.strictEqual(q('#empty').hidden, true);
  // only with notes
  q('#onlynotes').checked = true; q('#onlynotes').onchange();
  assert.deepStrictEqual(rows().map(r => cell(r, 1).textContent), ['780AB8', 'ABB601']);
  q('#onlynotes').checked = false; q('#onlynotes').onchange();
  // sort
  q('#sort').value = 'tail'; q('#sort').onchange();
  assert.deepStrictEqual(rows().map(r => cell(r, 1).textContent), ['780AA4', '780AB8', 'ABB601'], 'by registration, those without last');
  q('#sort').value = 'notes'; q('#sort').onchange();
  assert.deepStrictEqual(rows().map(r => cell(r, 1).textContent), ['780AB8', 'ABB601', '780AA4'], 'most notes first');
  q('#sort').value = 'seen'; q('#sort').onchange();

  // opening an airframe's notes: the panel, the component, and the table following changes
  assert.strictEqual(q('#notespanel').hidden, true);
  cell(rows()[1], 6).children[0].onclick();               // B-LRD, which has none
  await tick();
  assert.strictEqual(q('#notespanel').hidden, false);
  assert.strictEqual(q('#notestitle').textContent, 'Notes · B-LRD · A359');
  assert.ok(w.calls.some(c => c[0] === 'GET' && c[1] === '/api/airframes/780AA4/notes'), 'the panel loaded B-LRD\'s notes');
  assert.ok(rows()[1].className.includes('open'), 'the open airframe is marked');
  const body = q('#notesbody');
  const pta = withClass(body, 'noteinput')[0];
  pta.value = 'Flown by the trainee crew'; pta.oninput();
  // the server now reports the note, as it would after the POST
  w.airframes[1].notes = 1; w.airframes[1].last_note = 'Flown by the trainee crew';
  const listCalls = () => w.calls.filter(c => c[1] === '/api/airframes').length;
  const l0 = listCalls();
  await withClass(body, 'notebtn2')[0].onclick(); await tick(); await tick();
  assert.ok(listCalls() > l0, 'the table reloads after a change');
  assert.strictEqual(q('#count').textContent, '3 of 3 airframes · 3 with notes');
  assert.strictEqual(cell(rows()[1], 6).children[0].textContent, '\u{1F4DD} 1 note');
  assert.strictEqual(cell(rows()[1], 6).children[1].textContent, 'Flown by the trainee crew');
  // closing
  q('#notesclose').onclick();
  assert.strictEqual(q('#notespanel').hidden, true);
  assert.ok(!rows()[1].className.includes('open'));
  // it keeps itself up to date
  assert.ok(w.intervals.some(([, ms]) => ms === 30000));

  // no airframes at all
  w.airframes = [];
  await q('#q').oninput(); await w.fetch('/api/airframes'); 
  vm.runInContext('load()', w.ctx); await tick();
  assert.strictEqual(q('#empty').hidden, false);
  assert.ok(q('#empty').textContent.startsWith('No airframes yet'), q('#empty').textContent);

  console.log('notes and airframes page checks passed');
})().catch(e => { console.error('FAILED:', e.message); if (process.env.UITEST_STACK) console.error(e.stack.split('\n').filter(l => l.includes('uitest-notes.js')).slice(0, 2).join('\n')); process.exit(1); });
