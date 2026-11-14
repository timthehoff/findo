import { escapeHTML, timeAgo } from './util.js';
import { getJSON, postJSON, putJSON, deleteJSON } from './api.js';
import { initTheme } from './theme.js';
import { toast } from './toast.js';
import { initVersion } from './version.js';
import { initUser } from './user.js';
import { withConfirm } from './confirm.js';

initTheme();
initVersion();

const usersEl = document.getElementById('users');
let me = null;

initUser().then(u => {
  me = u;
  if (u.role !== 'admin') {
    location.replace('/');
    return;
  }
  load();
});

async function load() {
  try {
    const { users } = await getJSON('/users');
    usersEl.innerHTML = users.map(row).join('');
  } catch (e) {
    usersEl.innerHTML = `<li class="bad">Could not load users: ${escapeHTML(e.message)}</li>`;
  }
}

function row(u) {
  const self = u.id === me.id;
  const badges = [
    u.role === 'admin' ? '<span class="badge neutral">admin</span>' : '',
    u.disabled ? '<span class="badge bad">disabled</span>' : '',
    u.passkeys === 0 ? '<span class="badge bad" title="Hasn\'t registered a passkey yet; give them a sign-in link">no passkey yet</span>' : '',
    self ? '<span class="badge ok">you</span>' : '',
  ].join(' ');
  const attrs = `data-id="${u.id}" data-name="${escapeHTML(u.username)}"`;
  return `
    <li>
      <div class="main">
        <span class="title">${escapeHTML(u.username)}</span> ${badges}
        <div class="sub">${u.passkeys} passkey${u.passkeys === 1 ? '' : 's'} &middot; ${u.volumes} volume${u.volumes === 1 ? '' : 's'} &middot; ${u.lastLoginAt ? 'signed in ' + timeAgo(u.lastLoginAt) : 'never signed in'}</div>
      </div>
      <div class="actions">
        <button class="small" data-act="link" ${attrs} ${u.disabled ? 'disabled' : ''}>Sign-in link</button>
        <button class="small" data-act="role" ${attrs} data-role="${u.role === 'admin' ? 'user' : 'admin'}" ${self ? 'disabled title="You can\'t change your own role"' : ''}>${u.role === 'admin' ? 'Make user' : 'Make admin'}</button>
        <button class="small" data-act="disabled" ${attrs} data-disabled="${!u.disabled}" ${self ? 'disabled title="You can\'t disable yourself"' : ''}>${u.disabled ? 'Enable' : 'Disable'}</button>
        <button class="small danger" data-act="delete" ${attrs} data-volumes="${u.volumes}" ${self ? 'disabled title="You can\'t delete yourself"' : ''}>Delete</button>
      </div>
    </li>`;
}

// ---- the link dialog ----
const linkDialog = document.getElementById('linkDialog');
const linkBox = document.getElementById('linkBox');

function showLink(title, text, link) {
  document.getElementById('linkTitle').textContent = title;
  document.getElementById('linkText').textContent = text;
  linkBox.textContent = link;
  linkDialog.showModal();
}

document.getElementById('copyLink').addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(linkBox.textContent);
    toast('Link copied.', 'success');
  } catch {
    // No clipboard access (plain http, old browser): select it so it can be copied by hand.
    const range = document.createRange();
    range.selectNodeContents(linkBox);
    getSelection().removeAllRanges();
    getSelection().addRange(range);
    toast('Press and hold to copy.', 'info');
  }
});

// ---- actions ----
usersEl.addEventListener('click', async (ev) => {
  const btn = ev.target.closest('button[data-act]');
  if (!btn) return;
  const { id, name } = btn.dataset;
  try {
    switch (btn.dataset.act) {
      case 'link': {
        const out = await withConfirm(() => postJSON(`/users/${id}/enrollment`));
        showLink(`Sign-in link for ${name}`, 'Open this on their device to register a passkey.', out.enrollmentLink);
        break;
      }
      case 'role':
        await withConfirm(() => putJSON(`/users/${id}`, { role: btn.dataset.role }));
        toast(`${name} is now ${btn.dataset.role === 'admin' ? 'an administrator' : 'a regular user'}.`, 'success');
        break;
      case 'disabled': {
        const disabled = btn.dataset.disabled === 'true';
        await withConfirm(() => putJSON(`/users/${id}`, { disabled }));
        toast(disabled ? `${name} is disabled and signed out.` : `${name} can sign in again.`, 'success');
        break;
      }
      case 'delete': {
        const n = Number(btn.dataset.volumes);
        const also = n ? ` Their ${n} volume${n === 1 ? '' : 's'} and everything indexed under ${n === 1 ? 'it' : 'them'} will be deleted too.` : '';
        if (!confirm(`Delete ${name}?${also} This cannot be undone.`)) return;
        await withConfirm(() => deleteJSON(`/users/${id}`));
        toast(`${name} deleted.`, 'success');
        break;
      }
    }
  } catch (e) {
    toast(e.message, 'error');
  }
  load();
});

// ---- add user ----
const userDialog = document.getElementById('userDialog');
document.getElementById('addUserBtn').addEventListener('click', () => {
  document.getElementById('userForm').reset();
  userDialog.showModal();
});
document.getElementById('userCancel').addEventListener('click', () => userDialog.close());
document.getElementById('userForm').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  try {
    const body = {
      username: document.getElementById('uName').value.trim(),
      role: document.getElementById('uRole').value,
    };
    const out = await withConfirm(() => postJSON('/users', body));
    userDialog.close();
    showLink(`Welcome, ${out.user.username}`, 'Open this on their device to register a passkey.', out.enrollmentLink);
    load();
  } catch (e) {
    toast(e.message, 'error');
  }
});
