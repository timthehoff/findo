import { initTheme } from './theme.js';
import { join, passkeysSupported, friendlyError, deviceLabel } from './webauthn.js';

initTheme();

// The link is /join#<token>. The token lives in the fragment so it never
// reaches a server log or a Referer header.
const token = location.hash.slice(1);
const form = document.getElementById('form');
const btn = document.getElementById('goBtn');
const msg = document.getElementById('msg');
const username = document.getElementById('username');
const label = document.getElementById('label');
label.value = deviceLabel();

function say(text, bad) {
  msg.className = 'auth-msg ' + (bad ? 'bad' : 'muted');
  msg.textContent = text;
}

if (!passkeysSupported()) {
  say('This browser can\'t use passkeys. Open the link in a current Safari, Chrome, Edge or Firefox.', true);
} else if (!token) {
  say('This link is missing its code. Ask for a new one.', true);
} else {
  btn.disabled = false;
}

form.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  btn.disabled = true;
  say('Waiting for your device…');
  try {
    await join(token, username.value.trim(), label.value.trim());
    location.replace('/account.html');
  } catch (e) {
    if (e.status === 400 && /invite/.test(e.message)) {
      say(e.message + ' Ask for a new one.', true);
      return; // nothing to retry with this link
    }
    // 400 (a name that isn't allowed, a passkey that didn't verify), 409 (name
    // taken) and 429 all say what to change; the browser's own errors are
    // translated.
    say(e.status ? e.message : friendlyError(e), true);
    btn.disabled = false;
  }
});
