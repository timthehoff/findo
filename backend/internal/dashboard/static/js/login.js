import { getJSON } from './api.js';
import { initTheme } from './theme.js';
import { signIn, passkeysSupported, friendlyError } from './webauthn.js';

initTheme();

const btn = document.getElementById('signInBtn');
const msg = document.getElementById('msg');

// Only ever go back to a page on this site.
function nextPage() {
  const raw = new URLSearchParams(location.search).get('next') || '/';
  try {
    const u = new URL(raw, location.origin);
    if (u.origin === location.origin && !u.pathname.startsWith('//')) return u.pathname + u.search;
  } catch {
    // fall through
  }
  return '/';
}

function fail(text) {
  msg.className = 'auth-msg bad';
  msg.textContent = text;
}

async function start() {
  if (!passkeysSupported()) {
    fail('This browser can\'t use passkeys. Try a current Safari, Chrome, Edge or Firefox.');
    return;
  }
  try {
    await getJSON('/auth/me');
    location.replace(nextPage()); // already signed in
    return;
  } catch (e) {
    if (e.data && e.data.setupRequired) {
      document.getElementById('setup').hidden = false;
      return;
    }
  }
  btn.disabled = false;
}

btn.addEventListener('click', async () => {
  btn.disabled = true;
  msg.className = 'auth-msg muted';
  msg.textContent = 'Waiting for your passkey…';
  try {
    await signIn();
    location.replace(nextPage());
  } catch (e) {
    fail(e.status === 429 ? e.message : e.status === 401 ? 'Sign-in failed. Is this device\'s passkey registered with Findo?' : friendlyError(e));
    btn.disabled = false;
  }
});

start();
