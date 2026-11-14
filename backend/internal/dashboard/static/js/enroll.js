import { initTheme } from './theme.js';
import { enroll, passkeysSupported, friendlyError, deviceLabel } from './webauthn.js';

initTheme();

// The link is /enroll#<token>. The token lives in the fragment so it never
// reaches a server log or a Referer header.
const token = location.hash.slice(1);
const form = document.getElementById('form');
const btn = document.getElementById('goBtn');
const msg = document.getElementById('msg');
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
    await enroll(token, label.value.trim());
    location.replace('/account.html');
  } catch (e) {
    say(e.status === 400 ? e.message + ' Ask for a new one.' : e.status === 429 ? e.message : friendlyError(e), true);
    btn.disabled = e.status === 400;
  }
});
