'use strict';

// ============================================================ 基础工具

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const sleep = ms => new Promise(r => setTimeout(r, ms));

const TOKEN = (location.hash.match(/t=([0-9a-f]+)/) || [])[1] || sessionStorage.getItem('fo-token') || '';
if (TOKEN) sessionStorage.setItem('fo-token', TOKEN);
window.addEventListener('hashchange', () => {
  const t = (location.hash.match(/t=([0-9a-f]+)/) || [])[1];
  if (t && t !== TOKEN) location.reload();
});

function setOffline(v) { $('#offline').classList.toggle('hidden', !v); }

async function api(method, path, body) {
  let r;
  try {
    r = await fetch(path, {
      method,
      headers: { 'X-Token': TOKEN, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (e) {
    setOffline(true);
    throw new Error('无法连接到程序，它可能已经被关闭');
  }
  setOffline(false);
  let j = null;
  try { j = await r.json(); } catch { /* 忽略 */ }
  if (!r.ok) throw new Error((j && j.error) || `请求失败（${r.status}）`);
  return j;
}
const GET = p => api('GET', p);
const POST = (p, b) => api('POST', p, b || {});

function fmtSize(n) {
  if (!n) return '0 B';
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return (n >= 100 ? n.toFixed(0) : n >= 10 ? n.toFixed(1) : n.toFixed(2)) + ' ' + u[i];
}
const fmtNum = n => (n || 0).toLocaleString('zh-CN');
function fmtTime(sec) {
  if (!sec) return '';
  const d = new Date(sec * 1000), p = x => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
function fmtDur(ms) {
  if (ms < 1000) return ms + ' 毫秒';
  if (ms < 60000) return (ms / 1000).toFixed(1) + ' 秒';
  return Math.floor(ms / 60000) + ' 分 ' + Math.round((ms % 60000) / 1000) + ' 秒';
}
function extOf(name) {
  const i = name.lastIndexOf('.');
  return i <= 0 || i === name.length - 1 ? '' : name.slice(i + 1).toLowerCase();
}
function debounce(fn, ms) {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
}

const ICON_FOLDER = '<svg viewBox="0 0 24 24" class="ficon"><path d="M2 6a2 2 0 0 1 2-2h5l2 2h9a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2z"/></svg>';
const ICON_FOLDER_ERR = '<svg viewBox="0 0 24 24" class="ficon err"><path d="M2 6a2 2 0 0 1 2-2h5l2 2h9a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2z"/></svg>';
const ICON_CARET = '<svg viewBox="0 0 24 24"><path d="m9 6 6 6-6 6"/></svg>';
const ICON_DRIVE = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"><rect x="3" y="6" width="18" height="12" rx="2.5" fill="var(--panel-2)"/><path d="M7 14h.01M11 14h6"/></svg>';

// ============================================================ 全局状态

const S = {
  root: null,          // 扫描根目录信息
  rootNode: null,      // 树根节点 {id,name,size,...}
  cur: 0,              // 当前文件夹 ID
  curInfo: null,
  recursive: false,
  filter: { ext: '', cat: '', q: '' },
  sort: { key: 'name', desc: false },
  rows: [], total: 0, totalSize: 0, fileCount: 0, dirCount: 0,
  loading: false, listSeq: 0,
  sel: new Map(), selAll: false, anchor: -1,
  treeSort: localStorage.getItem('fo-tree-sort') || 'size',
  expanded: new Set(), treeKids: new Map(),
  statsOpen: new Set(), stats: null,
  recent: [],
  cats: [], catByKey: {}, extCat: {},
  places: [], os: '', undoable: 0, logPath: '',
  dragBody: null, dragCount: 0,
  restorePath: null, lastScan: null,
  view: localStorage.getItem('fo-view') === 'grid' ? 'grid' : 'list',
  thumbExts: new Set(), quick: [],
};
const trashName = () => S.os === 'windows' ? '回收站' : '废纸篓';

function applyState(st) {
  S.os = st.os;
  S.logPath = st.logPath;
  S.undoable = st.undoable;
  if (st.thumbExts) S.thumbExts = new Set(st.thumbExts);
  if (st.categories && !S.cats.length) {
    S.cats = st.categories;
    for (const c of S.cats) {
      S.catByKey[c.key] = c;
      for (const e of c.exts || []) if (!(e in S.extCat)) S.extCat[e] = c.key;
    }
  }
  $('#btnUndo').disabled = !S.undoable;
  $('#btnUndo').title = S.undoable ? `撤销上一步操作 (Ctrl+Z)，还可撤销 ${S.undoable} 步` : '没有可以撤销的操作';
}

async function refreshState() {
  const st = await GET('/api/state');
  applyState(st);
  if (st.root) { S.root = st.root; renderScanInfo(); }
  return st;
}

function catOfExt(ext) {
  if (!ext) return S.catByKey.noext || { name: '无扩展名', color: '#a0a0a0' };
  return S.catByKey[S.extCat[ext]] || S.catByKey.other || { name: '其他', color: '#b5b5b5' };
}
function extBadge(ext) {
  const c = catOfExt(ext);
  return `<span class="ext-badge" style="background:${c.color}">${esc(ext ? ext.slice(0, 4) : '—')}</span>`;
}

// ============================================================ 提示、对话框、菜单

function toast(msg, opts = {}) {
  const el = document.createElement('div');
  el.className = 'toast' + (opts.err ? ' err' : '');
  el.innerHTML = `<span class="tmsg">${esc(msg)}</span>`;
  if (opts.action) {
    const b = document.createElement('button');
    b.textContent = opts.action;
    b.onclick = () => { el.remove(); opts.onAction(); };
    el.appendChild(b);
  }
  $('#toasts').appendChild(el);
  setTimeout(() => el.remove(), opts.ms || (opts.action ? 8000 : 4000));
}
const toastErr = e => toast(e.message || String(e), { err: true, ms: 7000 });

const modals = [];
function modal({ title, body, foot, wide, onClose }) {
  const back = document.createElement('div');
  back.className = 'modal-back';
  back.innerHTML = `<div class="modal${wide ? ' wide' : ''}" role="dialog">
    <div class="modal-head"><h3>${esc(title)}</h3><button class="x" title="关闭">×</button></div>
    <div class="modal-body"></div>
    <div class="modal-foot"></div></div>`;
  const m = { el: back, body: $('.modal-body', back), foot: $('.modal-foot', back), close };
  if (typeof body === 'string') m.body.innerHTML = body; else if (body) m.body.appendChild(body);
  if (foot) m.foot.innerHTML = foot; else m.foot.remove();
  $('.x', back).onclick = () => close();
  back.addEventListener('mousedown', e => { if (e.target === back) back._downOnBack = true; });
  back.addEventListener('click', e => { if (e.target === back && back._downOnBack) close(); back._downOnBack = false; });
  function close(v) {
    const i = modals.indexOf(m);
    if (i >= 0) modals.splice(i, 1);
    back.remove();
    onClose && onClose(v);
  }
  $('#modalRoot').appendChild(back);
  modals.push(m);
  return m;
}

function confirmDlg(title, msg, okText = '确定', danger = false) {
  return new Promise(res => {
    const m = modal({
      title, body: `<div style="line-height:1.7;white-space:pre-wrap">${esc(msg)}</div>`,
      foot: `<button class="btn" data-a="no">取消</button><button class="btn ${danger ? 'danger' : 'primary'}" data-a="ok">${esc(okText)}</button>`,
      onClose: v => res(!!v),
    });
    $('[data-a=no]', m.foot).onclick = () => m.close(false);
    $('[data-a=ok]', m.foot).onclick = () => m.close(true);
    setTimeout(() => $('[data-a=ok]', m.foot).focus(), 0);
  });
}

function promptDlg(title, label, value, { selectBase = false, okText = '确定' } = {}) {
  return new Promise(res => {
    const m = modal({
      title,
      body: `<label class="field-label">${esc(label)}</label><input type="text" style="width:100%" spellcheck="false"><div class="error hidden"></div>`,
      foot: `<button class="btn" data-a="no">取消</button><button class="btn primary" data-a="ok">${esc(okText)}</button>`,
      onClose: v => res(v ?? null),
    });
    const input = $('input', m.body);
    input.value = value || '';
    const ok = () => { const v = input.value.trim(); if (v) m.close(v); };
    $('[data-a=no]', m.foot).onclick = () => m.close(null);
    $('[data-a=ok]', m.foot).onclick = ok;
    input.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); ok(); } });
    setTimeout(() => {
      input.focus();
      const dot = selectBase ? input.value.lastIndexOf('.') : -1;
      input.setSelectionRange(0, dot > 0 ? dot : input.value.length);
    }, 0);
  });
}

function errorListDlg(title, errors) {
  const m = modal({
    title,
    body: `<div style="max-height:360px;overflow:auto;line-height:1.7">${errors.map(e => `<div>• ${esc(e)}</div>`).join('')}</div>`,
    foot: '<button class="btn primary">知道了</button>',
  });
  $('button', m.foot).onclick = () => m.close();
}

function showMenu(x, y, items) {
  const menu = $('#menu');
  menu.innerHTML = '';
  for (const it of items) {
    if (it === '-') { menu.appendChild(document.createElement('hr')); continue; }
    if (!it) continue;
    if (it.header) {
      const h = document.createElement('div');
      h.className = 'menu-head';
      h.textContent = it.header;
      menu.appendChild(h);
      continue;
    }
    const b = document.createElement('button');
    b.innerHTML = `${it.icon || ''}<span>${esc(it.label)}</span>${it.sub ? `<small>${esc(it.sub)}</small>` : ''}${it.kbd ? `<kbd>${esc(it.kbd)}</kbd>` : ''}`;
    if (it.danger) b.classList.add('danger');
    if (it.title) b.title = it.title;
    b.disabled = !!it.disabled;
    b.onclick = () => { hideMenu(); it.action(); };
    menu.appendChild(b);
  }
  menu.classList.remove('hidden');
  const r = menu.getBoundingClientRect();
  menu.style.left = Math.min(x, innerWidth - r.width - 6) + 'px';
  menu.style.top = Math.min(y, innerHeight - r.height - 6) + 'px';
}
function hideMenu() { $('#menu').classList.add('hidden'); }
document.addEventListener('mousedown', e => { if (!e.target.closest('#menu')) hideMenu(); });
window.addEventListener('blur', hideMenu);

async function busy(fn) {
  document.body.classList.add('busy');
  try { return await fn(); } finally { document.body.classList.remove('busy'); }
}

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); }
  catch {
    const ta = document.createElement('textarea');
    ta.value = text; document.body.appendChild(ta); ta.select();
    document.execCommand('copy'); ta.remove();
  }
  toast('已复制：' + text);
}

// ============================================================ 欢迎页 / 扫描

function showWelcome(asOverlay) {
  $('#welcome').classList.remove('hidden');
  $('#welcome').classList.toggle('overlay', !!asOverlay);
  $('#btnWelcomeBack').classList.toggle('hidden', !asOverlay);
  $('#welcomeForm').classList.remove('hidden');
  $('#welcomeProgress').classList.add('hidden');
  if (!$('#scanPath').value && S.root) $('#scanPath').value = S.root.path;
  setTimeout(() => $('#scanPath').focus(), 0);
}

function welcomeError(msg) {
  const el = $('#welcomeErr');
  el.textContent = msg || '';
  el.classList.toggle('hidden', !msg);
}

async function loadPlaces() {
  try { S.places = await GET('/api/places'); } catch { S.places = []; }
  const box = $('#places');
  box.innerHTML = S.places.map((p, i) => {
    const used = p.total ? (p.total - p.free) / p.total * 100 : 0;
    const icon = p.kind === 'drive' ? ICON_DRIVE : ICON_FOLDER.replace('class="ficon"', 'class="ficon" style="width:26px;height:26px"');
    return `<button class="place" data-i="${i}" title="${esc(p.path)}">${icon}<div>
      <div class="pn">${esc(p.name)}</div>
      ${p.total ? `<div class="ps">可用 ${fmtSize(p.free)} / 共 ${fmtSize(p.total)}</div><div class="pbar"><i style="width:${used.toFixed(1)}%"></i></div>` : `<div class="ps">${esc(p.path)}</div>`}
    </div></button>`;
  }).join('');
}

$('#places').addEventListener('click', e => {
  const b = e.target.closest('.place');
  if (!b) return;
  $$('.place').forEach(x => x.classList.toggle('on', x === b));
  $('#scanPath').value = S.places[+b.dataset.i].path;
});
$('#places').addEventListener('dblclick', e => { if (e.target.closest('.place')) $('#btnScan').click(); });
$('#scanPath').addEventListener('keydown', e => { if (e.key === 'Enter') $('#btnScan').click(); });
$('#scanPath').addEventListener('input', () => $$('.place').forEach(x => x.classList.remove('on')));
$('#btnBrowse').onclick = () => openBrowseDialog($('#scanPath').value.trim());
$('#btnWelcomeBack').onclick = () => $('#welcome').classList.add('hidden');
$('#btnScan').onclick = () => startScan($('#scanPath').value, $('#skipHidden').checked);
$('#btnCancelScan').onclick = async () => {
  await POST('/api/scan/cancel').catch(() => {});
  showWelcome(!!S.root);
};

async function startScan(path, skipHidden) {
  welcomeError('');
  if (!path.trim()) { welcomeError('请先选择或输入一个磁盘/文件夹'); return; }
  try {
    await POST('/api/scan', { path, skipHidden });
  } catch (e) { welcomeError(e.message); return; }
  S.lastScan = { path, skipHidden };
  $('#welcome').classList.remove('hidden');
  $('#welcomeForm').classList.add('hidden');
  $('#welcomeProgress').classList.remove('hidden');
  pollScan();
}

async function pollScan() {
  for (;;) {
    let st;
    try { st = await GET('/api/state'); } catch { await sleep(1000); continue; }
    const p = st.scan;
    $('#progPath').textContent = S.lastScan ? S.lastScan.path : '';
    $('#progFiles').textContent = fmtNum(p.files);
    $('#progDirs').textContent = fmtNum(p.dirs);
    $('#progBytes').textContent = fmtSize(p.bytes);
    $('#progTime').textContent = fmtDur(p.elapsedMs);
    $('#progCurrent').textContent = p.current || '';
    if (p.running) { await sleep(250); continue; }
    if (p.error) { showWelcome(!!S.root); if (p.error !== '已取消扫描') welcomeError(p.error); return; }
    applyState(st);
    if (st.scanned) await enterApp(st);
    return;
  }
}

function renderScanInfo() {
  const r = S.root;
  if (!r) return;
  $('#rootPath').textContent = r.path;
  $('#rootPath').title = r.path;
  $('#scanInfo').textContent = `共 ${fmtNum(r.nFiles)} 个文件 · ${fmtNum(r.nDirs)} 个文件夹 · ${fmtSize(r.size)} · 扫描用时 ${fmtDur(r.tookMs)}` +
    (r.errors ? ` · ${fmtNum(r.errors)} 个文件夹无法访问` : '');
}

async function enterApp(st) {
  S.root = st.root;
  $('#welcome').classList.add('hidden');
  $('#app').classList.remove('hidden');
  renderScanInfo();
  S.expanded = new Set([S.root.id]);
  S.treeKids.clear();
  S.recent = [];
  S.statsOpen.clear();
  let target = S.root.id;
  if (S.restorePath) {
    try {
      const r = await GET('/api/locate?path=' + encodeURIComponent(S.restorePath));
      if (r.id) target = r.id;
    } catch { /* 忽略 */ }
    S.restorePath = null;
  }
  await refreshTree();
  await openDir(target);
  loadQuick();
}

$('#btnRescan').onclick = () => {
  if (!S.root) return;
  S.restorePath = S.curInfo ? S.curInfo.path : null;
  startScan(S.root.path, S.root.skipHidden);
};
$('#btnChangeRoot').onclick = () => { $('#scanPath').value = ''; showWelcome(true); };

// ============================================================ 选择文件夹对话框（扫描前）

async function openBrowseDialog(start) {
  const body = document.createElement('div');
  body.innerHTML = `<div class="pathrow" style="margin-bottom:10px">
      <button class="btn" data-a="up" title="上一级">↑ 上一级</button>
      <input type="text" data-a="path" spellcheck="false" placeholder="输入路径后按回车">
    </div>
    <div class="browse">
      <div class="browse-places">${S.places.map((p, i) => `<button data-i="${i}" title="${esc(p.path)}">${esc(p.name)}</button>`).join('')}</div>
      <div class="browse-list"></div>
    </div>
    <div class="hint" style="margin-top:8px">单击选中，双击进入文件夹。</div>`;
  const m = modal({
    title: '选择要整理的文件夹', body, wide: true,
    foot: '<span class="left muted small" data-a="sel"></span><button class="btn" data-a="no">取消</button><button class="btn primary" data-a="ok">选择此文件夹</button>',
  });
  let cur = null, picked = null;
  const list = $('.browse-list', body), input = $('[data-a=path]', body);
  const updateSel = () => { $('[data-a=sel]', m.foot).textContent = picked || cur?.path || ''; };
  async function go(path) {
    if (!path) { list.innerHTML = '<div class="empty">请从左侧选择一个位置</div>'; return; }
    try {
      cur = await GET('/api/browse?path=' + encodeURIComponent(path));
    } catch (e) { toastErr(e); return; }
    picked = null;
    input.value = cur.path;
    list.innerHTML = cur.dirs.length
      ? cur.dirs.map((d, i) => `<div class="browse-item" data-i="${i}">${ICON_FOLDER}<span>${esc(d.name)}</span></div>`).join('')
      : `<div class="empty">${cur.error ? esc(cur.error) : '没有子文件夹'}</div>`;
    updateSel();
  }
  list.addEventListener('click', e => {
    const it = e.target.closest('.browse-item');
    if (!it) return;
    $$('.browse-item', list).forEach(x => x.classList.toggle('on', x === it));
    picked = cur.dirs[+it.dataset.i].path;
    updateSel();
  });
  list.addEventListener('dblclick', e => {
    const it = e.target.closest('.browse-item');
    if (it) go(cur.dirs[+it.dataset.i].path);
  });
  $('.browse-places', body).addEventListener('click', e => {
    const b = e.target.closest('button');
    if (b) go(S.places[+b.dataset.i].path);
  });
  $('[data-a=up]', body).onclick = () => cur?.parent && go(cur.parent);
  input.addEventListener('keydown', e => { if (e.key === 'Enter') go(input.value); });
  $('[data-a=no]', m.foot).onclick = () => m.close();
  $('[data-a=ok]', m.foot).onclick = () => {
    const p = picked || cur?.path;
    if (!p) return;
    $('#scanPath').value = p;
    $$('.place').forEach(x => x.classList.remove('on'));
    m.close();
  };
  go(start || (S.places.find(p => p.kind === 'drive') || S.places[0] || {}).path);
}

// ============================================================ 文件夹树

async function fetchKids(id) {
  const kids = await GET(`/api/children?id=${id}&sort=${S.treeSort}`);
  S.treeKids.set(id, kids);
  return kids;
}

async function refreshTree() {
  if (!S.root) return;
  const ids = [...S.expanded];
  const [rootInfo, ...results] = await Promise.allSettled([GET('/api/dir?id=' + S.root.id), ...ids.map(fetchKids)]);
  if (rootInfo.status === 'fulfilled') S.rootNode = { ...rootInfo.value.info, name: S.root.path };
  results.forEach((r, i) => {
    if (r.status === 'rejected') { S.expanded.delete(ids[i]); S.treeKids.delete(ids[i]); }
  });
  renderTree();
}

function treeNodeHTML(n, depth, parentSize, st) {
  const open = st.expanded.has(n.id);
  const kids = open ? st.kids.get(n.id) : null;
  const pad = 6 + depth * 16;
  const pct = parentSize > 0 ? Math.max(0.6, n.size / parentSize * 100) : 0;
  const tip = `${n.name}\n${fmtSize(n.size)} · ${fmtNum(n.nFiles)} 个文件 · ${fmtNum(n.nDirs)} 个子文件夹${n.err ? '\n' + n.err : ''}`;
  let h = `<div class="trow${n.id === st.cur ? ' cur' : ''}" data-id="${n.id}" style="padding-left:${pad}px;--indent:${pad + 42}px" title="${esc(tip)}"${depth > 0 && st.draggable ? ' draggable="true"' : ''}>
    <span class="caret${n.hasChildren ? (open ? ' open' : '') : ' none'}" data-caret>${ICON_CARET}</span>
    ${n.err ? ICON_FOLDER_ERR : ICON_FOLDER}
    <span class="tname">${esc(n.name)}</span>
    <span class="tsize">${fmtSize(n.size)}</span>
    ${depth > 0 && st.bars ? `<span class="tbar"><i style="width:${pct.toFixed(1)}%"></i></span>` : ''}
  </div>`;
  if (open && kids) for (const k of kids) h += treeNodeHTML(k, depth + 1, n.size, st);
  else if (open && n.hasChildren) h += `<div class="tree-loading" style="padding-left:${pad + 40}px">加载中…</div>`;
  return h;
}

function renderTree() {
  if (!S.rootNode) return;
  const el = $('#tree');
  const top = el.scrollTop;
  el.innerHTML = treeNodeHTML(S.rootNode, 0, 0, { expanded: S.expanded, kids: S.treeKids, cur: S.cur, draggable: true, bars: true });
  el.scrollTop = top;
}

async function toggleExpand(id) {
  if (S.expanded.has(id)) { S.expanded.delete(id); renderTree(); return; }
  S.expanded.add(id);
  renderTree();
  try { await fetchKids(id); } catch (e) { toastErr(e); S.expanded.delete(id); }
  renderTree();
}

async function revealInTree(crumbs) {
  const need = crumbs.slice(0, -1).map(c => c.id).filter(id => !S.expanded.has(id) || !S.treeKids.has(id));
  for (const c of crumbs.slice(0, -1)) S.expanded.add(c.id);
  if (need.length) await Promise.all(need.map(id => fetchKids(id).catch(() => {})));
  renderTree();
  const row = $(`#tree .trow[data-id="${S.cur}"]`);
  if (row) {
    const box = $('#tree').getBoundingClientRect(), r = row.getBoundingClientRect();
    if (r.top < box.top || r.bottom > box.bottom) row.scrollIntoView({ block: 'center' });
  }
}

$('#tree').addEventListener('click', e => {
  const row = e.target.closest('.trow');
  if (!row) return;
  const id = +row.dataset.id;
  if (e.target.closest('[data-caret]')) { toggleExpand(id); return; }
  openDir(id);
});
$('#tree').addEventListener('dblclick', e => {
  const row = e.target.closest('.trow');
  if (row && !e.target.closest('[data-caret]')) toggleExpand(+row.dataset.id);
});
$('#tree').addEventListener('contextmenu', e => {
  const row = e.target.closest('.trow');
  if (!row) return;
  e.preventDefault();
  const id = +row.dataset.id, isRoot = id === S.root.id;
  const name = $('.tname', row).textContent;
  showMenu(e.clientX, e.clientY, [
    { label: '打开', action: () => openDir(id) },
    { label: '新建子文件夹…', action: () => newFolder(id) },
    '-',
    { label: '重命名…', disabled: isRoot, action: () => renameDirPrompt(id, name) },
    { label: '移动到…', disabled: isRoot, action: () => openMoveDialog({ body: { items: [{ d: id }] }, count: 1, what: `文件夹“${name}”` }) },
    '-',
    { label: '添加到最近文件夹', disabled: isRoot, title: '显示在右下方，方便把文件拖进去', action: () => addQuick(id) },
    '-',
    { label: '在资源管理器中显示', action: () => POST('/api/reveal', { item: { d: id } }).catch(toastErr) },
    { label: '复制路径', action: async () => copyText((await POST('/api/path', { item: { d: id } })).path) },
    '-',
    { label: `删除（移到${trashName()}）`, disabled: isRoot, danger: true, action: () => deleteDir(id, name) },
  ]);
});
$('#treeSort').addEventListener('click', async e => {
  const b = e.target.closest('button');
  if (!b) return;
  S.treeSort = b.dataset.v;
  localStorage.setItem('fo-tree-sort', S.treeSort);
  $$('#treeSort button').forEach(x => x.classList.toggle('on', x === b));
  await refreshTree();
});
$$('#treeSort button').forEach(x => x.classList.toggle('on', x.dataset.v === S.treeSort));

async function renameDirPrompt(id, name) {
  const nv = await promptDlg('重命名文件夹', '新名称', name);
  if (!nv || nv === name) return;
  try {
    await POST('/api/rename', { item: { d: id }, newName: nv });
    toast(`已重命名为“${nv}”`, { action: '撤销', onAction: undo });
    await afterChange();
  } catch (e) { toastErr(e); }
}

// ============================================================ 打开文件夹 / 面包屑

async function openDir(id, { keepView = false } = {}) {
  S.cur = id;
  if (!keepView) {
    S.recursive = false;
    S.filter = { ext: '', cat: '', q: '' };
    $('#search').value = '';
    syncScopeSeg();
  }
  clearSel();
  renderTree();
  renderQuick();
  $('#listwrap').scrollTop = 0;
  await Promise.all([loadDirInfo(), loadList(), loadStats()]);
  if (S.curInfo) await revealInTree(S.curInfo.crumbs);
}

async function loadDirInfo() {
  const id = S.cur;
  const info = await GET('/api/dir?id=' + id);
  if (id !== S.cur) return;
  S.curInfo = info;
  const crumbs = info.crumbs;
  $('#crumbs').innerHTML = crumbs.map((c, i) => {
    const name = i === 0 ? S.root.path : c.name;
    return `${i ? '<span class="crumb-sep">›</span>' : ''}<span class="crumb${i === crumbs.length - 1 ? ' last' : ''}" data-id="${c.id}" title="${esc(name)}">${esc(name)}</span>`;
  }).join('');
}

$('#crumbs').addEventListener('click', e => {
  const c = e.target.closest('.crumb');
  if (c) openDir(+c.dataset.id);
});

function goUp() {
  if (!S.curInfo || S.curInfo.crumbs.length < 2) return;
  openDir(S.curInfo.crumbs[S.curInfo.crumbs.length - 2].id);
}

// ============================================================ 文件列表

function currentQuery() {
  return { dir: S.cur, recursive: S.recursive, ext: S.filter.ext, cat: S.filter.cat, q: S.filter.q, sort: S.sort.key, desc: S.sort.desc };
}
const rowKey = r => r.k === 'd' ? 'd:' + r.d : 'f:' + r.d + '/' + r.n;
const refOf = r => r.k === 'd' ? { d: r.d } : { d: r.d, n: r.n };

async function loadList(more = false, keepCount = false) {
  const seq = ++S.listSeq;
  const offset = more ? S.rows.length : 0;
  const limit = keepCount ? Math.max(300, S.rows.length) : 300;
  S.loading = true;
  let res;
  try {
    res = await POST('/api/list', { ...currentQuery(), offset, limit });
  } catch (e) {
    if (seq === S.listSeq) S.loading = false;
    toastErr(e);
    return;
  }
  if (seq !== S.listSeq) return;
  S.loading = false;
  S.rows = more ? S.rows.concat(res.rows) : res.rows;
  S.total = res.total;
  S.totalSize = res.totalSize;
  S.fileCount = res.fileCount;
  S.dirCount = res.dirCount;
  if (!more) {
    // 刷新后去掉已经不存在的选择
    const keys = new Set(S.rows.map(rowKey));
    for (const k of [...S.sel.keys()]) if (!keys.has(k)) S.sel.delete(k);
  }
  renderList(more ? offset : 0);
}

// ------------------------------------------------ 缩略图

const hasThumb = r => r.k === 'f' && S.thumbExts.has(r.e);
const thumbURL = (r, size) => `/api/thumb?d=${r.d}&n=${encodeURIComponent(r.n)}&s=${size}&v=${r.t}_${r.s}&t=${TOKEN}`;
// 先显示格式标签，缩略图加载成功后再替换，失败则保留标签
const THUMB_EVENTS = `onload="this.parentNode.classList.add('ok')" onerror="this.remove()"`;
function fileIcon(r) {
  if (!hasThumb(r)) return extBadge(r.e);
  return `<span class="ticon">${extBadge(r.e)}<img loading="lazy" decoding="async" alt="" src="${thumbURL(r, 64)}" ${THUMB_EVENTS}></span>`;
}

function tileHTML(r, i) {
  const sel = S.selAll || S.sel.has(rowKey(r));
  const isDir = r.k === 'd';
  let visual;
  if (isDir) visual = `<svg viewBox="0 0 24 24" class="gfolder${r.err ? ' err' : ''}"><path d="M2 6a2 2 0 0 1 2-2h5l2 2h9a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2z"/></svg>`;
  else visual = `<span class="gbadge">${extBadge(r.e)}</span>` + (hasThumb(r) ? `<img loading="lazy" decoding="async" alt="" src="${thumbURL(r, 200)}" ${THUMB_EVENTS}>` : '');
  const meta = isDir ? `${r.nf ? fmtNum(r.nf) + ' 个文件' : '空文件夹'} · ${fmtSize(r.s)}` : fmtSize(r.s);
  const tip = `${r.n}\n${meta}${r.t ? '\n修改时间：' + fmtTime(r.t) : ''}${S.recursive ? '\n位置：' + (r.loc || '（当前文件夹）') : ''}`;
  return `<div class="row tile${sel ? ' sel' : ''}" data-i="${i}" draggable="true" title="${esc(tip)}">
    <input type="checkbox" tabindex="-1"${sel ? ' checked' : ''}>
    <div class="gthumb">${visual}</div>
    <div class="gname"><span class="nm">${esc(r.n)}</span></div>
    <div class="gmeta">${esc(meta)}</div>
  </div>`;
}

const itemHTML = (r, i) => S.view === 'grid' ? tileHTML(r, i) : rowHTML(r, i);
const itemsBox = () => S.view === 'grid' ? $('#grid') : $('#rows');
const itemEl = i => $(`.row[data-i="${i}"]`, itemsBox());

function rowHTML(r, i) {
  const sel = S.selAll || S.sel.has(rowKey(r));
  const isDir = r.k === 'd';
  const icon = isDir ? (r.err ? ICON_FOLDER_ERR : ICON_FOLDER) : fileIcon(r);
  const cat = isDir ? null : catOfExt(r.e);
  const type = isDir ? `文件夹${r.nf ? ' · ' + fmtNum(r.nf) + ' 个文件' : ''}` : cat.name + (r.e ? ' · ' + r.e.toUpperCase() : '');
  const loc = r.loc || '（当前文件夹）';
  return `<tr class="row${sel ? ' sel' : ''}" data-i="${i}" draggable="true">
    <td class="c-check"><input type="checkbox" tabindex="-1"${sel ? ' checked' : ''}></td>
    <td class="c-name"><div class="namecell">${icon}<span class="nm" title="${esc(r.n)}">${esc(r.n)}</span>${r.err ? `<span class="errtag" title="${esc(r.err)}">无法访问</span>` : ''}</div></td>
    <td class="c-loc" title="点击打开：${esc(loc)}">${esc(loc)}</td>
    <td class="c-type" title="${esc(type)}">${esc(type)}</td>
    <td class="c-size">${fmtSize(r.s)}</td>
    <td class="c-time">${fmtTime(r.t)}</td>
  </tr>`;
}

function renderList(from = 0) {
  const grid = S.view === 'grid';
  $('#list').classList.toggle('hidden', grid);
  $('#grid').classList.toggle('hidden', !grid);
  $('#gridbar').classList.toggle('hidden', !grid);
  $('#list').classList.toggle('recursive', S.recursive);
  const box = itemsBox();
  if (from === 0) {
    (grid ? $('#rows') : $('#grid')).innerHTML = '';
    box.innerHTML = S.rows.map(itemHTML).join('');
  } else {
    box.insertAdjacentHTML('beforeend', S.rows.slice(from).map((r, j) => itemHTML(r, from + j)).join(''));
  }
  $('#sortSel').value = `${S.sort.key}:${S.sort.desc ? 1 : 0}`;
  $$('#list th.sortable').forEach(th => {
    const on = th.dataset.k === S.sort.key;
    th.innerHTML = th.textContent.replace(/[▲▼]/g, '').trim() + (on ? `<span class="arrow">${S.sort.desc ? '▼' : '▲'}</span>` : '');
  });
  const empty = $('#listEmpty');
  if (!S.total) {
    empty.textContent = S.filter.ext || S.filter.cat || S.filter.q ? '没有找到符合条件的文件' : '这个文件夹是空的';
    empty.classList.remove('hidden');
  } else empty.classList.add('hidden');
  const more = $('#listMore');
  if (S.rows.length < S.total) {
    more.textContent = `已显示 ${fmtNum(S.rows.length)} / ${fmtNum(S.total)} 项，向下滚动加载更多…`;
    more.classList.remove('hidden');
  } else more.classList.add('hidden');
  renderFilterBar();
  syncSel();
}

function renderFilterBar() {
  const bar = $('#filterbar');
  const chips = [];
  if (S.filter.cat) {
    const c = S.catByKey[S.filter.cat] || { name: S.filter.cat, color: '#999' };
    chips.push(`<span class="chip"><span class="dot" style="background:${c.color}"></span>类型：${esc(c.name)}<button data-clear="cat" title="取消筛选">×</button></span>`);
  }
  if (S.filter.ext) {
    const label = S.filter.ext === '-' ? '无扩展名' : '.' + S.filter.ext;
    chips.push(`<span class="chip"><span class="dot" style="background:${catOfExt(S.filter.ext === '-' ? '' : S.filter.ext).color}"></span>格式：${esc(label)}<button data-clear="ext" title="取消筛选">×</button></span>`);
  }
  if (S.filter.q) chips.push(`<span class="chip">搜索：${esc(S.filter.q)}<button data-clear="q" title="清除搜索">×</button></span>`);
  bar.classList.toggle('hidden', !chips.length);
  if (!chips.length) return;
  const scope = S.recursive ? '当前文件夹及所有子文件夹中' : '当前文件夹中';
  bar.innerHTML = chips.join('') + `<span class="muted">${scope}找到 ${fmtNum(S.total)} 项，共 ${fmtSize(S.totalSize)}</span>`;
}

$('#filterbar').addEventListener('click', e => {
  const b = e.target.closest('[data-clear]');
  if (!b) return;
  const k = b.dataset.clear;
  S.filter[k] = '';
  if (k === 'q') $('#search').value = '';
  clearSel();
  loadList();
  renderStats();
});

function setFilter(f) {
  S.filter.ext = f.ext || '';
  S.filter.cat = f.cat || '';
  S.recursive = true;
  syncScopeSeg();
  clearSel();
  $('#listwrap').scrollTop = 0;
  loadList();
  renderStats();
}

function syncScopeSeg() {
  $$('#scopeSeg button').forEach(b => b.classList.toggle('on', (b.dataset.v === '1') === S.recursive));
}
$('#scopeSeg').addEventListener('click', e => {
  const b = e.target.closest('button');
  if (!b) return;
  S.recursive = b.dataset.v === '1';
  syncScopeSeg();
  clearSel();
  $('#listwrap').scrollTop = 0;
  loadList();
});

$('#search').addEventListener('input', debounce(() => {
  const q = $('#search').value.trim();
  if (q === S.filter.q) return;
  S.filter.q = q;
  if (q && !S.recursive) { S.recursive = true; syncScopeSeg(); }
  clearSel();
  $('#listwrap').scrollTop = 0;
  loadList();
}, 300));

$('#list thead').addEventListener('click', e => {
  const th = e.target.closest('th.sortable');
  if (!th) return;
  const k = th.dataset.k;
  if (S.sort.key === k) S.sort.desc = !S.sort.desc;
  else S.sort = { key: k, desc: k === 'size' || k === 'mtime' };
  clearSel();
  loadList();
});

$('#listwrap').addEventListener('scroll', () => {
  const w = $('#listwrap');
  if (!S.loading && S.rows.length < S.total && w.scrollTop + w.clientHeight > w.scrollHeight - 400) loadList(true);
});

// ------------------------------------------------ 选择

function clearSel() {
  S.sel.clear();
  S.selAll = false;
  S.anchor = -1;
  syncSel();
}
function selectOnly(i) {
  S.sel.clear();
  S.selAll = false;
  const r = S.rows[i];
  if (r) S.sel.set(rowKey(r), r);
  S.anchor = i;
  syncSel();
}
function toggleSel(i) {
  if (S.selAll) { S.selAll = false; S.rows.forEach(r => S.sel.set(rowKey(r), r)); }
  const r = S.rows[i], k = rowKey(r);
  if (S.sel.has(k)) S.sel.delete(k); else S.sel.set(k, r);
  S.anchor = i;
  syncSel();
}
function selectRange(i) {
  S.selAll = false;
  const [a, b] = [Math.min(S.anchor, i), Math.max(S.anchor, i)];
  S.sel.clear();
  for (let j = a; j <= b; j++) S.sel.set(rowKey(S.rows[j]), S.rows[j]);
  syncSel();
}
function selectAllLoaded() {
  S.selAll = false;
  S.rows.forEach(r => S.sel.set(rowKey(r), r));
  syncSel();
}
const selCount = () => S.selAll ? S.total : S.sel.size;
const selectedRows = () => S.rows.filter(r => S.selAll || S.sel.has(rowKey(r)));
function selBody() {
  if (S.selAll) return { query: currentQuery() };
  return { items: selectedRows().map(refOf) };
}
function selSize() {
  if (S.selAll) return S.totalSize + S.rows.filter(r => r.k === 'd').reduce((a, r) => a + r.s, 0);
  let n = 0;
  for (const r of S.sel.values()) n += r.s;
  return n;
}

function syncSel() {
  for (const el of itemsBox().children) {
    const r = S.rows[+el.dataset.i];
    if (!r) continue;
    const on = S.selAll || S.sel.has(rowKey(r));
    el.classList.toggle('sel', on);
    const cb = el.querySelector('input[type=checkbox]');
    if (cb) cb.checked = on;
  }
  const n = selCount();
  for (const all of [$('#checkAll'), $('#gridCheckAll')]) {
    all.checked = n > 0 && n >= S.rows.length;
    all.indeterminate = n > 0 && n < S.rows.length;
  }
  $('#btnMove').disabled = !n;
  $('#btnDelete').disabled = !n;
  $('#btnBatchRename').disabled = !n;
  $('#btnRename').disabled = n !== 1 || S.selAll;
  $('#btnReveal').disabled = n !== 1 || S.selAll;

  const banner = $('#selbanner');
  if (S.selAll) {
    banner.innerHTML = `已选择全部 <b>${fmtNum(S.total)}</b> 项（${fmtSize(selSize())}）。<button class="linkbtn" data-a="clear">取消选择</button>`;
    banner.classList.remove('hidden');
  } else if (n > 0 && n === S.rows.length && S.total > S.rows.length) {
    banner.innerHTML = `已选择当前显示的 ${fmtNum(n)} 项。<button class="linkbtn" data-a="all">选择全部 ${fmtNum(S.total)} 项</button>`;
    banner.classList.remove('hidden');
  } else banner.classList.add('hidden');

  const info = $('#selInfo');
  if (n) info.textContent = `已选择 ${fmtNum(n)} 项 · ${fmtSize(selSize())}`;
  else if (S.curInfo) {
    const parts = [];
    if (S.dirCount) parts.push(`${fmtNum(S.dirCount)} 个文件夹`);
    parts.push(`${fmtNum(S.fileCount)} 个文件（${fmtSize(S.totalSize)}）`);
    info.textContent = (S.recursive ? '含子文件夹：' : '当前文件夹：') + parts.join('，');
  } else info.textContent = '';
}

$('#selbanner').addEventListener('click', e => {
  const b = e.target.closest('[data-a]');
  if (!b) return;
  if (b.dataset.a === 'all') { S.selAll = true; syncSel(); } else clearSel();
});
for (const id of ['#checkAll', '#gridCheckAll']) $(id).addEventListener('change', e => { if (e.target.checked) selectAllLoaded(); else clearSel(); });

const rowsEl = $('#listwrap');
rowsEl.addEventListener('click', e => {
  const tr = e.target.closest('.row');
  if (!tr) {
    // 点击空白处取消选择
    if (!e.target.closest('thead, .gridbar, .rename-input') && (e.target === rowsEl || e.target.closest('#grid, #listEmpty, #listMore'))) clearSel();
    return;
  }
  if (e.target.closest('.rename-input')) return;
  const i = +tr.dataset.i, r = S.rows[i];
  if (e.target.matches('input[type=checkbox]')) { toggleSel(i); return; }
  if (e.target.closest('td.c-loc') && S.recursive) { openDir(r.k === 'd' ? r.p : r.d); return; }
  if (e.shiftKey && S.anchor >= 0) selectRange(i);
  else if (e.ctrlKey || e.metaKey) toggleSel(i);
  else selectOnly(i);
});
rowsEl.addEventListener('dblclick', e => {
  const tr = e.target.closest('.row');
  if (!tr || e.target.closest('.rename-input') || e.target.matches('input[type=checkbox]')) return;
  openRow(S.rows[+tr.dataset.i]);
});
rowsEl.addEventListener('contextmenu', e => {
  const tr = e.target.closest('.row');
  if (!tr) return;
  e.preventDefault();
  const i = +tr.dataset.i, r = S.rows[i];
  if (!S.selAll && !S.sel.has(rowKey(r))) selectOnly(i);
  const n = selCount(), single = n === 1 && !S.selAll;
  const body = selBody();
  // 最近新建的文件夹：排除正在被移动的文件夹本身
  const moving = new Set(S.selAll ? [] : selectedRows().filter(x => x.k === 'd').map(x => x.d));
  const quick = S.quick.filter(q => !moving.has(q.id)).slice(0, 6);
  showMenu(e.clientX, e.clientY, [
    { label: r.k === 'd' ? '打开文件夹' : '打开文件', kbd: 'Enter', disabled: !single, action: () => openRow(r) },
    S.recursive ? { label: '转到所在文件夹', disabled: !single, action: () => openDir(r.k === 'd' ? r.p : r.d) } : null,
    { label: '在资源管理器中显示', disabled: !single, action: () => revealRow(r) },
    { label: '复制完整路径', disabled: !single, action: async () => copyText((await POST('/api/path', { item: refOf(r) })).path) },
    '-',
    { label: '重命名', kbd: 'F2', disabled: !single, action: () => startRename(i) },
    { label: `批量重命名（${fmtNum(n)} 项）…`, action: openBatchRename },
    { label: `移动到…（${fmtNum(n)} 项）`, action: moveSelected },
    quick.length ? '-' : null,
    quick.length ? { header: '归类到最近新建的文件夹' } : null,
    ...quick.map(q => ({ label: q.name, icon: ICON_FOLDER, sub: q.parent, title: q.path, action: () => doMove(body, q.id) })),
    '-',
    { label: `删除（移到${trashName()}）`, kbd: 'Del', danger: true, action: deleteSelected },
  ]);
});

function openRow(r) {
  if (!r) return;
  if (r.k === 'd') openDir(r.d);
  else POST('/api/open', { item: refOf(r) }).catch(toastErr);
}
function revealRow(r) { POST('/api/reveal', { item: refOf(r) }).catch(toastErr); }

// ------------------------------------------------ 行内重命名

function startRename(i) {
  const r = S.rows[i];
  const tr = itemEl(i);
  if (!r || !tr) return;
  selectOnly(i);
  tr.draggable = false;
  const nm = $('.nm', tr);
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'rename-input';
  input.value = r.n;
  input.spellcheck = false;
  nm.replaceWith(input);
  input.focus();
  const dot = r.k === 'f' ? r.n.lastIndexOf('.') : -1;
  input.setSelectionRange(0, dot > 0 ? dot : r.n.length);
  let done = false;
  const restore = () => { const t = document.createElement('template'); t.innerHTML = itemHTML(r, i).trim(); tr.replaceWith(t.content.firstChild); syncSel(); };
  const finish = async commit => {
    if (done) return;
    done = true;
    const nv = input.value.trim();
    if (!commit || !nv || nv === r.n) { restore(); return; }
    if (r.k === 'f' && extOf(nv) !== extOf(r.n)) {
      const ok = await confirmDlg('修改扩展名', `如果改变文件扩展名，可能会导致文件无法正常打开。\n\n“${r.n}” → “${nv}”\n\n确实要更改吗？`, '更改');
      if (!ok) { restore(); return; }
    }
    try {
      await POST('/api/rename', { item: refOf(r), newName: nv });
      toast(`已重命名为“${nv}”`, { action: '撤销', onAction: undo });
      await afterChange({ reselect: r.k === 'd' ? { k: 'd', d: r.d } : { k: 'f', d: r.d, n: nv } });
    } catch (e) { toastErr(e); restore(); }
  };
  input.addEventListener('keydown', e => {
    e.stopPropagation();
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('blur', () => finish(true));
  input.addEventListener('dblclick', e => e.stopPropagation());
}

function renameSelected() {
  if (selCount() !== 1 || S.selAll) return;
  const r = selectedRows()[0];
  const i = S.rows.indexOf(r);
  if (i >= 0) startRename(i);
}

// ------------------------------------------------ 刷新

async function afterChange(opts = {}) {
  await refreshState().catch(() => {});
  try { await loadDirInfo(); } catch { await openDir(S.root.id); return; }
  await Promise.all([loadList(false, true), loadStats(), refreshTree(), loadQuick()]);
  if (opts.reselect) {
    const i = S.rows.findIndex(r => r.k === opts.reselect.k && r.d === opts.reselect.d && (r.k === 'd' || r.n === opts.reselect.n));
    if (i >= 0) selectOnly(i); else clearSel();
  } else clearSel();
}

// ============================================================ 类型统计

async function loadStats() {
  const id = S.cur;
  let st;
  try { st = await GET('/api/stats?id=' + id); } catch (e) { return; }
  if (id !== S.cur) return;
  S.stats = st;
  renderStats();
}

function renderStats() {
  const st = S.stats, box = $('#stats');
  if (!st) return;
  const scopeName = S.curInfo ? (S.curInfo.crumbs.length === 1 ? S.root.path : S.curInfo.info.name) : '';
  $('#statsScope').textContent = scopeName ? '· ' + scopeName : '';
  $('#statsScope').title = S.curInfo ? S.curInfo.path : '';
  if (!st.count) {
    box.innerHTML = '<div class="empty">这里没有文件</div>';
    return;
  }
  const total = st.size || 1;
  let h = `<div class="stats-total"><b>${fmtSize(st.size)}</b><span class="muted">${fmtNum(st.count)} 个文件（含子文件夹）</span></div>`;
  h += '<div class="stack">' + st.cats.map(c => `<i style="width:${(c.size / total * 100).toFixed(2)}%;background:${c.color}" title="${esc(c.name)}：${fmtSize(c.size)}"></i>`).join('') + '</div>';
  for (const c of st.cats) {
    const open = S.statsOpen.has(c.key);
    const active = S.filter.cat === c.key && !S.filter.ext;
    const pct = c.size / total * 100;
    h += `<div class="cat">
      <div class="cat-row${active ? ' active' : ''}" data-cat="${c.key}" title="点击展开/收起具体格式">
        <span class="dot" style="background:${c.color}"></span>
        <span class="cat-name">${esc(c.name)}<small>${fmtNum(c.count)} 个 · ${pct < 0.1 ? '<0.1' : pct.toFixed(1)}%</small></span>
        <span class="cat-size">${fmtSize(c.size)}</span>
        <div class="cat-bar"><i style="width:${Math.max(pct, 0.5).toFixed(2)}%;background:${c.color}"></i></div>
        <div class="cat-actions">
          <button class="btn small" data-act="list">列出这些文件</button>
          <button class="btn small" data-act="collect">归类到文件夹…</button>
        </div>
      </div>`;
    if (open) {
      h += '<div class="cat-exts">' + c.exts.map(e => {
        const ea = S.filter.ext === (e.ext || '-');
        return `<div class="ext-row${ea ? ' active' : ''}" data-cat="${c.key}" data-ext="${esc(e.ext || '-')}" title="点击列出所有 ${esc(e.ext ? '.' + e.ext : '无扩展名')} 文件">
          ${extBadge(e.ext)}
          <span class="cat-name">${esc(e.ext ? '.' + e.ext : '无扩展名')}<small>${fmtNum(e.count)} 个</small></span>
          <span class="cat-size">${fmtSize(e.size)}</span>
          <div class="cat-actions"><button class="btn small" data-act="collect">归类到文件夹…</button></div>
        </div>`;
      }).join('') + '</div>';
    }
    h += '</div>';
  }
  h += `<div class="stats-hint">提示：点击类型可展开具体格式。“列出这些文件”会把当前文件夹及所有子文件夹里的这类文件集中列出来；“归类到文件夹”可一步把它们全部移动到一个（新建的）文件夹里。</div>`;
  box.innerHTML = h;
}

$('#stats').addEventListener('click', e => {
  const row = e.target.closest('.cat-row, .ext-row');
  if (!row) return;
  const cat = row.dataset.cat, ext = row.dataset.ext;
  const act = e.target.closest('[data-act]')?.dataset.act;
  const c = S.stats.cats.find(x => x.key === cat);
  if (act === 'collect') {
    const es = ext ? c.exts.find(x => (x.ext || '-') === ext) : null;
    const label = ext ? (ext === '-' ? '无扩展名' : ext.toUpperCase()) : c.name;
    openMoveDialog({
      body: { query: { dir: S.cur, recursive: true, ext: ext || '', cat: ext ? '' : cat, sort: 'name' } },
      count: es ? es.count : c.count,
      size: es ? es.size : c.size,
      what: `当前文件夹及其子文件夹中的全部「${label}」文件`,
      suggestName: ext ? label : c.name.split('/')[0],
    });
    return;
  }
  if (act === 'list' || ext) { setFilter(ext ? { ext } : { cat }); return; }
  if (S.statsOpen.has(cat)) S.statsOpen.delete(cat); else S.statsOpen.add(cat);
  renderStats();
});

// ============================================================ 新建 / 移动 / 批量重命名 / 撤销

async function newFolder(parentId = S.cur) {
  const name = await promptDlg('新建文件夹', '文件夹名称', '新建文件夹', { okText: '新建' });
  if (!name) return;
  try {
    const res = await POST('/api/mkdir', { parent: parentId, name });
    toast(`已新建文件夹“${name}”`, { action: '撤销', onAction: undo });
    S.expanded.add(parentId);
    await afterChange({ reselect: { k: 'd', d: res.newId } });
    return res.newId;
  } catch (e) { toastErr(e); }
}

function reportMove(res, verb = '移动') {
  const parts = [];
  if (res.done) parts.push(`已${verb} ${fmtNum(res.done)} 项` + (res.target ? `到“${res.target}”` : ''));
  if (res.skipped) parts.push(`跳过 ${fmtNum(res.skipped)} 项（已在目标位置或存在同名文件）`);
  if (!res.done && !res.skipped && !(res.errors || []).length) parts.push('没有需要移动的项目');
  if (parts.length) toast(parts.join('，'), res.done ? { action: '撤销', onAction: undo } : {});
  const errs = res.errors || [];
  if (errs.length) toast(`${errs.length} 项操作失败`, { err: true, action: '查看原因', onAction: () => errorListDlg('以下项目操作失败', errs) });
}

async function doMove(body, target, conflict = 'rename') {
  return busy(async () => {
    try {
      const res = await POST('/api/move', { ...body, target, conflict });
      reportMove(res);
      if (res.done) {
        const path = res.target;
        S.recent = [{ id: target, path }, ...S.recent.filter(x => x.id !== target)].slice(0, 6);
      }
      await afterChange();
      return res;
    } catch (e) { toastErr(e); }
  });
}

function moveSelected() {
  const n = selCount();
  if (!n) return;
  openMoveDialog({ body: selBody(), count: n, size: selSize(), what: `选中的 ${fmtNum(n)} 项` });
}

async function openMoveDialog({ body, count, size, what, suggestName }) {
  // 核对“最近移动到”的文件夹是否还在（可能已被移动、改名或删除）
  S.recent = (await Promise.all(S.recent.map(r => GET('/api/locate?path=' + encodeURIComponent(r.path))
    .then(x => x.id ? { id: x.id, path: r.path } : null).catch(() => null)))).filter(Boolean);
  const recent = S.recent.filter(r => !S.quick.some(q => q.id === r.id));
  const el = document.createElement('div');
  el.innerHTML = `
    <div style="margin-bottom:10px">把 <b>${esc(what)}</b>${size ? `（${fmtNum(count)} 项，${fmtSize(size)}）` : ''} 移动到：</div>
    <div class="picker"></div>
    <div class="target-line"><span class="muted">目标文件夹：</span><b data-a="path">（请在上面选择）</b></div>
    <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
      <button class="btn small" data-a="new">＋ 在所选文件夹中新建文件夹…</button>
    </div>
    ${S.quick.length ? `<div class="hint" style="margin-top:12px">最近新建的文件夹：</div><div class="recent">${S.quick.map(q => `<button class="btn small" data-rid="${q.id}" title="${esc(q.path)}">📁 ${esc(q.name)}</button>`).join('')}</div>` : ''}
    ${recent.length ? `<div class="hint" style="margin-top:12px">最近移动到：</div><div class="recent">${recent.map(r => `<button class="btn small" data-rid="${r.id}" title="${esc(r.path)}">${esc(r.path)}</button>`).join('')}</div>` : ''}
    <div class="radios">
      <label><input type="radio" name="conflict" value="rename" checked> 遇到同名文件时自动改名（例如“报告 (1).docx”），不会覆盖任何文件</label>
      <label><input type="radio" name="conflict" value="skip"> 遇到同名文件时跳过，保留在原位置</label>
    </div>`;
  const m = modal({
    title: '移动到文件夹', body: el, wide: true,
    foot: '<button class="btn" data-a="no">取消</button><button class="btn primary" data-a="ok" disabled>移动到这里</button>',
  });
  const pk = { expanded: new Set([S.root.id]), kids: new Map(), cur: 0 };
  const picker = $('.picker', el);
  const fetchPk = async id => { pk.kids.set(id, await GET(`/api/children?id=${id}&sort=name`)); };
  const renderPk = () => {
    const root = { ...S.rootNode, name: S.root.path };
    const top = picker.scrollTop;
    picker.innerHTML = treeNodeHTML(root, 0, 0, { expanded: pk.expanded, kids: pk.kids, cur: pk.cur, draggable: false, bars: false });
    picker.scrollTop = top;
  };
  const select = async id => {
    pk.cur = id;
    let info;
    try { info = await GET('/api/dir?id=' + id); } catch (e) { toastErr(e); return; }
    const anc = info.crumbs.slice(0, -1).map(c => c.id);
    await Promise.all(anc.filter(a => !pk.kids.has(a)).map(fetchPk));
    anc.forEach(a => pk.expanded.add(a));
    renderPk();
    $('[data-a=path]', el).textContent = info.path;
    $('[data-a=ok]', m.foot).disabled = false;
    const row = $(`.trow[data-id="${id}"]`, picker);
    if (row) row.scrollIntoView({ block: 'nearest' });
  };
  picker.addEventListener('click', async e => {
    const row = e.target.closest('.trow');
    if (!row) return;
    const id = +row.dataset.id;
    if (e.target.closest('[data-caret]')) {
      if (pk.expanded.has(id)) pk.expanded.delete(id);
      else { pk.expanded.add(id); if (!pk.kids.has(id)) await fetchPk(id); }
      renderPk();
      return;
    }
    select(id);
  });
  picker.addEventListener('dblclick', async e => {
    const row = e.target.closest('.trow');
    if (!row || e.target.closest('[data-caret]')) return;
    const id = +row.dataset.id;
    pk.expanded.add(id);
    if (!pk.kids.has(id)) await fetchPk(id);
    renderPk();
  });
  $('[data-a=new]', el).onclick = async () => {
    const parent = pk.cur || S.root.id;
    const name = await promptDlg('新建文件夹', '文件夹名称', suggestName || '新建文件夹', { okText: '新建' });
    if (!name) return;
    try {
      const res = await POST('/api/mkdir', { parent, name });
      await fetchPk(parent);
      pk.expanded.add(parent);
      await select(res.newId);
      refreshState().catch(() => {});
    } catch (e) { toastErr(e); }
  };
  el.addEventListener('click', e => {
    const b = e.target.closest('[data-rid]');
    if (b) select(+b.dataset.rid);
  });
  $('[data-a=no]', m.foot).onclick = () => m.close();
  $('[data-a=ok]', m.foot).onclick = async () => {
    if (!pk.cur) return;
    const conflict = $('input[name=conflict]:checked', el).value;
    const target = pk.cur;
    m.close();
    await doMove(body, target, conflict);
  };
  await fetchPk(S.root.id).catch(toastErr);
  renderPk();
  // 默认选中当前文件夹，便于在它下面新建子文件夹
  select(S.cur);
}

function openBatchRename() {
  const n = selCount();
  if (!n) return;
  const body = selBody();
  const el = document.createElement('div');
  el.innerHTML = `
    <div class="form-grid">
      <label>新名称模板</label><input type="text" data-f="template" value="{name}" spellcheck="false">
      <span></span><div class="hint">可以使用 <code data-ins="{name}">{name}</code> 代表原来的名称，<code data-ins="{n}">{n}</code> 代表序号。例如 <code data-set="旅行照片_{n}">旅行照片_{n}</code> 或 <code data-set="2024_{name}">2024_{name}</code>。文件的扩展名会自动保留。</div>
      <label>起始序号</label><div style="display:flex;gap:12px;align-items:center"><input type="number" data-f="start" value="1" min="0" style="width:90px"><label>序号位数</label><input type="number" data-f="digits" value="2" min="1" max="8" style="width:70px"><span class="hint">位数为 3 时：001、002…</span></div>
      <label>查找</label><input type="text" data-f="find" placeholder="（可选）要替换掉的文字" spellcheck="false">
      <label>替换为</label><input type="text" data-f="replace" placeholder="留空表示删除上面的文字" spellcheck="false">
    </div>
    <div class="preview-wrap"><table class="preview"><thead><tr><th style="width:38%">原名称</th><th style="width:38%">新名称</th><th>状态</th></tr></thead><tbody></tbody></table></div>
    <div class="hint" data-a="sum" style="margin-top:8px"></div>`;
  const m = modal({
    title: `批量重命名（${fmtNum(n)} 项）`, body: el, wide: true,
    foot: '<button class="btn" data-a="no">取消</button><button class="btn primary" data-a="ok" disabled>重命名</button>',
  });
  const val = () => ({
    template: $('[data-f=template]', el).value,
    start: parseInt($('[data-f=start]', el).value, 10) || 0,
    digits: parseInt($('[data-f=digits]', el).value, 10) || 1,
    find: $('[data-f=find]', el).value,
    replace: $('[data-f=replace]', el).value,
  });
  let seq = 0;
  const preview = debounce(async () => {
    const my = ++seq;
    let res;
    try { res = await POST('/api/batch-rename', { ...body, ...val(), preview: true }); } catch (e) { toastErr(e); return; }
    if (my !== seq) return;
    const statusText = s => s === 'ok' ? '✓' : s === 'same' ? '名称不变' : s;
    $('tbody', el).innerHTML = res.plans.map(p => `<tr class="${p.status === 'ok' ? '' : p.status === 'same' ? 'same' : 'bad'}">
      <td title="${esc(p.old)}">${esc(p.old)}</td><td class="new" title="${esc(p.new)}">${esc(p.new)}</td><td title="${esc(statusText(p.status))}">${esc(statusText(p.status))}</td></tr>`).join('');
    const bad = res.plans.filter(p => p.status !== 'ok' && p.status !== 'same').length;
    $('[data-a=sum]', el).textContent = `${fmtNum(res.ok)} 项将被重命名` + (bad ? `，${bad} 项有问题将被跳过` : '') + (res.plans.length < n ? `（预览只显示前 ${res.plans.length} 项）` : '');
    const ok = $('[data-a=ok]', m.foot);
    ok.disabled = !res.ok;
    ok.textContent = `重命名 ${fmtNum(res.ok)} 项`;
  }, 200);
  el.addEventListener('input', preview);
  el.addEventListener('click', e => {
    const ins = e.target.closest('[data-ins]'), set = e.target.closest('[data-set]');
    const t = $('[data-f=template]', el);
    if (ins) {
      const p = t.selectionStart ?? t.value.length;
      t.value = t.value.slice(0, p) + ins.dataset.ins + t.value.slice(t.selectionEnd ?? p);
      t.focus(); preview();
    } else if (set) { t.value = set.dataset.set; preview(); }
  });
  $('[data-a=no]', m.foot).onclick = () => m.close();
  $('[data-a=ok]', m.foot).onclick = async () => {
    m.close();
    await busy(async () => {
      try {
        const res = await POST('/api/batch-rename', { ...body, ...val(), preview: false });
        reportMove(res, '重命名');
        await afterChange();
      } catch (e) { toastErr(e); }
    });
  };
  preview();
  setTimeout(() => { const t = $('[data-f=template]', el); t.focus(); t.select(); }, 0);
}

// ------------------------------------------------ 删除（移到回收站）

async function deleteSelected() {
  const n = selCount();
  if (!n) return;
  const body = selBody();
  const rows = selectedRows();
  const names = S.selAll ? [] : rows.slice(0, 8).map(r => (r.k === 'd' ? '📁 ' : '• ') + r.n);
  const hasDir = (S.selAll ? S.rows : rows).some(r => r.k === 'd');
  const msg = `确定要把这 ${fmtNum(n)} 项（${fmtSize(selSize())}）移到${trashName()}吗？\n\n` +
    (names.length ? names.join('\n') + (n > names.length ? `\n……等 ${fmtNum(n)} 项` : '') + '\n\n' : '') +
    (hasDir ? '文件夹会连同里面的所有内容一起移到' + trashName() + '。\n' : '') +
    `删除后可以在${trashName()}中还原。`;
  if (!(await confirmDlg('删除', msg, `移到${trashName()}`, true))) return;
  await doDelete(body);
}

async function deleteDir(id, name) {
  const msg = `确定要把文件夹“${name}”连同里面的所有内容移到${trashName()}吗？\n\n删除后可以在${trashName()}中还原。`;
  if (!(await confirmDlg('删除文件夹', msg, `移到${trashName()}`, true))) return;
  await doDelete({ items: [{ d: id }] });
}

async function doDelete(body) {
  await busy(async () => {
    try {
      const res = await POST('/api/delete', body);
      if (res.done) toast(`已将 ${fmtNum(res.done)} 项移到${trashName()}，需要时可以在${trashName()}中还原`);
      const errs = res.errors || [];
      if (errs.length) toast(`${errs.length} 项没有删除`, { err: true, action: '查看原因', onAction: () => errorListDlg('以下项目没有删除', errs) });
      await afterChange();
    } catch (e) { toastErr(e); }
  });
}

// ------------------------------------------------ 最近新建的文件夹

async function loadQuick() {
  try { S.quick = await GET('/api/quick'); } catch { S.quick = []; }
  renderQuick();
}

function renderQuick() {
  const box = $('#quick');
  if (!S.quick.length) {
    box.innerHTML = '<div class="quick-empty">用本程序新建的文件夹会出现在这里，方便把文件直接拖进去归类。也可以在左侧文件夹上右键，选择“添加到最近文件夹”。</div>';
    return;
  }
  box.innerHTML = S.quick.map(q => `<div class="quick-item${q.id === S.cur ? ' cur' : ''}" data-id="${q.id}" title="${esc(q.path)}\n单击打开；把文件拖到这里即可移动进去">
    ${ICON_FOLDER}
    <div class="qtext"><div class="qname">${esc(q.name)}</div><div class="qpath">${esc(q.parent || '（根目录）')}</div></div>
    <span class="qsize">${fmtSize(q.size)}</span>
    <button class="qx" data-path="${esc(q.path)}" title="从这个列表中移除（不会删除文件夹）">×</button>
  </div>`).join('');
}

$('#quick').addEventListener('click', async e => {
  const x = e.target.closest('.qx');
  if (x) {
    e.stopPropagation();
    await POST('/api/quick/remove', { path: x.dataset.path }).catch(toastErr);
    loadQuick();
    return;
  }
  const it = e.target.closest('.quick-item');
  if (it) openDir(+it.dataset.id);
});

async function addQuick(id) {
  try {
    await POST('/api/quick/add', { id });
    await loadQuick();
    toast('已添加到“最近新建的文件夹”');
  } catch (e) { toastErr(e); }
}

async function undo() {
  if (!S.undoable) { toast('没有可以撤销的操作'); return; }
  await busy(async () => {
    try {
      const r = await POST('/api/undo');
      const res = r.result;
      toast(`已撤销：${r.desc}`);
      if (res.errors && res.errors.length) toast(`${res.errors.length} 项无法撤销`, { err: true, action: '查看原因', onAction: () => errorListDlg('以下项目无法撤销', res.errors) });
      await afterChange();
    } catch (e) { toastErr(e); await refreshState().catch(() => {}); }
  });
}

async function openHistory() {
  let list = [];
  try { list = await GET('/api/history'); } catch (e) { toastErr(e); return; }
  const fmtWhen = t => { const d = new Date(t); return d.toLocaleTimeString('zh-CN', { hour12: false }); };
  const body = list.length
    ? list.map((b, i) => `<div class="history-item"><span class="when">${fmtWhen(b.time)}</span><span class="what" title="${esc(b.desc)}">${esc(b.desc)}</span>${i === 0 ? '<button class="btn small" data-a="undo">撤销这一步</button>' : ''}</div>`).join('')
    : '<div class="empty">本次还没有进行任何操作</div>';
  const m = modal({
    title: '操作记录', wide: true,
    body: body + `<div class="hint" style="margin-top:12px">可以从最近的一步开始依次撤销。所有操作也会永久记录在日志文件中：<br>${esc(S.logPath)}</div>`,
    foot: '<button class="btn left" data-a="log">打开日志文件位置</button><button class="btn primary" data-a="close">关闭</button>',
  });
  $('[data-a=close]', m.foot).onclick = () => m.close();
  $('[data-a=log]', m.foot).onclick = () => POST('/api/open-log').catch(toastErr);
  const u = $('[data-a=undo]', m.body);
  if (u) u.onclick = async () => { m.close(); await undo(); openHistory(); };
}

// ============================================================ 拖放移动

function setDragBadge(text) {
  const b = $('#dragBadge');
  b.textContent = text;
  return b;
}

rowsEl.addEventListener('dragstart', e => {
  const tr = e.target.closest('.row');
  if (!tr) return;
  const i = +tr.dataset.i, r = S.rows[i];
  if (!S.selAll && !S.sel.has(rowKey(r))) selectOnly(i);
  S.dragBody = selBody();
  S.dragCount = selCount();
  S.dragKeys = new Set(S.selAll ? [] : [...S.sel.keys()]);
  e.dataTransfer.effectAllowed = 'move';
  e.dataTransfer.setData('text/plain', 'fo-move');
  e.dataTransfer.setDragImage(setDragBadge(`移动 ${fmtNum(S.dragCount)} 项`), -12, -8);
});
$('#tree').addEventListener('dragstart', e => {
  const row = e.target.closest('.trow');
  if (!row) return;
  S.dragBody = { items: [{ d: +row.dataset.id }] };
  S.dragCount = 1;
  S.dragKeys = new Set(['d:' + row.dataset.id]);
  e.dataTransfer.effectAllowed = 'move';
  e.dataTransfer.setData('text/plain', 'fo-move');
  e.dataTransfer.setDragImage(setDragBadge(`移动文件夹“${$('.tname', row).textContent}”`), -12, -8);
});
document.addEventListener('dragend', () => {
  S.dragBody = null;
  $$('.drop').forEach(x => x.classList.remove('drop'));
});

function dropTarget(e) {
  const t = e.target.closest('#tree .trow, .crumb, #listwrap .row, .quick-item');
  if (!t) return null;
  if (t.matches('.quick-item')) {
    const id = +t.dataset.id;
    if (S.dragKeys.has('d:' + id)) return null;
    return { el: t, id, name: $('.qname', t).textContent };
  }
  if (t.matches('.row')) {
    const r = S.rows[+t.dataset.i];
    if (!r || r.k !== 'd' || S.dragKeys.has(rowKey(r))) return null;
    return { el: t, id: r.d, name: r.n };
  }
  const id = +t.dataset.id;
  if (S.dragKeys.has('d:' + id)) return null;
  return { el: t, id, name: t.matches('.crumb') ? t.textContent : $('.tname', t).textContent };
}
// 从资源管理器拖入的外部文件：阻止浏览器直接打开该文件而离开本页面
function isExternalDrag(e) { return !S.dragBody && [...(e.dataTransfer?.types || [])].includes('Files'); }

document.addEventListener('dragover', e => {
  if (isExternalDrag(e)) { e.preventDefault(); e.dataTransfer.dropEffect = 'none'; return; }
  if (!S.dragBody) return;
  const t = dropTarget(e);
  $$('.drop').forEach(x => x !== t?.el && x.classList.remove('drop'));
  if (!t) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
  t.el.classList.add('drop');
});
document.addEventListener('drop', async e => {
  if (isExternalDrag(e)) { e.preventDefault(); toast('请在本程序的列表中拖动文件；不支持从外部拖入'); return; }
  if (!S.dragBody) return;
  const t = dropTarget(e);
  if (!t) return;
  e.preventDefault();
  const body = S.dragBody;
  S.dragBody = null;
  $$('.drop').forEach(x => x.classList.remove('drop'));
  await doMove(body, t.id, 'rename');
});

// ============================================================ 工具栏 / 快捷键 / 其他

$('#btnNewFolder').onclick = () => newFolder();
$('#btnMove').onclick = moveSelected;
$('#btnRename').onclick = renameSelected;
$('#btnBatchRename').onclick = openBatchRename;
$('#btnReveal').onclick = () => { const r = selectedRows()[0]; if (r) revealRow(r); };
$('#btnDelete').onclick = () => deleteSelected();
function syncViewSeg() { $$('#viewSeg button').forEach(b => b.classList.toggle('on', b.dataset.v === S.view)); }
$('#viewSeg').addEventListener('click', e => {
  const b = e.target.closest('button');
  if (!b || b.dataset.v === S.view) return;
  S.view = b.dataset.v;
  localStorage.setItem('fo-view', S.view);
  syncViewSeg();
  renderList();
});
syncViewSeg();
$('#sortSel').addEventListener('change', e => {
  const [key, desc] = e.target.value.split(':');
  S.sort = { key, desc: desc === '1' };
  clearSel();
  loadList();
});
$('#btnUndo').onclick = undo;
$('#btnStats').onclick = () => document.body.classList.toggle('stats-open');
$('#btnHistory').onclick = openHistory;
$('#btnQuit').onclick = async () => {
  if (!(await confirmDlg('退出程序', '确定要退出“文件整理助手”吗？\n所有已完成的整理操作都已经保存在磁盘上。', '退出'))) return;
  await POST('/api/quit').catch(() => {});
  document.body.innerHTML = '<div class="welcome"><div class="welcome-card" style="text-align:center"><h1>程序已退出</h1><p class="muted">现在可以关闭这个浏览器页面了。需要时重新运行“文件整理助手”即可。</p></div></div>';
};

document.addEventListener('keydown', e => {
  if (e.key === 'Escape') {
    hideMenu();
    if (modals.length) { modals[modals.length - 1].close(); return; }
  }
  if (modals.length || $('#app').classList.contains('hidden') || !$('#welcome').classList.contains('hidden')) return;
  const inInput = e.target instanceof Element && e.target.matches('input, textarea, select');
  const mod = e.ctrlKey || e.metaKey;
  if (mod && e.shiftKey && (e.key === 'N' || e.key === 'n')) { e.preventDefault(); newFolder(); return; }
  if (inInput) return;
  if (mod && (e.key === 'z' || e.key === 'Z')) { e.preventDefault(); undo(); }
  else if (mod && (e.key === 'a' || e.key === 'A')) { e.preventDefault(); selectAllLoaded(); }
  else if (e.key === 'F2') { e.preventDefault(); renameSelected(); }
  else if (e.key === 'Delete') { e.preventDefault(); deleteSelected(); }
  else if (e.key === 'Enter') {
    if (e.target instanceof Element && e.target.closest('button')) return; // 让按钮自己处理回车
    if (selCount() === 1 && !S.selAll) openRow(selectedRows()[0]);
  }
  else if (e.key === 'Backspace' || (e.altKey && e.key === 'ArrowUp')) { e.preventDefault(); goUp(); }
  else if (e.key === 'Escape') clearSel();
  else if (['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight'].includes(e.key)) {
    const grid = S.view === 'grid';
    if (!grid && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) return;
    e.preventDefault();
    if (!S.rows.length) return;
    let step = 1;
    if (grid && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      // 网格中上下移动一整行：计算每行有几个
      const tiles = $('#grid').children;
      step = 0;
      while (step < tiles.length && tiles[step].offsetTop === tiles[0].offsetTop) step++;
      step = Math.max(1, step);
    }
    const fwd = e.key === 'ArrowDown' || e.key === 'ArrowRight';
    let i = S.anchor < 0 ? (fwd ? -1 : S.rows.length) : S.anchor;
    i = fwd ? Math.min(S.rows.length - 1, i + step) : Math.max(0, i - step);
    if (e.shiftKey && S.anchor >= 0) { const a = S.anchor; selectRange(i); S.anchor = a; } else selectOnly(i);
    const tr = itemEl(i);
    if (tr) tr.scrollIntoView({ block: 'nearest' });
  }
});

// 可拖动的分栏
$$('.splitter').forEach(sp => {
  const pane = sp.dataset.for === 'tree' ? $('.tree-pane') : $('.stats-pane');
  const key = 'fo-w-' + sp.dataset.for;
  const saved = +localStorage.getItem(key);
  if (saved) pane.style.width = saved + 'px';
  sp.addEventListener('mousedown', e => {
    e.preventDefault();
    const startX = e.clientX, startW = pane.getBoundingClientRect().width;
    const dir = sp.dataset.for === 'tree' ? 1 : -1;
    sp.classList.add('drag');
    const mv = ev => { pane.style.width = Math.max(180, Math.min(700, startW + dir * (ev.clientX - startX))) + 'px'; };
    const up = () => {
      sp.classList.remove('drag');
      localStorage.setItem(key, Math.round(pane.getBoundingClientRect().width));
      document.removeEventListener('mousemove', mv);
      document.removeEventListener('mouseup', up);
    };
    document.addEventListener('mousemove', mv);
    document.addEventListener('mouseup', up);
  });
});

// ============================================================ 启动

(async function boot() {
  if (!TOKEN) {
    showWelcome(false);
    welcomeError('请通过运行“文件整理助手”程序来打开本页面（程序窗口里显示的地址）。');
    return;
  }
  let st;
  try { st = await GET('/api/state'); } catch (e) {
    showWelcome(false);
    welcomeError(e.message);
    return;
  }
  applyState(st);
  loadPlaces();
  if (st.scan.running) {
    S.lastScan = { path: st.scan.current };
    $('#welcomeForm').classList.add('hidden');
    $('#welcomeProgress').classList.remove('hidden');
    pollScan();
  } else if (st.scanned) {
    await enterApp(st);
  } else {
    showWelcome(false);
  }
})();
