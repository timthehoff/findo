import { escapeHTML, humanSize, debounce, lineIcon } from './util.js';
import { getJSON } from './api.js';
import { initTheme } from './theme.js';
import { toast } from './toast.js';
import { initVersion } from './version.js';

initTheme();
initVersion();

const volumeSelect = document.getElementById('volumeFilter');
const qInput = document.getElementById('q');
const breadcrumbsEl = document.getElementById('breadcrumbs');
const errEl = document.getElementById('err');
const resultsEl = document.getElementById('results');
const countEl = document.getElementById('count');
const showHiddenEl = document.getElementById('showHidden');

let volumesById = new Map();
let currentPath = ''; // browse-mode path within the selected volume
let lastQuery = '';   // previous search text, to decide push vs replace in the URL

// The page state (search text, selected volume, browse path) lives in the
// query string so back/forward, reloads and shared links all work. Volumes
// appear by name, which the server keeps unique (case-insensitively).
function writeURL(push) {
  const params = new URLSearchParams();
  const q = qInput.value.trim();
  if (q) params.set('q', q);
  const vol = volumesById.get(volumeSelect.value);
  if (vol) params.set('volume', vol.name);
  if (!q && volumeSelect.value && currentPath) params.set('path', currentPath);
  if (showHiddenEl.checked) params.set('hidden', '1');
  const qs = params.toString();
  const url = location.pathname + (qs ? '?' + qs : '');
  if (url === location.pathname + location.search) return;
  history[push ? 'pushState' : 'replaceState'](null, '', url);
}

// updateTitle puts the current search or folder in the tab title, most
// specific part first so it survives narrow tabs and reads well in history.
function updateTitle() {
  const vol = volumesById.get(volumeSelect.value);
  const q = qInput.value.trim();
  const folder = currentPath ? currentPath.split('/').pop() : '';
  let parts;
  if (q) {
    parts = [vol ? `${q} in ${vol.name}` : q];
  } else if (vol) {
    parts = folder ? [folder, vol.name] : [vol.name];
  } else {
    parts = ['Search'];
  }
  document.title = [...parts, 'Findo'].join(' — ');
}

function updatePlaceholder() {
  const vol = volumesById.get(volumeSelect.value);
  qInput.placeholder = vol ? `Search in ${vol.name}…` : 'Search all volumes…';
}

// restoreFromURL applies the query string to the controls and runs the
// matching search or browse, without touching history.
async function restoreFromURL() {
  const params = new URLSearchParams(location.search);
  const wanted = (params.get('volume') || '').toLowerCase();
  const vol = [...volumesById.values()].find(v => v.name.toLowerCase() === wanted);
  volumeSelect.value = vol ? String(vol.id) : '';
  qInput.value = params.get('q') || '';
  currentPath = volumeSelect.value ? (params.get('path') || '') : '';
  showHiddenEl.checked = params.get('hidden') === '1';
  lastQuery = qInput.value.trim();
  updatePlaceholder();
  if (lastQuery) {
    await performSearch(lastQuery);
  } else {
    await loadBrowse();
  }
}

// File kinds (and so tile colors) come from the server's registry
// (/filetypes), the same table that decides how a file is served.
let fileKinds = {};

async function loadFileTypes() {
  try {
    const body = await getJSON('/filetypes');
    fileKinds = Object.fromEntries(Object.entries(body.types || {}).map(([ext, t]) => [ext, t.kind]));
  } catch {
    // Tiles fall back to the plain "file" color; nothing else depends on this.
  }
}

// fileTypeBadge maps an extension to a short label/color class so results
// are scannable at a glance without a full icon set.
function fileTypeBadge(ext, isDir) {
  if (isDir) return { label: 'DIR', cls: 'dir' };
  const e = (ext || '').toLowerCase();
  return { label: e ? e.toUpperCase() : 'FILE', cls: fileKinds[e] || 'file' };
}

// highlight wraps the first case-insensitive match of query in name with
// <mark>, so a search result shows exactly what matched.
function highlight(name, query) {
  if (!query) return escapeHTML(name);
  const idx = name.toLowerCase().indexOf(query.toLowerCase());
  if (idx === -1) return escapeHTML(name);
  return escapeHTML(name.slice(0, idx))
    + '<mark>' + escapeHTML(name.slice(idx, idx + query.length)) + '</mark>'
    + escapeHTML(name.slice(idx + query.length));
}

// smbURL builds the address a user pastes into Finder's or Files'
// "Connect to Server". Segments are encoded individually so slashes survive.
function smbURL(vol, path) {
  const segs = [vol.share, ...path.split('/')].map(encodeURIComponent);
  return `smb://${vol.host}/${segs.join('/')}`;
}

// copyText falls back to a hidden textarea because navigator.clipboard
// needs a secure context, and the dashboard is usually served over plain
// http on the LAN.
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return;
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    if (!ok) throw new Error('copy failed');
  }
}

async function loadVolumes() {
  try {
    const body = await getJSON('/volumes');
    const volumes = body.volumes || [];
    volumesById = new Map(volumes.map(v => [String(v.id), v]));

    const prev = volumeSelect.value;
    volumeSelect.innerHTML = '<option value="">all volumes</option>'
      + volumes.map(v => `<option value="${v.id}">${escapeHTML(v.name)}</option>`).join('');
    volumeSelect.value = volumes.some(v => String(v.id) === prev) ? prev : '';
  } catch (e) {
    errEl.textContent = 'Could not fetch volumes: ' + e.message;
    toast('Could not fetch volumes: ' + e.message, 'error');
  }
}

function renderBreadcrumbs() {
  const volId = volumeSelect.value;
  if (!volId || qInput.value.trim()) {
    breadcrumbsEl.innerHTML = '';
    return;
  }

  const vol = volumesById.get(volId);
  const parts = currentPath ? currentPath.split('/') : [];
  const crumbs = [{ label: vol ? vol.name : 'root', path: '' }];
  let acc = '';
  for (const p of parts) {
    acc = acc ? acc + '/' + p : p;
    crumbs.push({ label: p, path: acc });
  }

  breadcrumbsEl.innerHTML = crumbs.map((c, i) =>
    i === crumbs.length - 1
      ? `<span>${escapeHTML(c.label)}</span>`
      : `<a href="#" data-path="${escapeHTML(c.path)}">${escapeHTML(c.label)}</a>`
  ).join(' <span class="sep">›</span> ');

  breadcrumbsEl.querySelectorAll('a').forEach(a => {
    a.addEventListener('click', (ev) => {
      ev.preventDefault();
      currentPath = a.dataset.path;
      writeURL(true);
      loadBrowse();
    });
  });
}

const ICON_FOLDER = lineIcon('<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>', 22);
const ICON_JUMP = lineIcon('<path d="M9 14 4 9l5-5"/><path d="M20 20v-7a4 4 0 0 0-4-4H4"/>');
const ICON_COPY = lineIcon('<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>');
const ICON_DOWNLOAD = lineIcon('<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>');

const SEARCH_LIMIT = 100;

// hiddenParam opts the API request in to dotfiles when the toggle is on.
const hiddenParam = () => (showHiddenEl.checked ? '&hidden=1' : '');

function showMessage(html) {
  countEl.textContent = '';
  resultsEl.innerHTML = `<li class="empty-state muted">${html}</li>`;
}

function renderCount(n, query, dateLabel) {
  if (query) {
    countEl.textContent = n >= SEARCH_LIMIT
      ? `Showing the first ${SEARCH_LIMIT} results — narrow your search to see more`
      : `${n} ${n === 1 ? 'result' : 'results'}`;
    if (dateLabel) countEl.textContent += ` · modified ${dateLabel}, newest first`;
  } else {
    countEl.textContent = `${n} ${n === 1 ? 'item' : 'items'}`;
  }
}

// `query` is what the user typed; `nameQuery` is the part left after a date
// phrase ("report last month" -> "report") and is what rows highlight.
function renderResults(entries, query, nameQuery = query, dateLabel = '') {
  if (entries.length === 0) {
    showMessage(query
      ? `No results for “${escapeHTML(query)}” — Findo sniffed everywhere. Try a different scent. 🐕`
      : 'Nothing in this folder.');
    return;
  }
  renderCount(entries.length, query, dateLabel);

  // `query` is empty when browsing a folder, where every row already lives
  // in the current location, so the "show in folder" button is omitted.
  resultsEl.innerHTML = entries.map(f => {
    const badge = fileTypeBadge(f.ext, f.isDir);
    const vol = volumesById.get(String(f.volumeId));
    const volName = vol ? vol.name : String(f.volumeId);
    // The name suffix is cosmetic: it becomes the browser tab's title.
    const contentURL = `/files/content/${encodeURIComponent(f.name)}?volume=${f.volumeId}&path=${encodeURIComponent(f.path)}`;
    const sharePath = vol ? `${vol.share}/${f.path}` : f.path;
    const parent = f.path.includes('/') ? f.path.slice(0, f.path.lastIndexOf('/')) : '';
    const nameHTML = highlight(f.name, nameQuery);
    // The type tile links exactly where the name does, so either one opens the
    // file or folder. It's a mouse/touch target only (the name is the real
    // link for keyboards and screen readers), hence tabindex -1 and aria-hidden.
    const target = f.isDir
      ? `href="#" data-dir="${escapeHTML(f.path)}" data-vol="${f.volumeId}"`
      : `href="${contentURL}" target="_blank" rel="noopener"`;
    const name = `<a class="result-name" ${target}>${nameHTML}</a>`;
    const tile = `<a class="type-tile type-${badge.cls}${badge.label.length > 4 ? ' long' : ''}" ${target} tabindex="-1" aria-hidden="true" title="${badge.label}">${f.isDir ? ICON_FOLDER : badge.label}</a>`;
    const where = [`<span class="vol">${escapeHTML(volName)}</span>`, ...(parent ? parent.split('/') : []).map(escapeHTML)]
      .join(' <span class="sep">›</span> ');
    const date = new Date(f.modTime * 1000);
    const meta = [
      f.isDir ? '' : humanSize(f.size),
      date.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }),
    ].filter(Boolean).map(part => `<span>${part}</span>`).join(' · ');
    return `
      <li class="result">
        ${tile}
        <div class="result-main">
          ${name}
          <span class="result-where" title="${escapeHTML(sharePath)}">${where}</span>
        </div>
        <div class="result-meta" title="${escapeHTML(date.toLocaleString())}">${meta}</div>
        <div class="result-actions">
          ${query ? `<a class="icon-btn" href="#" data-dir="${escapeHTML(parent)}" data-vol="${f.volumeId}" title="Show in folder" aria-label="Show in folder">${ICON_JUMP}</a>` : ''}
          <button class="icon-btn" data-copy="${escapeHTML(f.path)}" data-vol="${f.volumeId}" title="Copy SMB path" aria-label="Copy SMB path">${ICON_COPY}</button>
          ${f.isDir ? `<span class="icon-btn" style="visibility:hidden" aria-hidden="true">${ICON_DOWNLOAD}</span>` : `<a class="icon-btn" href="${contentURL}&download=1" title="Download" aria-label="Download">${ICON_DOWNLOAD}</a>`}
        </div>
      </li>
    `;
  }).join('');

  resultsEl.querySelectorAll('button[data-copy]').forEach(b => {
    b.addEventListener('click', async () => {
      const vol = volumesById.get(b.dataset.vol);
      if (!vol) return;
      try {
        await copyText(smbURL(vol, b.dataset.copy));
        toast('Path copied — paste it into Connect to Server', 'success');
      } catch (e) {
        toast('Could not copy: ' + e.message, 'error');
      }
    });
  });

  resultsEl.querySelectorAll('a[data-dir]').forEach(a => {
    a.addEventListener('click', (ev) => {
      ev.preventDefault();
      volumeSelect.value = a.dataset.vol;
      currentPath = a.dataset.dir;
      qInput.value = '';
      lastQuery = '';
      updatePlaceholder();
      writeURL(true);
      loadBrowse();
    });
  });
}

async function loadBrowse() {
  errEl.textContent = '';
  updateTitle();
  renderBreadcrumbs();

  const volId = volumeSelect.value;
  if (!volId) {
    countEl.textContent = '';
    resultsEl.innerHTML = '';
    return;
  }

  try {
    const body = await getJSON(`/files?volume=${volId}&path=${encodeURIComponent(currentPath)}${hiddenParam()}`);
    renderResults(body.entries || [], '');
  } catch (e) {
    errEl.textContent = e.message;
  }
}

async function performSearch(query) {
  errEl.textContent = '';
  updateTitle();
  renderBreadcrumbs();

  const volId = volumeSelect.value;
  try {
    // tz so "today"/"last month" start at the viewer's midnight, not the server's.
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    let url = `/search?q=${encodeURIComponent(query)}&limit=${SEARCH_LIMIT}${hiddenParam()}`;
    if (tz) url += `&tz=${encodeURIComponent(tz)}`;
    if (volId) url += `&volume=${volId}`;
    const body = await getJSON(url);
    renderResults(body.entries || [], query, body.nameQuery, body.modified ? body.modified.phrase : '');
  } catch (e) {
    errEl.textContent = e.message;
  }
}

const debouncedSearch = debounce((query) => {
  if (query) {
    performSearch(query);
  } else {
    loadBrowse();
  }
}, 250);

qInput.addEventListener('input', () => {
  const query = qInput.value.trim();
  // One history entry per search session, not per keystroke.
  writeURL(lastQuery === '' && query !== '');
  lastQuery = query;
  debouncedSearch(query);
});

volumeSelect.addEventListener('change', () => {
  currentPath = '';
  updatePlaceholder();
  writeURL(true);
  const query = qInput.value.trim();
  if (query) {
    performSearch(query);
  } else {
    loadBrowse();
  }
});

showHiddenEl.addEventListener('change', () => {
  writeURL(true);
  const query = qInput.value.trim();
  if (query) {
    performSearch(query);
  } else {
    loadBrowse();
  }
});

window.addEventListener('popstate', restoreFromURL);

(async function init() {
  await Promise.all([loadVolumes(), loadFileTypes()]);
  await restoreFromURL();
})();
