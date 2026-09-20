import { escapeHTML, humanSize, debounce } from './util.js';
import { getJSON } from './api.js';
import { initTheme } from './theme.js';
import { toast } from './toast.js';

initTheme();

const volumeSelect = document.getElementById('volumeFilter');
const qInput = document.getElementById('q');
const breadcrumbsEl = document.getElementById('breadcrumbs');
const errEl = document.getElementById('err');
const resultsBody = document.querySelector('#results tbody');

let volumesById = new Map();
let currentPath = ''; // browse-mode path within the selected volume

// fileTypeBadge maps an extension to a short label/color class so results
// are scannable at a glance without a full icon set.
const EXT_CLASSES = {
  pdf: 'pdf',
  doc: 'doc', docx: 'doc', pages: 'doc', rtf: 'doc',
  xls: 'xls', xlsx: 'xls', csv: 'xls', numbers: 'xls',
  ppt: 'ppt', pptx: 'ppt', key: 'ppt',
  jpg: 'img', jpeg: 'img', png: 'img', gif: 'img', heic: 'img', webp: 'img',
  mp4: 'vid', mov: 'vid', avi: 'vid', mkv: 'vid',
  zip: 'zip', tar: 'zip', gz: 'zip', '7z': 'zip',
  txt: 'txt', md: 'txt',
};

function fileTypeBadge(ext, isDir) {
  if (isDir) return { label: 'DIR', cls: 'dir' };
  const e = (ext || '').toLowerCase();
  return { label: e ? e.toUpperCase() : 'FILE', cls: EXT_CLASSES[e] || 'file' };
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

async function loadVolumes() {
  try {
    const body = await getJSON('/volumes');
    const volumes = body.volumes || [];
    volumesById = new Map(volumes.map(v => [String(v.id), v]));

    const prev = volumeSelect.value;
    volumeSelect.innerHTML = '<option value="">All volumes</option>'
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
  ).join(' <span class="sep">/</span> ');

  breadcrumbsEl.querySelectorAll('a').forEach(a => {
    a.addEventListener('click', (ev) => {
      ev.preventDefault();
      currentPath = a.dataset.path;
      loadBrowse();
    });
  });
}

function renderResults(entries, query) {
  if (entries.length === 0) {
    resultsBody.innerHTML = '<tr><td colspan="6" class="muted">No results — try a different scent.</td></tr>';
    return;
  }

  resultsBody.innerHTML = entries.map(f => {
    const badge = fileTypeBadge(f.ext, f.isDir);
    const vol = volumesById.get(String(f.volumeId));
    const nameHTML = highlight(f.name, query);
    const nameCell = f.isDir
      ? `<a href="#" data-dir="${escapeHTML(f.path)}" data-vol="${f.volumeId}">${nameHTML}</a>`
      : `<a href="/files/content?volume=${f.volumeId}&path=${encodeURIComponent(f.path)}" target="_blank" rel="noopener">${nameHTML}</a>`;
    return `
      <tr>
        <td>${nameCell}</td>
        <td><span class="badge type-${badge.cls}">${badge.label}</span></td>
        <td>${vol ? escapeHTML(vol.name) : f.volumeId}</td>
        <td>${escapeHTML(f.dir ? '/' + f.dir : '/')}</td>
        <td>${f.isDir ? '' : humanSize(f.size)}</td>
        <td>${new Date(f.modTime * 1000).toLocaleString()}</td>
      </tr>
    `;
  }).join('');

  resultsBody.querySelectorAll('a[data-dir]').forEach(a => {
    a.addEventListener('click', (ev) => {
      ev.preventDefault();
      volumeSelect.value = a.dataset.vol;
      currentPath = a.dataset.dir;
      qInput.value = '';
      loadBrowse();
    });
  });
}

async function loadBrowse() {
  errEl.textContent = '';
  renderBreadcrumbs();

  const volId = volumeSelect.value;
  if (!volId) {
    resultsBody.innerHTML = '<tr><td colspan="6" class="muted">Pick a volume to browse, or type above to search across all volumes.</td></tr>';
    return;
  }

  try {
    const body = await getJSON(`/files?volume=${volId}&path=${encodeURIComponent(currentPath)}`);
    renderResults(body.entries || [], '');
  } catch (e) {
    errEl.textContent = e.message;
  }
}

async function performSearch(query) {
  errEl.textContent = '';
  renderBreadcrumbs();

  const volId = volumeSelect.value;
  try {
    let url = `/search?q=${encodeURIComponent(query)}&limit=100`;
    if (volId) url += `&volume=${volId}`;
    const body = await getJSON(url);
    renderResults(body.entries || [], query);
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

qInput.addEventListener('input', () => debouncedSearch(qInput.value.trim()));

volumeSelect.addEventListener('change', () => {
  currentPath = '';
  const query = qInput.value.trim();
  if (query) {
    performSearch(query);
  } else {
    loadBrowse();
  }
});

(async function init() {
  await loadVolumes();
  const query = qInput.value.trim();
  if (query) {
    await performSearch(query);
  } else {
    await loadBrowse();
  }
})();
