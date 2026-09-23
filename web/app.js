'use strict';

const SVGNS = 'http://www.w3.org/2000/svg';
const $ = (s) => document.querySelector(s);

const PAD = 4;
const MIN_W = 2;
const GAP = 2;
const HEADER_H = 22;
const TRACK_H = 14;
const TRACKS = 3; // current merges, queued merges, candidates
const BAR_H = 40;
const AXIS_H = 22;
const LANE_GAP = 14;
const LANE_H = HEADER_H + TRACK_H * TRACKS + 4 + BAR_H + AXIS_H + LANE_GAP;

// Fixed level buckets, so a colour keeps its meaning across refreshes.
const LEVEL_BUCKETS = [[0, 0], [1, 1], [2, 2], [3, 3], [4, 4], [5, 6], [7, 9], [10, 19], [20, 49], [50, Infinity]];

const state = {
  tables: [],
  hosts: [],
  data: null,
  hover: [],
  minmax: new Map(), // partition key -> Promise<{columns, byPart}>
  loading: false,
  timer: null,
  showTable: false,
  lastPointer: null,
};

// ---------- formatting ----------

const nf = new Intl.NumberFormat();
function fmtNum(n) { return nf.format(n); }
function fmtBytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  const i = Math.min(u.length - 1, Math.floor(Math.log2(n) / 10));
  const v = n / 2 ** (10 * i);
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`;
}
function fmtDuration(s) {
  s = Math.max(0, Math.round(s));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}
function fmtTime(t) {
  if (!t) return '—';
  const d = new Date(t * 1000);
  const pad = (x) => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
function fmtTimeAgo(t, now) { return t ? `${fmtTime(t)} (${fmtDuration(now - t)} ago)` : '—'; }
function fmtScore(x) { return x >= 1e6 ? x.toExponential(2) : x.toFixed(0); }
function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function levelBucket(level) {
  return LEVEL_BUCKETS.findIndex(([lo, hi]) => level >= lo && level <= hi);
}
function bucketLabel(i) {
  const [lo, hi] = LEVEL_BUCKETS[i];
  if (lo === hi) return String(lo);
  return hi === Infinity ? `${lo}+` : `${lo}–${hi}`;
}

// ---------- DOM helpers ----------

function el(tag, attrs, parent) {
  const e = document.createElementNS(SVGNS, tag);
  for (const k in attrs) e.setAttribute(k, attrs[k]);
  if (parent) parent.appendChild(e);
  return e;
}
function text(parent, x, y, str, cls, anchor = 'start') {
  const t = el('text', { x, y, class: cls, 'text-anchor': anchor }, parent);
  t.textContent = str;
  return t;
}

async function fetchJSON(url) {
  const r = await fetch(url);
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(body.error || `${r.status} ${r.statusText}`);
  return body;
}

// ---------- URL state ----------

function readURL() {
  const p = new URLSearchParams(location.search);
  for (const id of ['mode', 'sort', 'interval']) {
    const v = p.get(id);
    if (v && [...$('#' + id).options].some((o) => o.value === v)) $('#' + id).value = v;
  }
  if (p.has('filter')) $('#filter').value = p.get('filter');
  if (p.get('refresh') === '0') $('#refresh').checked = false;
  return { db: p.get('db'), table: p.get('table'), host: p.get('host') };
}

function writeURL() {
  const [db, table] = splitTable($('#table').value);
  const p = new URLSearchParams();
  if (db) { p.set('db', db); p.set('table', table); }
  if (state.hosts.length) p.set('host', $('#host').value);
  p.set('mode', $('#mode').value);
  p.set('sort', $('#sort').value);
  if ($('#filter').value) p.set('filter', $('#filter').value);
  if (!$('#refresh').checked) p.set('refresh', '0');
  p.set('interval', $('#interval').value);
  history.replaceState(null, '', '?' + p.toString());
}

function splitTable(v) {
  if (!v) return [null, null];
  const i = v.indexOf('\x00');
  return [v.slice(0, i), v.slice(i + 1)];
}

// ---------- loading ----------

async function init() {
  const want = readURL();
  try {
    const [config, tables] = await Promise.all([fetchJSON('api/config'), fetchJSON('api/tables')]);
    state.hosts = config.hosts || [];
    state.tables = tables || [];
  } catch (e) {
    showNotes([`Cannot reach ClickHouse: ${e.message}`], true);
    return;
  }

  if (state.hosts.length) {
    $('#host-wrap').hidden = false;
    for (const h of state.hosts) $('#host').add(new Option(h, h));
    if (want.host && state.hosts.includes(want.host)) $('#host').value = want.host;
  }

  const sel = $('#table');
  const groups = new Map();
  for (const t of state.tables) {
    if (!groups.has(t.database)) groups.set(t.database, []);
    groups.get(t.database).push(t);
  }
  for (const [db, list] of groups) {
    const og = document.createElement('optgroup');
    og.label = db;
    for (const t of list) {
      og.appendChild(new Option(`${t.name} — ${fmtNum(t.parts)} parts, ${fmtBytes(t.bytes)}`, `${t.database}\x00${t.name}`));
    }
    sel.appendChild(og);
  }
  const wanted = want.db && state.tables.find((t) => t.database === want.db && t.name === want.table);
  const first = wanted || state.tables.find((t) => t.database !== 'system' && t.parts > 0) || state.tables[0];
  if (!first) {
    showNotes(['No MergeTree tables found.'], true);
    return;
  }
  sel.value = `${first.database}\x00${first.name}`;

  sel.addEventListener('change', () => { state.minmax.clear(); state.data = null; reload(); });
  $('#host').addEventListener('change', () => { state.minmax.clear(); reload(); });
  for (const id of ['#mode', '#sort']) $(id).addEventListener('change', () => { writeURL(); render(); });
  $('#filter').addEventListener('input', () => { writeURL(); render(); });
  $('#refresh').addEventListener('change', () => { writeURL(); schedule(); });
  $('#interval').addEventListener('change', () => { writeURL(); schedule(); });
  $('#table-toggle').addEventListener('click', () => {
    state.showTable = !state.showTable;
    $('#table-toggle').textContent = state.showTable ? 'Chart view' : 'Table view';
    render();
  });
  window.addEventListener('resize', () => render());
  document.addEventListener('visibilitychange', () => { if (!document.hidden) reload(); });
  setupHover();

  reload();
}

function schedule() {
  clearTimeout(state.timer);
  if ($('#refresh').checked) state.timer = setTimeout(reload, Number($('#interval').value) * 1000);
}

async function reload() {
  clearTimeout(state.timer);
  writeURL();
  if (state.loading || document.hidden) { schedule(); return; }
  const [db, table] = splitTable($('#table').value);
  if (!db) return;
  const q = new URLSearchParams({ database: db, table });
  if (state.hosts.length) q.set('host', $('#host').value);
  state.loading = true;
  try {
    const data = await fetchJSON('api/parts?' + q);
    // Drop the response if the user picked another table while it loaded.
    if (splitTable($('#table').value).join('.') === `${data.table.database}.${data.table.name}`) {
      state.data = data;
      render();
    }
  } catch (e) {
    showNotes([`Load failed: ${e.message}`], true);
  } finally {
    state.loading = false;
    schedule();
  }
}

function minmaxFor(partitionID) {
  const d = state.data;
  const key = `${d.host || ''}\x00${d.table.database}\x00${d.table.name}\x00${partitionID}`;
  const cached = state.minmax.get(key);
  // Refetch if a part appeared that the cached result does not know.
  if (cached && !cached.stale) return cached.promise;
  const q = new URLSearchParams({ database: d.table.database, table: d.table.name, partition_id: partitionID });
  if (d.host) q.set('host', d.host);
  const entry = {
    stale: false,
    promise: fetchJSON('api/minmax?' + q).then((r) => {
      const byPart = new Map(r.parts.map((p) => [p.part, p]));
      return { columns: r.columns || [], byPart };
    }),
  };
  entry.promise.catch(() => state.minmax.delete(key));
  state.minmax.set(key, entry);
  return entry.promise;
}

function markMinmaxStale(partitionID, partName) {
  const d = state.data;
  const key = `${d.host || ''}\x00${d.table.database}\x00${d.table.name}\x00${partitionID}`;
  const e = state.minmax.get(key);
  if (!e) return;
  e.promise.then((r) => { if (!r.byPart.has(partName)) e.stale = true; }).catch(() => {});
}

// ---------- rendering ----------

function weight(p, mode) {
  switch (mode) {
    case 'linear': return p.bytes_on_disk;
    case 'log': return Math.log2(1 + p.bytes_on_disk);
    case 'blocks': return p.max_block_number - p.min_block_number + 1;
    case 'rows': return p.rows;
    default: return Math.sqrt(p.bytes_on_disk);
  }
}

function buildLanes(d) {
  const byPid = new Map();
  for (const p of d.parts) {
    if (!byPid.has(p.partition_id)) byPid.set(p.partition_id, []);
    byPid.get(p.partition_id).push(p);
  }
  let lanes = [...byPid].map(([pid, parts]) => ({
    pid,
    partition: parts[0].partition,
    parts,
    bytes: parts.reduce((a, p) => a + p.bytes_on_disk, 0),
    rows: parts.reduce((a, p) => a + p.rows, 0),
  }));
  const f = $('#filter').value.trim().toLowerCase();
  if (f) lanes = lanes.filter((l) => l.pid.toLowerCase().includes(f) || l.partition.toLowerCase().includes(f));
  const cmp = {
    asc: (a, b) => a.pid.localeCompare(b.pid, undefined, { numeric: true }),
    desc: (a, b) => b.pid.localeCompare(a.pid, undefined, { numeric: true }),
    parts: (a, b) => b.parts.length - a.parts.length,
    bytes: (a, b) => b.bytes - a.bytes,
  }[$('#sort').value];
  return lanes.sort(cmp);
}

function render() {
  const d = state.data;
  if (!d) return;
  renderSummary(d);
  const lanes = buildLanes(d);

  $('#chart').hidden = state.showTable;
  $('#table-view').hidden = !state.showTable;
  if (state.showTable) { renderTable(d, lanes); return; }

  const svg = $('#svg');
  svg.replaceChildren();
  state.hover = [];
  const defs = el('defs', {}, svg);
  const pat = el('pattern', { id: 'hatch', width: 6, height: 6, patternUnits: 'userSpaceOnUse', patternTransform: 'rotate(45)' }, defs);
  el('line', { x1: 0, y1: 0, x2: 0, y2: 6, class: 'hatch-line' }, pat);

  if (!lanes.length) {
    svg.setAttribute('width', 400);
    svg.setAttribute('height', 40);
    text(svg, PAD, 24, d.parts.length ? 'No partition matches the filter.' : 'This table has no active parts.', 'lane-sub');
    return;
  }

  const mode = $('#mode').value;
  const avail = Math.max(200, $('#chart').clientWidth - 32 - PAD * 2);
  let scale = Infinity;
  for (const l of lanes) {
    const total = l.parts.reduce((a, p) => a + weight(p, mode), 0);
    const room = avail - GAP * (l.parts.length - 1) - MIN_W * l.parts.length;
    if (total > 0) scale = Math.min(scale, Math.max(room, avail * 0.1) / total);
  }
  if (!isFinite(scale)) scale = 1;

  const mergeBy = new Map();
  for (const m of d.merges) for (const n of m.source_part_names) mergeBy.set(n, m);
  const queuedBy = new Map();
  for (const q of d.queued) for (const n of q.parts_to_merge) queuedBy.set(n, q);
  const candBy = new Map();
  for (const c of d.candidates) for (const n of c.parts) candBy.set(n, c);

  let maxX = 0;
  lanes.forEach((lane, li) => {
    const top = li * LANE_H;
    const g = el('g', { transform: `translate(0,${top})` }, svg);
    text(g, PAD, 14, lane.partition === lane.pid ? lane.pid : `${lane.partition}  (${lane.pid})`, 'lane-title');
    const titleW = g.lastChild.getComputedTextLength();
    text(g, PAD + titleW + 12, 14, `${fmtNum(lane.parts.length)} parts · ${fmtBytes(lane.bytes)} · ${fmtNum(lane.rows)} rows`, 'lane-sub');

    const barY = HEADER_H + TRACK_H * TRACKS + 4;
    const pos = new Map();
    let x = PAD;
    for (const p of lane.parts) {
      const w = Math.max(MIN_W, weight(p, mode) * scale);
      pos.set(p.name, [x, w]);
      const cls = `part lv${levelBucket(p.level)}${mergeBy.has(p.name) ? ' merging' : ''}`;
      const r = el('rect', { x, y: barY, width: w, height: BAR_H, rx: Math.min(2, w / 2), class: cls }, g);
      r.dataset.h = state.hover.push({ kind: 'part', part: p, lane, merge: mergeBy.get(p.name), queued: queuedBy.get(p.name), cand: candBy.get(p.name) }) - 1;
      x += w + GAP;
    }
    const laneEnd = x - GAP;
    maxX = Math.max(maxX, laneEnd);

    // Bracket for each group of source parts.
    const span = (names) => {
      let x0 = Infinity, x1 = -Infinity;
      for (const n of names) {
        const pw = pos.get(n);
        if (!pw) continue;
        x0 = Math.min(x0, pw[0]);
        x1 = Math.max(x1, pw[0] + pw[1]);
      }
      return x0 === Infinity ? null : [x0, x1];
    };
    const trackY = (i) => HEADER_H + i * TRACK_H + 2;
    const bh = TRACK_H - 4;

    for (const m of new Set(lane.parts.map((p) => mergeBy.get(p.name)).filter(Boolean))) {
      const s = span(m.source_part_names);
      if (!s) continue;
      const y = trackY(0);
      el('rect', { x: s[0], y, width: s[1] - s[0], height: bh, rx: 2, class: 'm-bg' }, g);
      el('rect', { x: s[0], y, width: Math.max(1, (s[1] - s[0]) * m.progress), height: bh, rx: 2, class: 'm-fg' }, g);
      hit(g, s[0], y, s[1] - s[0], bh, { kind: 'merge', merge: m });
    }
    for (const q of new Set(lane.parts.map((p) => queuedBy.get(p.name)).filter(Boolean))) {
      const s = span(q.parts_to_merge);
      if (!s) continue;
      const y = trackY(1);
      el('rect', { x: s[0], y, width: s[1] - s[0], height: bh, rx: 2, class: 'q-bg' }, g);
      hit(g, s[0], y, s[1] - s[0], bh, { kind: 'queued', queued: q });
    }
    for (const c of new Set(lane.parts.map((p) => candBy.get(p.name)).filter(Boolean))) {
      const s = span(c.parts);
      if (!s) continue;
      const y = trackY(2);
      el('rect', { x: s[0], y, width: s[1] - s[0], height: bh, rx: 2, class: `c-bg${c.rank === 1 ? ' next' : ''}` }, g);
      if (s[1] - s[0] > 24) text(g, s[0] + 4, y + bh - 2, `#${c.rank}`, 'bracket-label');
      hit(g, s[0], y, s[1] - s[0], bh, { kind: 'candidate', cand: c });
    }

    // Block number axis: a label at each part's left edge, thinned when crowded.
    const ax = el('g', { class: 'axis' }, g);
    const axY = barY + BAR_H;
    el('line', { x1: PAD, x2: laneEnd, y1: axY + 0.5, y2: axY + 0.5, class: 'baseline' }, ax);
    let lastRight = -Infinity;
    let lastText = null;
    const label = (lx, str, anchor) => {
      const w = str.length * 6 + 6;
      const left = anchor === 'end' ? lx - w : lx;
      if (left < lastRight) return false;
      el('line', { x1: lx + 0.5, x2: lx + 0.5, y1: axY, y2: axY + 4 }, ax);
      text(ax, lx, axY + 15, str, '', anchor);
      lastRight = left + w;
      lastText = str;
      return true;
    };
    const endLabel = String(lane.parts[lane.parts.length - 1].max_block_number);
    const endW = endLabel.length * 6 + 6;
    for (const p of lane.parts) {
      const [px] = pos.get(p.name);
      const s = String(p.min_block_number);
      if (p !== lane.parts[0] && px + s.length * 6 + 6 > laneEnd - endW) break;
      label(px, s, 'start');
    }
    if (endLabel !== lastText) label(laneEnd, endLabel, 'end');
  });

  svg.setAttribute('width', Math.max(maxX + PAD, 400));
  svg.setAttribute('height', lanes.length * LANE_H);

  if (state.lastPointer) showHover(state.lastPointer);
}

function hit(g, x, y, w, h, obj) {
  const r = el('rect', { x, y: y - 2, width: Math.max(w, 4), height: h + 4, class: 'hit' }, g);
  r.dataset.h = state.hover.push(obj) - 1;
}

function renderSummary(d) {
  const partitions = new Set(d.parts.map((p) => p.partition_id)).size;
  const bytes = d.parts.reduce((a, p) => a + p.bytes_on_disk, 0);
  const maxInPartition = Math.max(0, ...Object.values(d.parts.reduce((a, p) => ((a[p.partition_id] = (a[p.partition_id] || 0) + 1), a), {})));
  const stats = [
    ['Active parts', fmtNum(d.parts.length)],
    ['Partitions', fmtNum(partitions)],
    ['Max parts in a partition', fmtNum(maxInPartition)],
    ['On disk', fmtBytes(bytes)],
    ['Merges running', fmtNum(d.merges.length)],
  ];
  if (/^(Replicated|Shared)/.test(d.table.engine)) stats.push(['Merges queued', fmtNum(d.queued.length)]);
  stats.push(['Candidates', fmtNum(d.candidates.length)]);
  $('#stats').innerHTML = stats.map(([k, v]) => `<div class="stat"><div class="v">${esc(v)}</div><div class="k">${esc(k)}</div></div>`).join('');

  const used = new Set(d.parts.map((p) => levelBucket(p.level)));
  const sw = (fill, extra = '') => `<svg width="14" height="12"><rect x="1" y="1" width="12" height="10" rx="2" style="fill:${fill}" ${extra}/></svg>`;
  let html = '<span>Level</span>';
  LEVEL_BUCKETS.forEach((_, i) => { if (used.has(i)) html += `<span class="item">${sw(`var(--lv${i})`)}${esc(bucketLabel(i))}</span>`; });
  html += `<span class="item"><svg width="22" height="12"><rect x="1" y="2" width="20" height="8" rx="2" class="m-bg"/><rect x="1" y="2" width="12" height="8" rx="2" class="m-fg"/></svg>Merge running (fill = progress)</span>`;
  if (/^(Replicated|Shared)/.test(d.table.engine)) html += `<span class="item"><svg width="22" height="12"><rect x="1" y="2" width="20" height="8" rx="2" class="q-bg"/></svg>Merge queued</span>`;
  html += `<span class="item"><svg width="22" height="12"><rect x="1" y="2" width="20" height="8" rx="2" class="c-bg next"/></svg>Next pick (estimate)</span>`;
  html += `<span class="item"><svg width="22" height="12"><rect x="1" y="2" width="20" height="8" rx="2" class="c-bg"/></svg>Other candidates</span>`;
  html += `<span>Width: ${esc($('#mode').selectedOptions[0].text.toLowerCase())}</span>`;
  $('#legend').innerHTML = html;

  const notes = [];
  if (d.selector_note) notes.push(d.selector_note);
  if (!d.part_log) notes.push('system.part_log is not enabled, so part creation times are not known.');
  showNotes(notes, false, d.warnings || []);
}

function showNotes(notes, isError, warnings = []) {
  $('#notes').innerHTML =
    notes.map((n) => `<div class="${isError ? 'warn' : ''}">${esc(n)}</div>`).join('') +
    warnings.map((w) => `<div class="warn">${esc(w)}</div>`).join('');
}

function renderTable(d, lanes) {
  const rows = lanes.flatMap((l) => l.parts).slice(0, 5000);
  const head = ['Part', 'Partition', 'Min block', 'Max block', 'Level', 'Rows', 'On disk', 'Modified', 'State'];
  const body = rows.map((p) => {
    const st = d.merges.some((m) => m.source_part_names.includes(p.name)) ? 'merging'
      : d.queued.some((q) => q.parts_to_merge.includes(p.name)) ? 'queued'
      : d.candidates.find((c) => c.parts.includes(p.name)) ? `candidate #${d.candidates.find((c) => c.parts.includes(p.name)).rank}` : '';
    return `<tr><td>${esc(p.name)}</td><td>${esc(p.partition)}</td><td>${p.min_block_number}</td><td>${p.max_block_number}</td>
      <td>${p.level}</td><td>${fmtNum(p.rows)}</td><td>${fmtBytes(p.bytes_on_disk)}</td><td>${fmtTime(p.modification_time)}</td><td>${esc(st)}</td></tr>`;
  }).join('');
  $('#table-view').innerHTML = rows.length
    ? `<table><thead><tr>${head.map((h) => `<th>${h}</th>`).join('')}</tr></thead><tbody>${body}</tbody></table>`
    : '<div class="empty">No parts.</div>';
}

// ---------- hover ----------

function setupHover() {
  const svg = $('#svg');
  svg.addEventListener('pointermove', (e) => { state.lastPointer = { x: e.clientX, y: e.clientY }; showHover(state.lastPointer); });
  svg.addEventListener('pointerleave', () => { state.lastPointer = null; hideTooltip(); });
  $('#chart').addEventListener('scroll', () => { if (state.lastPointer) showHover(state.lastPointer); });
}

let hoverKey = null;
function showHover(pt) {
  const target = document.elementFromPoint(pt.x, pt.y);
  const idx = target && target.dataset ? target.dataset.h : undefined;
  if (idx === undefined) { hideTooltip(); return; }
  const obj = state.hover[idx];
  document.querySelectorAll('.part.hl').forEach((n) => n.classList.remove('hl'));
  const tip = $('#tooltip');
  const key = obj.kind === 'part' ? 'p:' + obj.part.name : obj.kind + ':' + idx;
  if (key !== hoverKey || tip.hidden) {
    hoverKey = key;
    tip.innerHTML = tooltipHTML(obj);
    tip.hidden = false;
    if (obj.kind === 'part') loadMinmax(obj);
  }
  if (obj.kind !== 'part') highlight(obj);
  placeTooltip(tip, pt);
}

function highlight(obj) {
  const names = new Set(obj.kind === 'merge' ? obj.merge.source_part_names : obj.kind === 'queued' ? obj.queued.parts_to_merge : obj.cand.parts);
  $('#svg').querySelectorAll('.part').forEach((n) => {
    const h = state.hover[n.dataset.h];
    if (h && names.has(h.part.name)) n.classList.add('hl');
  });
}

function hideTooltip() {
  $('#tooltip').hidden = true;
  hoverKey = null;
  document.querySelectorAll('.part.hl').forEach((n) => n.classList.remove('hl'));
}

function placeTooltip(tip, pt) {
  const r = tip.getBoundingClientRect();
  let x = pt.x + 14, y = pt.y + 14;
  if (x + r.width > window.innerWidth - 8) x = pt.x - r.width - 14;
  if (y + r.height > window.innerHeight - 8) y = Math.max(8, window.innerHeight - r.height - 8);
  tip.style.left = `${Math.max(8, x)}px`;
  tip.style.top = `${y}px`;
}

function rowsHTML(rows) {
  return rows.filter(Boolean).map(([k, v, cls]) =>
    k === '§' ? `<tr class="sep"><td colspan="2">${esc(v)}</td></tr>` : `<tr><td>${esc(k)}</td><td class="${cls || ''}">${esc(v)}</td></tr>`).join('');
}

function tooltipHTML(obj) {
  const now = state.data.now;
  if (obj.kind === 'merge') {
    const m = obj.merge;
    return `<h3>${esc(m.result_part_name)}</h3><table>${rowsHTML([
      ['Type', `${m.is_mutation ? 'mutation' : 'merge'} · ${m.merge_type} · ${m.merge_algorithm}`],
      ['Source parts', fmtNum(m.num_parts || m.source_part_names.length)],
      ['Progress', `${(m.progress * 100).toFixed(1)}%`],
      ['Elapsed', fmtDuration(m.elapsed)],
      m.progress > 0.01 ? ['Remaining (est.)', fmtDuration((m.elapsed / m.progress) * (1 - m.progress))] : null,
      ['Compressed size', fmtBytes(m.total_size_bytes_compressed)],
      ['Rows read', fmtNum(m.rows_read)],
      ['Read / written (uncompressed)', `${fmtBytes(m.bytes_read_uncompressed)} / ${fmtBytes(m.bytes_written_uncompressed)}`],
    ])}</table>`;
  }
  if (obj.kind === 'queued') {
    const q = obj.queued;
    return `<h3>${esc(q.new_part_name)}</h3><table>${rowsHTML([
      ['State', q.is_currently_executing ? 'executing' : 'queued'],
      ['Source parts', fmtNum(q.parts_to_merge.length)],
      ['Created', fmtTimeAgo(q.create_time, now)],
      ['Tries', fmtNum(q.num_tries)],
      q.postpone_reason ? ['Postponed', q.postpone_reason] : null,
      q.last_exception ? ['Last error', q.last_exception] : null,
    ])}</table>`;
  }
  if (obj.kind === 'candidate') {
    const c = obj.cand;
    return `<h3>Candidate #${c.rank}${c.rank === 1 ? ' (next pick)' : ''}</h3><table>${rowsHTML([
      ['Parts', fmtNum(c.parts.length)],
      ['From', c.parts[0], 'mono'],
      ['To', c.parts[c.parts.length - 1], 'mono'],
      ['Size', fmtBytes(c.sum_bytes)],
      ['Rows', fmtNum(c.sum_rows)],
      ['Score (lower is better)', fmtScore(c.score)],
      ['§', 'Estimate from a port of SimpleMergeSelector. ClickHouse also uses free pool slots and disk space.'],
    ])}</table>`;
  }

  const p = obj.part;
  const rows = [
    ['Partition', p.partition === p.partition_id ? p.partition : `${p.partition} (${p.partition_id})`],
    ['Blocks', `${fmtNum(p.min_block_number)} – ${fmtNum(p.max_block_number)} (${fmtNum(p.max_block_number - p.min_block_number + 1)})`],
    ['Level', String(p.level)],
    p.data_version !== p.min_block_number ? ['Data version', String(p.data_version)] : null,
    ['Rows', fmtNum(p.rows)],
    ['Marks', fmtNum(p.marks)],
    ['On disk', fmtBytes(p.bytes_on_disk)],
    ['Compressed / uncompressed', `${fmtBytes(p.data_compressed_bytes)} / ${fmtBytes(p.data_uncompressed_bytes)}` +
      (p.data_compressed_bytes ? ` (×${(p.data_uncompressed_bytes / p.data_compressed_bytes).toFixed(1)})` : '')],
    ['Type / disk', `${p.part_type} · ${p.disk_name}`],
    ['§', 'Time'],
    ['Modified', fmtTimeAgo(p.modification_time, now)],
    p.created_at ? ['Created', `${fmtTimeAgo(p.created_at, now)} by ${p.created_event}`] : null,
    p.created_event === 'MergeParts' && p.duration_ms ? ['Merge took', `${fmtDuration(p.duration_ms / 1000)} from ${fmtNum(p.merged_from)} parts`] : null,
  ];
  if (p.min_date && p.min_date !== '1970-01-01') rows.push(['Min / max date', `${p.min_date} – ${p.max_date}`]);
  if (p.min_time) rows.push(['Min / max time', `${fmtTime(p.min_time)} – ${fmtTime(p.max_time)}`]);
  if (obj.merge) rows.push(['§', 'State'], ['Merging into', obj.merge.result_part_name, 'mono']);
  else if (obj.queued) rows.push(['§', 'State'], ['Queued merge into', obj.queued.new_part_name, 'mono']);
  else if (obj.cand) rows.push(['§', 'State'], ['Candidate', `#${obj.cand.rank} of ${obj.cand.parts.length} parts`]);
  const cols = state.data.table.minmax_columns || [];
  let mm = '';
  if (cols.length) mm = `<tr class="sep"><td colspan="2">Minmax index</td></tr><tbody id="mm"><tr><td colspan="2">Loading…</td></tr></tbody>`;
  return `<h3>${esc(p.name)}</h3><table>${rowsHTML(rows)}${mm}</table>`;
}

function loadMinmax(obj) {
  const cols = state.data.table.minmax_columns || [];
  if (!cols.length) return;
  const p = obj.part;
  markMinmaxStale(p.partition_id, p.name);
  const key = hoverKey;
  minmaxFor(p.partition_id).then((r) => {
    if (hoverKey !== key) return;
    const tb = document.getElementById('mm');
    if (!tb) return;
    const e = r.byPart.get(p.name);
    tb.innerHTML = e
      ? rowsHTML(r.columns.map((c, i) => [c, e.min[i] === e.max[i] ? e.min[i] : `${e.min[i]} – ${e.max[i]}`, 'mono']))
      : rowsHTML([['', 'Not found (part may be gone)']]);
    placeTooltip($('#tooltip'), state.lastPointer || { x: 0, y: 0 });
  }).catch((err) => {
    const tb = document.getElementById('mm');
    if (tb && hoverKey === key) tb.innerHTML = rowsHTML([['Error', err.message]]);
  });
}

init();
