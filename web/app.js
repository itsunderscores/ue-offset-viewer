(() => {
'use strict';

// ---------- helpers ----------
const $ = (s) => document.querySelector(s);
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const reEsc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const fmt = (n) => n.toLocaleString('en-US');
const hex = (n) => '0x' + n.toString(16);

function makeHl(terms) {
  const ts = (terms || []).map((t) => String(t).trim()).filter(Boolean);
  if (!ts.length) return esc;
  const re = new RegExp(ts.map(reEsc).join('|'), 'gi');
  return (text) => {
    text = String(text);
    let out = '', last = 0, m;
    re.lastIndex = 0;
    while ((m = re.exec(text))) {
      if (!m[0].length) { re.lastIndex++; continue; }
      out += esc(text.slice(last, m.index)) + '<mark>' + esc(m[0]) + '</mark>';
      last = m.index + m[0].length;
    }
    return out + esc(text.slice(last));
  };
}

function typeClass(t) {
  const l = t.toLowerCase();
  if (l === 'bool' || l === 'boolproperty') return 't-bool';
  if (l === 'float' || l === 'double' || l === 'floatproperty' || l === 'doubleproperty') return 't-float';
  if (/^(u?int(8|16|32|64)|int|byteproperty|(u?int(8|16|32|64))property)$/.test(l)) return 't-int';
  if (/^(fstring|fname|ftext|textproperty|strproperty|nameproperty)$/.test(l)) return 't-string';
  if (l.includes('delegate')) return 't-delegate';
  if (l === 'enumproperty' || /^e[A-Z]/.test(t)) return 't-enum';
  if (l.endsWith('*') || l.includes('objectproperty') || l.includes('classproperty') || l.includes('interfaceproperty')) return 't-ptr';
  if (/^F[A-Z0-9_]/.test(t) || /^T(Array|Map|Set|Soft|Weak|SubclassOf|Optional|Script|Field|Enum)/.test(t) || l.endsWith('property')) return 't-struct';
  return 't-other';
}

let toastTimer;
function toast(msg) {
  const el = $('#toast');
  el.textContent = msg;
  el.classList.remove('hidden');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add('hidden'), 1400);
}

async function copy(text, label) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text; document.body.appendChild(ta); ta.select();
    document.execCommand('copy'); ta.remove();
  }
  toast('Copied ' + (label || text));
}

async function api(path) {
  const r = await fetch(path);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r.json();
}

// ---------- state ----------
const state = {
  classes: [],         // ClassSummary[] {n, f, l}, A→Z from server
  byName: new Map(),
  filtered: [],
  listQuery: '',
  current: null,       // full class {name, line, end_line, fields}
  rows: [],            // decorated fields {idx, name, offset, hex, type, line, gap}
  visible: [],
  selected: null,      // selected field name
  sort: { key: 'offset', dir: 1 },
  results: null,
  active: -1,
  seq: 0,
  etag: null,
  pendingField: null,
  offsets: null,       // Important Offsets doc from offsets.json (read-only)
  offsetGroup: '',     // active group chip filter
  selectedOffset: null,// "group/name" to highlight in the Important view
  view: null,          // 'class' | 'important' | null
};
const IMPORTANT_ROUTE = '!important';

// ---------- sidebar (virtual list) ----------
const list = $('#class-list');
const spacer = list.querySelector('.vlist-spacer');
const items = list.querySelector('.vlist-items');
const ROW_H = 30;
let listHl = esc;

function applyListFilter(resetScroll = true) {
  const q = $('#class-filter').value.trim().toLowerCase();
  const sort = $('#class-sort').value;
  state.listQuery = q;
  listHl = makeHl(q ? [q] : []);

  let arr = state.classes;
  if (q) {
    const starts = [], contains = [];
    for (const c of arr) {
      const i = c.n.toLowerCase().indexOf(q);
      if (i === 0) starts.push(c); else if (i > 0) contains.push(c);
    }
    arr = starts.concat(contains);
  } else {
    arr = arr.slice();
  }
  if (sort === 'file') arr.sort((a, b) => a.l - b.l);
  else if (sort === 'fields') arr.sort((a, b) => b.f - a.f || a.n.localeCompare(b.n));
  else if (q) { /* keep prefix-first order */ }
  // 'az' is the server's default order.

  state.filtered = arr;
  $('#class-count').textContent = `${fmt(arr.length)}${arr.length !== state.classes.length ? ' / ' + fmt(state.classes.length) : ''}`;
  if (resetScroll) list.scrollTop = 0;
  renderList();
}

function renderList() {
  const total = state.filtered.length;
  spacer.style.height = total * ROW_H + 'px';
  const top = list.scrollTop;
  const start = Math.max(0, Math.floor(top / ROW_H) - 8);
  const end = Math.min(total, Math.ceil((top + list.clientHeight) / ROW_H) + 8);
  items.style.transform = `translateY(${start * ROW_H}px)`;
  const cur = state.current ? state.current.name : null;
  let html = '';
  for (let i = start; i < end; i++) {
    const c = state.filtered[i];
    html += `<div class="vrow${c.f === 0 ? ' empty' : ''}${c.n === cur ? ' active' : ''}" data-i="${i}" title="${esc(c.n)} — ${c.f} fields, line ${c.l}"><span class="nm">${listHl(c.n)}</span><span class="cnt">${c.f}</span></div>`;
  }
  items.innerHTML = html;
}

let listRaf = 0;
list.addEventListener('scroll', () => {
  if (listRaf) return;
  listRaf = requestAnimationFrame(() => { listRaf = 0; renderList(); });
});
new ResizeObserver(renderList).observe(list);
items.addEventListener('click', (e) => {
  const row = e.target.closest('.vrow');
  if (row) navigate(state.filtered[+row.dataset.i].n);
});
list.addEventListener('keydown', (e) => {
  if (!state.current) return;
  const i = state.filtered.findIndex((c) => c.n === state.current.name);
  if (e.key === 'ArrowDown' && i < state.filtered.length - 1) { e.preventDefault(); navigate(state.filtered[i + 1].n); }
  if (e.key === 'ArrowUp' && i > 0) { e.preventDefault(); navigate(state.filtered[i - 1].n); }
});

function ensureListVisible(name) {
  const i = state.filtered.findIndex((c) => c.n === name);
  if (i < 0) return;
  const y = i * ROW_H;
  if (y < list.scrollTop || y + ROW_H > list.scrollTop + list.clientHeight) {
    list.scrollTop = Math.max(0, y - list.clientHeight / 2 + ROW_H / 2);
  }
  renderList();
}

$('#class-filter').addEventListener('input', () => applyListFilter(true));
$('#class-sort').addEventListener('change', () => applyListFilter(true));

// ---------- routing ----------
function navigate(cls, field) {
  const h = '#/' + encodeURIComponent(cls) + (field ? '/' + encodeURIComponent(field) : '');
  if (location.hash === h) onRoute(); else location.hash = h;
  closeSidebar();
}

function openSidebar() {
  document.body.classList.add('sidebar-open');
  $('#btn-menu').setAttribute('aria-expanded', 'true');
  $('#sidebar-backdrop').classList.remove('hidden');
}
function closeSidebar() {
  document.body.classList.remove('sidebar-open');
  $('#btn-menu').setAttribute('aria-expanded', 'false');
  $('#sidebar-backdrop').classList.add('hidden');
}
function toggleSidebar() {
  document.body.classList.contains('sidebar-open') ? closeSidebar() : openSidebar();
}

function parseHash() {
  const m = location.hash.match(/^#\/([^/]+)(?:\/(.+))?$/);
  if (!m) return null;
  try { return { cls: decodeURIComponent(m[1]), field: m[2] ? decodeURIComponent(m[2]) : null }; }
  catch { return null; }
}

function showView(which) {
  state.view = which;
  $('#empty-state').classList.toggle('hidden', which !== null);
  $('#class-view').classList.toggle('hidden', which !== 'class');
  $('#important-view').classList.toggle('hidden', which !== 'important');
  $('#important-pin').classList.toggle('active', which === 'important');
}

async function onRoute() {
  const r = parseHash();
  if (!r) {
    state.current = null;
    showView(null);
    document.title = 'SDK Viewer';
    renderList();
    return;
  }
  if (r.cls === IMPORTANT_ROUTE) {
    state.current = null;
    closeSource();
    showView('important');
    document.title = 'Important Offsets · SDK Viewer';
    renderList();
    state.selectedOffset = r.field || null; // "group/name"
    if (state.selectedOffset) { state.offsetGroup = ''; $('#important-filter').value = ''; }
    renderImportant();
    return;
  }
  if (!state.current || state.current.name !== r.cls) {
    try {
      await loadClass(r.cls);
    } catch (err) {
      toast('Class not found: ' + r.cls);
      return;
    }
  }
  selectField(r.field);
}
window.addEventListener('hashchange', onRoute);

// ---------- class view ----------
async function loadClass(name) {
  const c = await api('/api/class/' + encodeURIComponent(name));
  state.current = c;
  state.selected = null;
  closeSource();

  // Decorate: index in file order, gap to the next field by offset.
  const rows = c.fields.map((f, i) => ({ ...f, idx: i + 1, gap: null }));
  const byOff = rows.slice().sort((a, b) => a.offset - b.offset || a.idx - b.idx);
  for (let i = 0; i < byOff.length - 1; i++) byOff[i].gap = byOff[i + 1].offset - byOff[i].offset;
  state.rows = rows;

  $('#class-name').textContent = c.name;
  const max = rows.length ? Math.max(...rows.map((r) => r.offset)) : 0;
  const min = rows.length ? Math.min(...rows.map((r) => r.offset)) : 0;
  $('#class-meta').innerHTML = rows.length
    ? `${fmt(rows.length)} fields · offsets ${hex(min)} – ${hex(max)} · source lines ${fmt(c.line)}–${fmt(c.end_line)}`
    : `no fields · source line ${fmt(c.line)}`;
  document.title = c.name + ' · SDK Viewer';

  // Type filter options for this class.
  const counts = new Map();
  for (const r of rows) counts.set(r.type, (counts.get(r.type) || 0) + 1);
  const sel = $('#type-filter');
  sel.innerHTML = '<option value="">All types</option>' +
    [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([t, n]) => `<option value="${esc(t)}">${esc(t)} (${n})</option>`).join('');
  $('#field-filter').value = '';

  showView('class');
  renderTable();
  ensureListVisible(c.name);
}

function visibleRows() {
  const q = $('#field-filter').value.trim().toLowerCase();
  const t = $('#type-filter').value;
  let rows = state.rows;
  if (t) rows = rows.filter((r) => r.type === t);
  if (q) {
    rows = rows.filter((r) => r.name.toLowerCase().includes(q) || r.hex.toLowerCase().includes(q) || r.type.toLowerCase().includes(q));
  }
  const { key, dir } = state.sort;
  const cmp = {
    idx: (a, b) => a.idx - b.idx,
    name: (a, b) => a.name.localeCompare(b.name),
    offset: (a, b) => a.offset - b.offset || a.idx - b.idx,
    dec: (a, b) => a.offset - b.offset || a.idx - b.idx,
    type: (a, b) => a.type.localeCompare(b.type) || a.offset - b.offset,
    gap: (a, b) => (a.gap ?? -1) - (b.gap ?? -1) || a.offset - b.offset,
  }[key] || ((a, b) => a.offset - b.offset);
  rows = rows.slice().sort((a, b) => dir * cmp(a, b));
  return rows;
}

function renderTable() {
  const rows = visibleRows();
  state.visible = rows;
  const q = $('#field-filter').value.trim();
  const H = makeHl(q ? [q] : []);
  const cls = state.current.name;

  const tbody = $('#field-table tbody');
  let html = '';
  for (const r of rows) {
    html += `<tr data-f="${esc(r.name)}"${r.name === state.selected ? ' class="selected"' : ''}>` +
      `<td class="idx num">${r.idx}</td>` +
      `<td class="name" title="Copy name">${H(r.name)}</td>` +
      `<td class="offset num" title="Copy offset">${H(r.hex)}</td>` +
      `<td class="dec num">${r.offset}</td>` +
      `<td class="tcell"><span class="type ${typeClass(r.type)}" title="Search type:${esc(r.type)}">${H(r.type)}</span></td>` +
      `<td class="gap num">${r.gap == null ? '–' : r.gap}</td>` +
      `<td class="act"><button class="btn small" data-act="cpp" title="Copy C++ line">C++</button> <button class="btn small" data-act="link" title="Copy link to this field">link</button></td>` +
      `</tr>`;
  }
  tbody.innerHTML = html;

  for (const th of document.querySelectorAll('#field-table th[data-k]')) {
    th.classList.toggle('sorted', th.dataset.k === state.sort.key);
    th.classList.toggle('desc', th.dataset.k === state.sort.key && state.sort.dir < 0);
  }
  $('#field-count').textContent = rows.length === state.rows.length
    ? `${fmt(rows.length)} fields`
    : `${fmt(rows.length)} of ${fmt(state.rows.length)} fields`;
}

function cppLine(r) { return `inline uint64_t ${r.name} = ${r.hex}; // ${r.type}`; }

function selectField(name, flash = true) {
  state.selected = name || null;
  for (const tr of $('#field-table tbody').querySelectorAll('tr.selected')) tr.classList.remove('selected');
  if (!name) return;
  if (!state.visible.some((r) => r.name === name)) {
    // The target is filtered out; clear filters so it can be shown.
    $('#field-filter').value = '';
    $('#type-filter').value = '';
    renderTable();
  }
  const tr = [...$('#field-table tbody').rows].find((tr) => tr.dataset.f === name);
  if (!tr) { toast('Field not found: ' + name); return; }
  tr.classList.add('selected');
  if (flash) { tr.classList.remove('flash'); void tr.offsetWidth; tr.classList.add('flash'); }
  tr.scrollIntoView({ block: 'center' });
  if (!$('#source-view').classList.contains('hidden')) highlightSourceLine();
}

$('#field-table thead').addEventListener('click', (e) => {
  const th = e.target.closest('th[data-k]');
  if (!th) return;
  const k = th.dataset.k;
  if (state.sort.key === k) state.sort.dir *= -1; else state.sort = { key: k, dir: 1 };
  renderTable();
});

$('#field-table tbody').addEventListener('click', (e) => {
  const tr = e.target.closest('tr');
  if (!tr) return;
  const r = state.rows.find((r) => r.name === tr.dataset.f);
  if (!r) return;
  const btn = e.target.closest('button[data-act]');
  if (btn) {
    if (btn.dataset.act === 'cpp') copy(cppLine(r), 'C++ line');
    else copy(location.origin + '/#/' + encodeURIComponent(state.current.name) + '/' + encodeURIComponent(r.name), 'link');
    return;
  }
  if (e.target.closest('.type')) { search('type:' + r.type); return; }
  if (e.target.closest('td.name')) { copy(r.name); return; }
  if (e.target.closest('td.offset')) { copy(r.hex); return; }
  navigate(state.current.name, r.name);
});

$('#field-filter').addEventListener('input', renderTable);
$('#type-filter').addEventListener('change', renderTable);
$('#show-dec').addEventListener('change', (e) => $('#field-table').classList.toggle('hide-dec', !e.target.checked));
$('#show-gap').addEventListener('change', (e) => $('#field-table').classList.toggle('hide-gap', !e.target.checked));

$('#btn-copy-class').addEventListener('click', () => copy(state.current.name));
$('#btn-copy-cpp').addEventListener('click', () => {
  const body = state.visible.map((r) => '    ' + cppLine(r)).join('\n');
  copy(`namespace ${state.current.name} {\n${body}\n}`, `${state.visible.length} lines of C++`);
});
$('#btn-copy-json').addEventListener('click', () => {
  const doc = { name: state.current.name, fields: state.visible.map(({ name, offset, hex, type }) => ({ name, offset, hex, type })) };
  copy(JSON.stringify(doc, null, 2), 'JSON');
});

// ---------- source view ----------
async function openSource() {
  const c = state.current;
  if (!c) return;
  const data = await api(`/api/source?from=${c.line}&to=${c.end_line}`);
  const byLine = new Map(state.rows.map((r) => [r.line, r]));
  const pre = $('#source-pre');
  pre.innerHTML = data.lines.map((text, i) => {
    const ln = data.from + i;
    const f = byLine.get(ln);
    return `<div class="sl${f ? ' field' : ''}" data-ln="${ln}"${f ? ` data-f="${esc(f.name)}"` : ''}><span class="ln">${ln}</span><span>${esc(text) || ' '}</span></div>`;
  }).join('');
  $('#source-title').textContent = `${c.name} · lines ${fmt(data.from)}–${fmt(data.to)} of ${fmt(data.total)}`;
  $('#source-view').classList.remove('hidden');
  $('#btn-source').classList.add('active');
  highlightSourceLine();
}

function highlightSourceLine() {
  const pre = $('#source-pre');
  for (const el of pre.querySelectorAll('.sl.hl')) el.classList.remove('hl');
  if (!state.selected) return;
  const r = state.rows.find((r) => r.name === state.selected);
  const el = r && pre.querySelector(`.sl[data-ln="${r.line}"]`);
  if (el) { el.classList.add('hl'); el.scrollIntoView({ block: 'center' }); }
}

function closeSource() {
  $('#source-view').classList.add('hidden');
  $('#btn-source').classList.remove('active');
}

$('#btn-source').addEventListener('click', () => {
  $('#source-view').classList.contains('hidden') ? openSource() : closeSource();
});
$('#btn-source-close').addEventListener('click', closeSource);
$('#source-pre').addEventListener('click', (e) => {
  const el = e.target.closest('.sl.field');
  if (el) { closeSource(); navigate(state.current.name, el.dataset.f); }
});

// ---------- important offsets (read-only, from offsets.json on the server) ----------
async function loadOffsets() {
  const prev = state.offsets;
  const next = await api('/api/offsets');
  const changed = !prev || prev.loaded_at !== next.loaded_at || prev.error !== next.error;
  state.offsets = next;
  $('#important-count').textContent = next.total;
  if (changed) renderBuild();
  if (changed && state.view === 'important') renderImportant();
  return !!prev && changed;
}

// ----- game build badge (offsets.json → "_meta": {"version": "...", "date": "..."}) -----
function daysAgo(iso) {
  const [y, m, d] = iso.split('-').map(Number);
  const then = Date.UTC(y, m - 1, d);
  const now = new Date();
  const today = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate());
  return Math.round((today - then) / 86400000);
}

function agoText(n) {
  if (n === 0) return 'today';
  if (n === 1) return 'yesterday';
  if (n < 0) return `in ${-n} day${n === -1 ? '' : 's'}`;
  return `${n} days ago`;
}

function fmtDate(iso) {
  const [y, m, d] = iso.split('-').map(Number);
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' });
}

function buildHtml(meta) {
  const parts = [];
  if (meta.version) parts.push(`<span class="ver">${esc(meta.version)}</span>`);
  if (meta.date) {
    const n = daysAgo(meta.date);
    parts.push(`<span>${esc(fmtDate(meta.date))}</span><span class="ago${n > 30 ? ' stale' : ''}">(${agoText(n)})</span>`);
  } else if (meta.date_raw) {
    parts.push(`<span>${esc(meta.date_raw)}</span>`);
  }
  return parts.join('<span class="sep">·</span>');
}

function renderBuild() {
  const meta = state.offsets && state.offsets.meta;
  const badge = $('#build');
  const line = $('#empty-build');
  if (!meta || (!meta.version && !meta.date && !meta.date_raw)) {
    badge.classList.add('hidden'); line.classList.add('hidden');
    return;
  }
  const html = buildHtml(meta);
  badge.innerHTML = html;
  line.innerHTML = 'Offsets for build ' + html;
  badge.classList.remove('hidden');
  line.classList.remove('hidden');
  const extra = meta.extra ? Object.entries(meta.extra).map(([k, v]) => `${k}: ${v}`).join('\n') : '';
  const tip = 'Game build these offsets are for' + (meta.date_raw ? `\nDumped ${meta.date_raw}` : '') + (extra ? '\n' + extra : '');
  badge.title = tip;
  line.title = tip;
}
// Keep "N days ago" correct if the tab stays open past midnight.
setInterval(renderBuild, 60 * 60 * 1000);

function offsetEntries() {
  const d = state.offsets;
  return d ? d.groups.flatMap((g) => g.items) : [];
}

function impCpp(it) { return `inline uint64_t ${it.name} = ${it.hex};`; }

// ----- code snippets ("_code" section) -----
const CPP_KW = new Set(('alignas alignof and asm auto bool break case catch char char8_t char16_t char32_t class const consteval constexpr constinit ' +
  'const_cast continue decltype default delete do double dynamic_cast else enum explicit export extern false float for friend goto if inline int long ' +
  'mutable namespace new noexcept not nullptr operator or private protected public register reinterpret_cast requires return short signed sizeof static ' +
  'static_assert static_cast struct switch template this thread_local throw true try typedef typeid typename union unsigned using virtual void volatile ' +
  'while override final fn let mut pub impl match use def elif pass lambda None True False import from var function const').split(' '));
const CPP_TYPES = /^(?:u?int(?:8|16|32|64)?_t|uintptr_t|intptr_t|size_t|ptrdiff_t|std|DWORD|QWORD|BYTE|WORD|HANDLE|LPVOID|BOOL|FVector|FRotator|FName|FString|UObject|UWorld|AActor|APawn|[A-Z][A-Za-z0-9_]*(?:_t)?)$/;

function highlightCode(src) {
  const re = /(\/\/[^\n]*|\/\*[\s\S]*?\*\/)|("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*')|(^[ \t]*#[^\n]*)|(\b0[xX][0-9a-fA-F']+(?:u|U|l|L|ull|ULL|ll|LL)?\b|\b\d[\d']*(?:\.\d+)?(?:[eE][+-]?\d+)?(?:f|F|u|U|l|L|ull|ULL|ll|LL)?\b)|([A-Za-z_][A-Za-z0-9_]*)(?=\s*\()|([A-Za-z_][A-Za-z0-9_]*)|([{}()[\];,.<>=+\-*\/%&|^!~?:]+)/gm;
  let out = '', last = 0, m;
  while ((m = re.exec(src))) {
    out += esc(src.slice(last, m.index));
    const [tok, cmt, str, pre, num, call, word, punct] = m;
    if (cmt) out += `<span class="c">${esc(tok)}</span>`;
    else if (str) out += `<span class="s">${esc(tok)}</span>`;
    else if (pre) out += `<span class="k">${esc(tok)}</span>`;
    else if (num) out += `<span class="n">${esc(tok)}</span>`;
    else if (call) out += CPP_KW.has(tok) ? `<span class="k">${esc(tok)}</span>` : `<span class="f">${esc(tok)}</span>`;
    else if (word) out += CPP_KW.has(tok) ? `<span class="k">${esc(tok)}</span>` : CPP_TYPES.test(tok) ? `<span class="t">${esc(tok)}</span>` : esc(tok);
    else if (punct) out += `<span class="p">${esc(tok)}</span>`;
    else out += esc(tok);
    last = m.index + tok.length;
  }
  return out + esc(src.slice(last));
}

function snippetsFor(name) {
  const sn = (state.offsets && state.offsets.snippets) || [];
  const n = name.toLowerCase();
  return sn.filter((s) => (s.for && s.for.toLowerCase() === n) || s.title.toLowerCase() === n);
}

function renderSnippets(q) {
  const el = $('#important-code');
  const all = (state.offsets && state.offsets.snippets) || [];
  const list = q ? all.filter((s) => (s.title + ' ' + (s.for || '') + ' ' + (s.note || '') + ' ' + s.code).toLowerCase().includes(q)) : all;
  if (!list.length) { el.classList.add('hidden'); el.innerHTML = ''; return; }
  el.innerHTML = `<div class="sec-title">Code · ${list.length}</div>` + list.map((s) => {
    const i = all.indexOf(s);
    return `<div class="code-card" id="code-${i}" data-i="${i}">` +
      `<div class="code-head"><span class="title">${esc(s.title)}</span>` +
      (s.lang ? `<span class="lang">${esc(s.lang)}</span>` : '') +
      (s.for ? `<span class="for" data-for="${esc(s.for)}" title="Jump to this offset">@ ${esc(s.for)}</span>` : '') +
      `<button class="btn small" data-act="copy-code">Copy</button></div>` +
      (s.note ? `<div class="code-note">${esc(s.note)}</div>` : '') +
      `<pre><code>${highlightCode(s.code)}</code></pre></div>`;
  }).join('');
  el.classList.remove('hidden');
}

function showSnippet(i) {
  const card = $(`#code-${i}`);
  if (!card) return;
  card.scrollIntoView({ block: 'center', behavior: 'smooth' });
  card.classList.remove('flash'); void card.offsetWidth; card.classList.add('flash');
}

$('#important-code').addEventListener('click', (e) => {
  const card = e.target.closest('.code-card');
  if (!card) return;
  const s = state.offsets.snippets[+card.dataset.i];
  if (e.target.closest('[data-act="copy-code"]')) { copy(s.code, s.title); return; }
  const f = e.target.closest('.for');
  if (f) {
    const g = state.offsets.groups.find((g) => g.items.some((it) => it.name.toLowerCase() === f.dataset.for.toLowerCase()));
    const it = g && g.items.find((it) => it.name.toLowerCase() === f.dataset.for.toLowerCase());
    if (it) navigate(IMPORTANT_ROUTE, g.name + '/' + it.name); else toast(`No offset named ${f.dataset.for}`);
  }
});

function renderGroupChips() {
  const d = state.offsets;
  const el = $('#important-groups');
  if (!d || !d.groups.length) { el.innerHTML = ''; return; }
  el.innerHTML = d.groups.map((g) =>
    `<span class="chip${state.offsetGroup === g.name ? ' on' : ''}" data-g="${esc(g.name)}">${esc(g.name)} <span class="n">${g.items.length}</span></span>`
  ).join('') + (d.snippets && d.snippets.length
    ? `<span class="chip code-chip" data-code="1" title="Jump to code snippets">{ } code <span class="n">${d.snippets.length}</span></span>` : '');
}

function renderImportant() {
  const d = state.offsets;
  const tbody = $('#important-table tbody');
  const err = $('#important-error');
  const empty = $('#important-empty');
  if (!d) return;

  err.classList.toggle('hidden', !d.error);
  if (d.error) err.textContent = 'offsets.json could not be parsed — showing the last good version.\n' + d.error;

  let metaText = `${fmt(d.total)} offsets in ${d.groups.length} group${d.groups.length === 1 ? '' : 's'}`;
  if (d.meta && d.meta.version) metaText += ` · build ${d.meta.version}`;
  if (d.meta && d.meta.date) metaText += ` · ${fmtDate(d.meta.date)} (${agoText(daysAgo(d.meta.date))})`;
  $('#important-meta').textContent = metaText;
  renderGroupChips();

  const q = $('#important-filter').value.trim().toLowerCase();
  const H = makeHl(q ? [q] : []);
  let shown = 0, idx = 0;
  let html = '';
  for (const g of d.groups) {
    if (state.offsetGroup && g.name !== state.offsetGroup) continue;
    const items = q ? g.items.filter((it) => it.name.toLowerCase().includes(q) || it.hex.toLowerCase().includes(q) || g.name.toLowerCase().includes(q)) : g.items;
    if (!items.length) continue;
    html += `<tr class="grp" data-g="${esc(g.name)}"><td colspan="6">${esc(g.name)} <span class="muted">· ${items.length}</span>` +
      `<button class="btn small" data-act="grp-cpp" title="Copy this group as a C++ namespace">Copy C++</button></td></tr>`;
    for (const it of items) {
      const key = g.name + '/' + it.name;
      html += `<tr data-g="${esc(g.name)}" data-n="${esc(it.name)}"${key === state.selectedOffset ? ' class="selected"' : ''}>` +
        `<td class="idx num">${++idx}</td>` +
        `<td class="name" title="Copy name">${H(it.name)}</td>` +
        `<td class="offset num" title="Copy offset">${H(it.hex)}</td>` +
        `<td class="dec num">${it.offset}</td>` +
        `<td class="grp-cell">${H(g.name)}</td>` +
        `<td class="act">` +
        (snippetsFor(it.name).length ? `<button class="btn small code-btn" data-act="code" title="Show code for ${esc(it.name)}">{ }</button> ` : '') +
        `<button class="btn small" data-act="cpp" title="Copy C++ line">C++</button> ` +
        `<button class="btn small" data-act="sdk" title="Find SDK fields at this offset">SDK @${esc(it.hex)}</button> ` +
        `<button class="btn small" data-act="link" title="Copy link">link</button></td></tr>`;
      shown++;
    }
  }
  tbody.innerHTML = html;
  $('#important-filter-count').textContent = shown === d.total ? `${fmt(d.total)} offsets` : `${fmt(shown)} of ${fmt(d.total)} offsets`;
  renderSnippets(state.offsetGroup ? '\0' : q); // hide snippets while a group chip is active

  empty.classList.toggle('hidden', shown > 0);
  if (shown === 0) {
    empty.innerHTML = d.total === 0
      ? `No <code>offsets.json</code> found next to the server binary.<br>Create one with <code>{ "group": { "Name": "0x1A0", … } }</code> and it will appear here automatically.`
      : 'No offsets match the current filter.';
  }

  if (state.selectedOffset) {
    const tr = tbody.querySelector('tr.selected');
    if (tr) { tr.classList.add('flash'); tr.scrollIntoView({ block: 'center' }); }
  }
}

$('#important-filter').addEventListener('input', () => { state.selectedOffset = null; renderImportant(); });
$('#important-groups').addEventListener('click', (e) => {
  const chip = e.target.closest('.chip');
  if (!chip) return;
  if (chip.dataset.code) {
    if (state.offsetGroup) { state.offsetGroup = ''; renderImportant(); }
    $('#important-code').scrollIntoView({ block: 'start', behavior: 'smooth' });
    return;
  }
  state.offsetGroup = state.offsetGroup === chip.dataset.g ? '' : chip.dataset.g;
  state.selectedOffset = null;
  renderImportant();
});

$('#important-table tbody').addEventListener('click', (e) => {
  const tr = e.target.closest('tr');
  if (!tr || !state.offsets) return;
  const g = state.offsets.groups.find((x) => x.name === tr.dataset.g);
  if (!g) return;
  const btn = e.target.closest('button[data-act]');
  if (tr.classList.contains('grp')) {
    if (btn && btn.dataset.act === 'grp-cpp') {
      copy(`namespace ${g.name} {\n${g.items.map((it) => '    ' + impCpp(it)).join('\n')}\n}`, `${g.name} as C++`);
    }
    return;
  }
  const it = g.items.find((x) => x.name === tr.dataset.n);
  if (!it) return;
  if (btn) {
    switch (btn.dataset.act) {
      case 'cpp': copy(impCpp(it), 'C++ line'); break;
      case 'sdk': search(it.hex); break;
      case 'code': {
        const s = snippetsFor(it.name)[0];
        if (state.offsetGroup) { state.offsetGroup = ''; renderImportant(); }
        showSnippet(state.offsets.snippets.indexOf(s));
        break;
      }
      case 'link': copy(location.origin + '/#/' + IMPORTANT_ROUTE + '/' + encodeURIComponent(g.name + '/' + it.name), 'link'); break;
    }
    return;
  }
  if (e.target.closest('td.name')) copy(it.name);
  else if (e.target.closest('td.offset')) copy(it.hex);
});

$('#btn-important-cpp').addEventListener('click', () => {
  const d = state.offsets;
  if (!d) return;
  const text = d.groups.map((g) => `namespace ${g.name} {\n${g.items.map((it) => '    ' + impCpp(it)).join('\n')}\n}`).join('\n\n');
  copy(text, `${d.total} offsets as C++`);
});
$('#btn-important-json').addEventListener('click', () => {
  const d = state.offsets;
  if (!d) return;
  const obj = {};
  for (const g of d.groups) { obj[g.name] = {}; for (const it of g.items) obj[g.name][it.name] = it.hex; }
  copy(JSON.stringify(obj, null, 2), 'JSON');
});
$('#important-pin').addEventListener('click', () => navigate(IMPORTANT_ROUTE));

// ---------- search ----------
const input = $('#search');
const results = $('#results');
let searchTimer;

function search(q) {
  input.value = q;
  input.focus();
  runSearch();
}

input.addEventListener('input', () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(runSearch, 50);
});
input.addEventListener('focus', () => { if (state.results && input.value.trim()) results.classList.remove('hidden'); });

async function runSearch() {
  const q = input.value.trim();
  if (!q) { hideResults(); state.results = null; return; }
  const seq = ++state.seq;
  let data;
  try { data = await api('/api/search?q=' + encodeURIComponent(q)); }
  catch (err) { toast('Search failed: ' + err.message); return; }
  if (seq !== state.seq) return;
  state.results = data;
  state.active = -1;
  renderResults(data);
}

function renderResults(d) {
  const H = makeHl(d.terms);
  let html = '';
  const flat = [];
  if (d.offsets && d.offsets.length) {
    html += `<div class="group">Important Offsets · ${fmt(d.offsets.length)}</div>`;
    for (const it of d.offsets) {
      flat.push({ cls: IMPORTANT_ROUTE, field: it.group + '/' + it.name });
      html += `<div class="result imp" data-i="${flat.length - 1}">` +
        `<div class="title"><span class="cls">${H(it.group)}</span><span class="sep">›</span>${H(it.name)}</div>` +
        `<div class="right">${H(it.hex)}</div>` +
        `<div class="preview"><span class="muted">offsets.json · ${it.offset}</span></div></div>`;
    }
  }
  if (d.classes.length) {
    html += `<div class="group">Classes · ${fmt(d.class_total)}</div>`;
    for (const c of d.classes) {
      flat.push({ cls: c.name });
      html += `<div class="result" data-i="${flat.length - 1}"><div class="title">${H(c.name)}</div><div class="right">${c.fields} fields</div>` +
        `<div class="preview"><span class="ln">L${fmt(c.line)}</span>namespace ${esc(c.name)} {</div></div>`;
    }
  }
  if (d.fields.length) {
    html += `<div class="group">Fields · ${fmt(d.field_total)}${d.field_total > d.fields.length ? ` (showing ${d.fields.length})` : ''}</div>`;
    for (const f of d.fields) {
      flat.push({ cls: f.class, field: f.name });
      html += `<div class="result" data-i="${flat.length - 1}">` +
        `<div class="title"><span class="cls">${H(f.class)}</span><span class="sep">›</span>${H(f.name)}</div>` +
        `<div class="right">${esc(f.hex)} · <span class="type ${typeClass(f.type)}">${esc(f.type)}</span></div>` +
        `<div class="preview"><span class="ln">L${fmt(f.line)}</span>${H(f.src)}</div></div>`;
    }
  }
  if (!flat.length) html += `<div class="meta-line">No matches for <code>${esc(d.query)}</code>.</div>`;
  html += `<div class="meta-line">${fmt(d.field_total)} field${d.field_total === 1 ? '' : 's'} · ${fmt(d.class_total)} class${d.class_total === 1 ? '' : 'es'} · ${d.took_ms.toFixed(1)} ms` +
    (d.field_total > d.fields.length ? ' · refine with <code>type:</code>, <code>in:</code> or <code>Class::Field</code>' : '') + `</div>`;
  results.innerHTML = html;
  results.scrollTop = 0;
  results.classList.remove('hidden');
  state.flat = flat;
}

function hideResults() { results.classList.add('hidden'); }

function openResult(i) {
  const r = state.flat && state.flat[i];
  if (!r) return;
  hideResults();
  navigate(r.cls, r.field);
}

results.addEventListener('mousedown', (e) => e.preventDefault()); // keep focus in the input
results.addEventListener('click', (e) => {
  const el = e.target.closest('.result');
  if (el) openResult(+el.dataset.i);
});

function setActive(i) {
  const els = results.querySelectorAll('.result');
  if (!els.length) return;
  i = Math.max(0, Math.min(els.length - 1, i));
  els.forEach((el, j) => el.classList.toggle('active', j === i));
  state.active = i;
  els[i].scrollIntoView({ block: 'nearest' });
}

input.addEventListener('keydown', (e) => {
  if (e.key === 'ArrowDown') { e.preventDefault(); if (results.classList.contains('hidden') && state.results) results.classList.remove('hidden'); setActive(state.active + 1); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); setActive(state.active - 1); }
  else if (e.key === 'Enter') { e.preventDefault(); openResult(state.active < 0 ? 0 : state.active); }
  else if (e.key === 'Escape') { hideResults(); input.blur(); }
});

document.addEventListener('click', (e) => {
  if (!e.target.closest('.search-wrap')) hideResults();
});

// ---------- global keys ----------
document.addEventListener('keydown', (e) => {
  const inInput = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName);
  if ((e.key === '/' && !inInput) || (e.ctrlKey && e.key.toLowerCase() === 'k')) {
    e.preventDefault(); input.focus(); input.select();
  } else if (e.ctrlKey && e.key.toLowerCase() === 'f' && state.current && !$('#class-view').classList.contains('hidden')) {
    e.preventDefault(); $('#field-filter').focus(); $('#field-filter').select();
  } else if (e.key === 'Escape') {
    if (!$('#help').classList.contains('hidden')) $('#help').classList.add('hidden');
    else if (!$('#source-view').classList.contains('hidden')) closeSource();
  }
});

// ---------- sidebar drawer (mobile / narrow) ----------
$('#btn-menu').addEventListener('click', toggleSidebar);
$('#sidebar-backdrop').addEventListener('click', closeSidebar);
window.addEventListener('resize', () => {
  if (window.matchMedia('(min-width: 1024px)').matches) closeSidebar();
  if (state.lastStats) $('#stats').textContent = formatStats(state.lastStats);
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && document.body.classList.contains('sidebar-open')) closeSidebar();
});

// ---------- help / stats ----------
$('#btn-help').addEventListener('click', () => $('#help').classList.remove('hidden'));
$('#btn-help-close').addEventListener('click', () => $('#help').classList.add('hidden'));
$('#help').addEventListener('click', (e) => { if (e.target === e.currentTarget) e.currentTarget.classList.add('hidden'); });

function formatStats(s) {
  const narrow = window.matchMedia('(max-width: 640px)').matches;
  if (narrow) {
    const k = (n) => (n >= 1000 ? (n / 1000).toFixed(n >= 10000 ? 0 : 1).replace(/\.0$/, '') + 'k' : fmt(n));
    return `${k(s.classes)} classes · ${k(s.fields)} fields · ${s.parse_ms.toFixed(0)} ms`;
  }
  return `${fmt(s.classes)} classes · ${fmt(s.fields)} fields · parsed in ${s.parse_ms.toFixed(0)} ms`;
}

async function loadStats() {
  const s = await api('/api/stats');
  state.lastStats = s;
  $('#stats').textContent = formatStats(s);
  $('#stats').title = `${s.source}\nloaded ${new Date(s.loaded_at).toLocaleString()}\n${fmt(s.classes)} classes · ${fmt(s.fields)} fields`;
  return s;
}

async function loadClasses() {
  state.classes = await api('/api/classes');
  state.byName = new Map(state.classes.map((c) => [c.n, c]));
}

async function refreshAll(msg) {
  await Promise.all([loadClasses(), loadStats(), loadOffsets()]);
  applyListFilter(false);
  if (state.current) {
    const name = state.current.name;
    const sel = state.selected;
    state.current = null;
    try { await loadClass(name); selectField(sel, false); } catch { navigate(''); }
  }
  if (msg) toast(msg);
}

// Pick up server-side hot reloads (file changed on disk).
setInterval(async () => {
  try {
    const s = await api('/api/stats');
    if (state.etag && s.etag !== state.etag) { state.etag = s.etag; await refreshAll('Source file changed — reloaded'); }
    else { state.etag = s.etag; if (await loadOffsets()) toast('offsets.json changed - reloaded'); } // picks up server-side edits
  } catch { /* server down; ignore */ }
}, 5000);

// ---------- init ----------
(async function init() {
  try {
    const [, s] = await Promise.all([loadClasses(), loadStats(), loadOffsets()]);
    state.etag = s.etag;
  } catch (err) {
    $('#stats').textContent = 'Failed to load: ' + err.message;
    return;
  }
  applyListFilter(true);
  onRoute();
  // Shareable searches: /?q=type:bool%20zipline
  const q = new URLSearchParams(location.search).get('q');
  if (q) search(q);
})();
})();
