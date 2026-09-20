import { escapeHTML, humanSize, humanDuration, timeAgo } from './util.js';
import { getJSON, postJSON, putJSON, deleteJSON } from './api.js';

const volumeDialog = document.getElementById('volumeDialog');
const volumeForm = document.getElementById('volumeForm');
const testResult = document.getElementById('testResult');

// Volume ids whose crawl history panel is currently expanded — re-rendered
// on every refresh so the panel stays live while open.
const expandedHistory = new Set();

async function refreshStats() {
  const cards = document.getElementById('cards');
  try {
    const [health, stats] = await Promise.all([getJSON('/health'), getJSON('/stats')]);
    const healthClass = health.status === 'ok' ? 'ok' : 'bad';
    cards.innerHTML = `
      <div class="card"><div class="label">Health</div><div class="value ${healthClass}">${health.status}</div></div>
      <div class="card"><div class="label">Files (all volumes)</div><div class="value">${stats.fileCount ?? 0}</div></div>
      <div class="card"><div class="label">Dirs (all volumes)</div><div class="value">${stats.dirCount ?? 0}</div></div>
      <div class="card"><div class="label">Volumes online</div><div class="value">${(health.volumes || []).filter(v => v.ok).length} / ${(health.volumes || []).length}</div></div>
    `;
  } catch (e) {
    cards.textContent = 'failed to load health/stats: ' + e.message;
  }
}

async function refreshVolumes() {
  const container = document.getElementById('volumes');
  try {
    const body = await getJSON('/volumes');
    const volumes = body.volumes || [];

    container.innerHTML = volumes.length === 0
      ? '<p class="muted">No volumes configured yet. A good dog needs a yard to search — add one below.</p>'
      : volumes.map(renderVolumeCard).join('');

    container.querySelectorAll('button[data-action]').forEach(btn => {
      btn.addEventListener('click', () => handleVolumeAction(btn.dataset.action, btn.dataset.id, volumes));
    });
    for (const id of expandedHistory) {
      loadCrawlHistory(id);
    }
  } catch (e) {
    container.textContent = 'failed to load volumes: ' + e.message;
  }
}

function renderVolumeCard(v) {
  const statusBadge = v.connected
    ? '<span class="badge ok">connected</span>'
    : '<span class="badge bad">' + (v.lastTestError ? 'error' : 'not connected') + '</span>';
  const crawlBadge = v.crawling ? '<span class="badge ok">crawling…</span>' : '';
  const watchBadge = v.connected
    ? (v.watchConnected ? '<span class="badge ok">watching</span>' : '<span class="badge bad">watch down</span>')
    : '';

  const errLine = v.lastCrawlError ? `<div class="volume-meta bad">Last crawl error: ${escapeHTML(v.lastCrawlError)}</div>` : '';
  const testErrLine = (!v.connected && v.lastTestError) ? `<div class="volume-meta bad">${escapeHTML(v.lastTestError)}</div>` : '';

  let lastCrawlLine = 'last crawl: never';
  if (v.lastCrawlStartedAt) {
    const when = v.lastCrawlFinishedAt ? timeAgo(v.lastCrawlFinishedAt) : 'in progress';
    const parts = [`last crawl (${escapeHTML(v.lastCrawlTrigger || 'manual')}): ${when}`];
    if (v.lastCrawlDurationMs) parts.push(humanDuration(v.lastCrawlDurationMs));
    if (v.lastCrawlFilesSeen !== undefined) {
      let seen = `${v.lastCrawlFilesSeen} files`;
      if (v.lastCrawlFilesRemoved) seen += `, ${v.lastCrawlFilesRemoved} removed`;
      parts.push(seen);
    }
    if (v.lastCrawlBytesIndexed) parts.push(humanSize(v.lastCrawlBytesIndexed));
    lastCrawlLine = parts.join(' &middot; ');
  }

  let watchLine = '';
  if (v.connected) {
    watchLine = 'change-notify: ' + (v.watchConnected ? 'connected' : 'reconnecting');
    if (v.lastEventAt) watchLine += `, last event ${timeAgo(v.lastEventAt)}`;
    if (v.resyncCount) watchLine += `, ${v.resyncCount} resync${v.resyncCount === 1 ? '' : 's'}`;
  }

  const expanded = expandedHistory.has(String(v.id));

  return `
    <div class="volume" data-id="${v.id}">
      <div class="volume-header">
        <div>
          <span class="name">${escapeHTML(v.name)}</span>
          ${statusBadge} ${crawlBadge} ${watchBadge}
        </div>
        <div class="volume-actions">
          <button class="small" data-action="reindex" data-id="${v.id}" ${v.crawling ? 'disabled' : ''}>Reindex</button>
          <button class="small" data-action="history" data-id="${v.id}">${expanded ? 'Hide history' : 'History'}</button>
          <button class="small" data-action="test" data-id="${v.id}">Test</button>
          <button class="small" data-action="edit" data-id="${v.id}">Edit</button>
          <button class="small danger" data-action="delete" data-id="${v.id}">Delete</button>
        </div>
      </div>
      <div class="volume-meta">
        \\\\${escapeHTML(v.host)}\\${escapeHTML(v.share)} as ${escapeHTML(v.username)}
        &middot; ${v.fileCount ?? 0} files, ${v.dirCount ?? 0} dirs
        ${v.enabled ? '' : ' &middot; <strong>disabled</strong>'}
      </div>
      <div class="volume-meta">${lastCrawlLine}</div>
      ${watchLine ? `<div class="volume-meta">${watchLine}</div>` : ''}
      ${errLine}${testErrLine}
      ${expanded ? `<div class="history" id="history-${v.id}">loading…</div>` : ''}
    </div>
  `;
}

async function loadCrawlHistory(id) {
  const el = document.getElementById(`history-${id}`);
  if (!el) return;
  try {
    const body = await getJSON(`/volumes/${id}/crawl-runs?limit=10`);
    const runs = body.runs || [];
    if (runs.length === 0) {
      el.innerHTML = '<div class="history-empty">No crawls recorded yet.</div>';
      return;
    }
    el.innerHTML = `
      <table>
        <thead><tr><th>Started</th><th>Trigger</th><th>Duration</th><th>Files</th><th>Removed</th><th>Size</th><th>Result</th></tr></thead>
        <tbody>
          ${runs.map(r => `
            <tr>
              <td>${new Date(r.startedAt).toLocaleString()}</td>
              <td>${escapeHTML(r.trigger)}</td>
              <td>${r.finishedAt ? humanDuration(r.durationMs) : 'running…'}</td>
              <td>${r.filesSeen ?? 0}</td>
              <td>${r.filesRemoved ?? 0}</td>
              <td>${humanSize(r.bytesIndexed)}</td>
              <td>${r.error ? `<span class="bad">${escapeHTML(r.error)}</span>` : (r.finishedAt ? '<span class="ok">ok</span>' : '')}</td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    `;
  } catch (e) {
    el.textContent = 'failed to load crawl history: ' + e.message;
  }
}

async function handleVolumeAction(action, id, volumes) {
  if (action === 'reindex') {
    await postJSON(`/volumes/${id}/reindex`).catch(() => {});
    refreshVolumes();
  } else if (action === 'history') {
    if (expandedHistory.has(String(id))) {
      expandedHistory.delete(String(id));
    } else {
      expandedHistory.add(String(id));
    }
    refreshVolumes();
  } else if (action === 'test') {
    try {
      const body = await postJSON(`/volumes/${id}/test`);
      alert(body.ok ? 'Connection OK' : 'Connection failed: ' + body.error);
    } catch (e) {
      alert('Test failed: ' + e.message);
    }
    refreshVolumes();
  } else if (action === 'edit') {
    openVolumeDialog(volumes.find(v => String(v.id) === String(id)));
  } else if (action === 'delete') {
    if (!confirm('Delete this volume and everything indexed under it? This cannot be undone.')) return;
    await deleteJSON(`/volumes/${id}`).catch(() => {});
    refreshVolumes();
    refreshStats();
  }
}

function openVolumeDialog(v) {
  volumeForm.reset();
  testResult.textContent = '';
  document.getElementById('volumeId').value = v ? v.id : '';
  document.getElementById('volumeDialogTitle').textContent = v ? 'Edit volume' : 'Add volume';
  document.getElementById('vName').value = v ? v.name : '';
  document.getElementById('vHost').value = v ? v.host : '';
  document.getElementById('vShare').value = v ? v.share : '';
  document.getElementById('vUsername').value = v ? v.username : '';
  document.getElementById('vPassword').placeholder = v ? 'leave blank to keep the current password' : '';
  document.getElementById('vPassword').required = !v;
  volumeDialog.showModal();
}

function currentVolumeInput() {
  return {
    name: document.getElementById('vName').value,
    host: document.getElementById('vHost').value,
    share: document.getElementById('vShare').value,
    username: document.getElementById('vUsername').value,
    password: document.getElementById('vPassword').value,
  };
}

document.getElementById('addVolumeBtn').addEventListener('click', () => openVolumeDialog(null));
document.getElementById('cancelBtn').addEventListener('click', () => volumeDialog.close());

document.getElementById('testBtn').addEventListener('click', async () => {
  const input = currentVolumeInput();
  const id = document.getElementById('volumeId').value;
  testResult.className = '';
  testResult.textContent = 'Testing…';
  try {
    const usesStoredPassword = id && !input.password;
    const body = usesStoredPassword
      ? await postJSON(`/volumes/${id}/test`)
      : await postJSON('/volumes/test', input);
    testResult.textContent = body.ok ? '✓ Connected' : '✗ ' + body.error;
    testResult.className = body.ok ? 'ok' : 'bad';
  } catch (e) {
    testResult.textContent = 'Test failed: ' + e.message;
    testResult.className = 'bad';
  }
});

volumeForm.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const id = document.getElementById('volumeId').value;
  const input = currentVolumeInput();
  if (!input.password) delete input.password;

  try {
    if (id) {
      await putJSON(`/volumes/${id}`, input);
    } else {
      await postJSON('/volumes', input);
    }
  } catch (e) {
    testResult.textContent = e.message;
    testResult.className = 'bad';
    return;
  }
  volumeDialog.close();
  refreshVolumes();
  refreshStats();
});

function refresh() {
  refreshStats();
  refreshVolumes();
}

refresh();
setInterval(refresh, 5000);
