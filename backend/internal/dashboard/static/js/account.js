import { escapeHTML, timeAgo } from './util.js';
import { getJSON, postJSON, putJSON, deleteJSON } from './api.js';
import { initTheme } from './theme.js';
import { toast } from './toast.js';
import { initVersion } from './version.js';
import { initUser } from './user.js';
import { addPasskey, passkeysSupported, friendlyError, deviceLabel } from './webauthn.js';

initTheme();
initVersion();

const passkeysEl = document.getElementById('passkeys');
const sessionsEl = document.getElementById('sessions');
const newLabel = document.getElementById('newLabel');
newLabel.value = deviceLabel();

initUser().then(u => {
  document.getElementById('heading').textContent = `Signed in as ${u.username}`;
});

// A short, readable name for a browser's User-Agent.
function describeAgent(ua) {
  if (!ua) return 'Unknown device';
  const os = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android'
    : /Mac/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : '';
  const browser = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox'
    : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : '';
  return [browser, os].filter(Boolean).join(' on ') || ua.slice(0, 40);
}

async function loadPasskeys() {
  try {
    const { passkeys } = await getJSON('/auth/passkeys');
    document.getElementById('oneKey').hidden = passkeys.length !== 1;
    passkeysEl.innerHTML = passkeys.map(p => `
      <li>
        <div class="main">
          <span class="title">${escapeHTML(p.label)}</span>
          ${p.backupEligible ? '<span class="badge neutral" title="This passkey syncs between your devices">synced</span>' : ''}
          <div class="sub">added ${new Date(p.createdAt).toLocaleDateString()} &middot; ${p.lastUsedAt ? 'last used ' + timeAgo(p.lastUsedAt) : 'never used to sign in'}</div>
        </div>
        <div class="actions">
          <button class="small" data-act="rename" data-id="${p.id}" data-label="${escapeHTML(p.label)}">Rename</button>
          <button class="small danger" data-act="remove" data-id="${p.id}" ${passkeys.length < 2 ? 'disabled title="This is your only passkey"' : ''}>Remove</button>
        </div>
      </li>`).join('');
  } catch (e) {
    passkeysEl.innerHTML = `<li class="bad">Could not load your passkeys: ${escapeHTML(e.message)}</li>`;
  }
}

async function loadSessions() {
  try {
    const { sessions } = await getJSON('/auth/sessions');
    sessionsEl.innerHTML = sessions.map(s => `
      <li>
        <div class="main">
          <span class="title">${escapeHTML(describeAgent(s.userAgent))}</span>
          ${s.current ? '<span class="badge ok">this device</span>' : ''}
          <div class="sub">${escapeHTML(s.ip || '')} &middot; last used ${timeAgo(s.lastUsedAt)}</div>
        </div>
        <div class="actions">
          ${s.current ? '' : `<button class="small" data-act="signout" data-id="${s.id}">Sign out</button>`}
        </div>
      </li>`).join('');
  } catch (e) {
    sessionsEl.innerHTML = `<li class="bad">Could not load your sessions: ${escapeHTML(e.message)}</li>`;
  }
}

// ---- passkeys ----
const renameDialog = document.getElementById('renameDialog');
const renameInput = document.getElementById('renameInput');
let renaming = null;

passkeysEl.addEventListener('click', async (ev) => {
  const btn = ev.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.dataset.id;
  if (btn.dataset.act === 'rename') {
    renaming = id;
    renameInput.value = btn.dataset.label;
    renameDialog.showModal();
  } else if (btn.dataset.act === 'remove') {
    if (!confirm('Remove this passkey? Devices that signed in with it are signed out.')) return;
    try {
      await deleteJSON(`/auth/passkeys/${id}`);
      toast('Passkey removed.', 'success');
    } catch (e) {
      toast(e.message, 'error');
    }
    loadPasskeys();
    loadSessions();
  }
});

document.getElementById('renameCancel').addEventListener('click', () => renameDialog.close());
document.getElementById('renameForm').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  try {
    await putJSON(`/auth/passkeys/${renaming}`, { label: renameInput.value });
    renameDialog.close();
    toast('Renamed.', 'success');
  } catch (e) {
    toast(e.message, 'error');
  }
  loadPasskeys();
});

document.getElementById('addForm').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  if (!passkeysSupported()) {
    toast('This browser can\'t use passkeys.', 'error');
    return;
  }
  const btn = document.getElementById('addBtn');
  btn.disabled = true;
  try {
    await addPasskey(newLabel.value.trim());
    toast('Passkey added.', 'success');
  } catch (e) {
    toast(friendlyError(e), 'error');
  }
  btn.disabled = false;
  loadPasskeys();
});

// ---- sessions ----
sessionsEl.addEventListener('click', async (ev) => {
  const btn = ev.target.closest('button[data-act="signout"]');
  if (!btn) return;
  try {
    await deleteJSON(`/auth/sessions/${btn.dataset.id}`);
    toast('Signed out.', 'success');
  } catch (e) {
    toast(e.message, 'error');
  }
  loadSessions();
});

document.getElementById('signOutBtn').addEventListener('click', async () => {
  try {
    await postJSON('/auth/logout');
  } finally {
    location.replace('/login.html');
  }
});

loadPasskeys();
loadSessions();
