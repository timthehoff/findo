import { escapeHTML } from './util.js';
import { getJSON } from './api.js';
import { initTheme } from './theme.js';
import { initVersion } from './version.js';

initTheme();
initVersion();

const params = new URLSearchParams(location.search);
const volumeId = params.get('volume') || '';
const path = params.get('path') || '';
const query = params.get('q') || '';

const errEl = document.getElementById('err');
const chunksEl = document.getElementById('chunks');

// highlight wraps each query word in <mark> and escapes everything else, so
// file text is only ever inserted as text. Words are letters and digits only
// (so they're safe inside a RegExp) and match anywhere, case-insensitively.
function highlight(text, words) {
  if (words.length === 0) return escapeHTML(text);
  const re = new RegExp(words.join('|'), 'giu');
  let out = '';
  let last = 0;
  for (const m of text.matchAll(re)) {
    out += escapeHTML(text.slice(last, m.index)) + '<mark>' + escapeHTML(m[0]) + '</mark>';
    last = m.index + m[0].length;
  }
  return out + escapeHTML(text.slice(last));
}

async function volumeName() {
  try {
    const body = await getJSON('/volumes');
    const v = (body.volumes || []).find(v => String(v.id) === volumeId);
    return v ? v.name : '';
  } catch {
    return '';
  }
}

async function init() {
  if (!volumeId || !path) {
    errEl.textContent = 'Missing volume or path. Open a file’s text from a search result.';
    return;
  }

  const name = path.split('/').pop();
  document.getElementById('fileName').textContent = name;
  document.title = `${name} — Findo`;

  const q = `volume=${encodeURIComponent(volumeId)}&path=${encodeURIComponent(path)}`;
  const content = `/files/content/${encodeURIComponent(name)}?${q}`;
  document.getElementById('openLink').href = content;
  document.getElementById('downloadLink').href = content + '&download=1';
  document.getElementById('actions').hidden = false;

  const parent = path.includes('/') ? path.slice(0, path.lastIndexOf('/')).split('/') : [];
  volumeName().then(vol => {
    document.getElementById('where').innerHTML = [vol || `volume ${volumeId}`, ...parent]
      .map(escapeHTML).join(' <span class="sep">›</span> ');
  });

  let body;
  try {
    body = await getJSON(`/files/text?${q}&format=json`);
  } catch (e) {
    errEl.textContent = e.message;
    return;
  }

  const words = (query.match(/[\p{L}\p{N}]{2,}/gu) || []);
  chunksEl.innerHTML = (body.chunks || [])
    .map((c, i) => `<section class="chunk" id="chunk-${i}">${highlight(c, words)}</section>`)
    .join('');
  document.getElementById('note').hidden = false;

  // A search hit links to #chunk-N: scroll there once the text exists.
  const target = location.hash && document.getElementById(location.hash.slice(1));
  if (target) target.scrollIntoView({ block: 'start' });
}

init();
