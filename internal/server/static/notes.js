// Notes about one airframe: read them, add one, edit or delete an old one. Shared by the map page's
// side panel and the airframes page. Whatever the user writes is put on the page as text, never as HTML.
//
//   mountNotes(container, hex, {onChange})  ->  {reload}
//
// onChange(notes) is called after every successful add, edit or delete.
function mountNotes(container, hex, opts = {}) {
  const MAX = 4000;
  let notes = [], editing = null, draft = '', editDraft = '';
  const status = document.createElement('div');
  status.className = 'noteerr';

  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  };
  const when = t => new Date(t).toISOString().slice(0, 16).replace('T', ' ') + 'Z';
  const base = `/api/airframes/${hex}/notes`;

  async function call(method, path, body) {
    const r = await fetch(base + path, {method, headers: {'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
    if (r.status === 204) return null;
    const j = await r.json();
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  }

  // Run a change; show why it failed, or reload the notes and tell the page.
  async function act(change) {
    status.textContent = '';
    try { await change(); } catch (e) { status.textContent = e.message; return; }
    await load();
    if (opts.onChange) opts.onChange(notes);
  }

  async function load() {
    try { notes = await call('GET', ''); } catch (e) { status.textContent = e.message; }
    render();
  }

  function addBox() {
    const box = el('div', 'noteadd');
    const ta = el('textarea', 'noteinput');
    ta.placeholder = 'Add a note about this airframe…';
    ta.maxLength = MAX;
    ta.rows = 3;
    ta.value = draft;
    const btn = el('button', 'notebtn2', 'Add note');
    btn.disabled = !draft.trim();
    const submit = () => act(async () => { await call('POST', '', {note: ta.value}); draft = ''; });
    ta.oninput = () => { draft = ta.value; btn.disabled = !ta.value.trim(); };
    ta.onkeydown = e => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); if (!btn.disabled) submit(); } };
    btn.onclick = submit;
    box.append(ta, btn);
    return box;
  }

  function noteEl(n) {
    const d = el('div', 'note');
    const edited = n.updated !== n.created;
    d.append(el('div', 'notewhen', when(n.created) + (edited ? ` · edited ${when(n.updated)}` : '')));
    d.append(el('div', 'notebody', n.note));
    const bar = el('div', 'notebar');
    const edit = el('button', 'notelink', 'Edit');
    edit.onclick = () => { editing = n.id; editDraft = n.note; render(); };
    const del = el('button', 'notelink', 'Delete');
    del.onclick = () => { if (confirm('Delete this note?')) act(() => call('DELETE', '/' + n.id)); };
    bar.append(edit, del);
    d.append(bar);
    return d;
  }

  function editBox(n) {
    const d = el('div', 'note editing');
    d.append(el('div', 'notewhen', when(n.created)));
    const ta = el('textarea', 'noteinput');
    ta.maxLength = MAX;
    ta.rows = 4;
    ta.value = editDraft;
    const save = el('button', 'notebtn2', 'Save');
    save.disabled = !editDraft.trim();
    ta.oninput = () => { editDraft = ta.value; save.disabled = !ta.value.trim(); };
    const doSave = () => act(async () => { await call('PUT', '/' + n.id, {note: ta.value}); editing = null; });
    ta.onkeydown = e => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); if (!save.disabled) doSave(); } };
    save.onclick = doSave;
    const cancel = el('button', 'notelink', 'Cancel');
    cancel.onclick = () => { editing = null; render(); };
    const bar = el('div', 'notebar');
    bar.append(save, cancel);
    d.append(ta, bar);
    return d;
  }

  function render() {
    const items = [addBox(), status];
    if (!notes.length) items.push(el('div', 'noteempty', 'No notes yet.'));
    for (const n of notes) items.push(n.id === editing ? editBox(n) : noteEl(n));
    container.replaceChildren(...items);
  }

  render();
  load();
  return {reload: load};
}
