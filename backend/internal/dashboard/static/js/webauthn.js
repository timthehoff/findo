// Passkey ceremonies in the browser. The server's options and the browser's
// responses are the standard JSON forms (base64url strings), which the
// browser converts with parse…OptionsFromJSON and toJSON, so there is no
// hand-rolled ArrayBuffer handling here.
//
// Each ceremony is two calls: …/begin returns {challengeId, options}, and
// …/finish?challenge=<id> takes the browser's response as the body.

import { postJSON } from './api.js';

export function passkeysSupported() {
  return !!(window.PublicKeyCredential
    && PublicKeyCredential.parseCreationOptionsFromJSON
    && PublicKeyCredential.parseRequestOptionsFromJSON);
}

const finish = (path, challengeId, credential) =>
  postJSON(`${path}?challenge=${encodeURIComponent(challengeId)}`, credential);

async function create(options) {
  const cred = await navigator.credentials.create({
    publicKey: PublicKeyCredential.parseCreationOptionsFromJSON(options.publicKey),
  });
  return cred.toJSON();
}

// signIn asks for any passkey Findo knows (no username needed).
export async function signIn() {
  const begin = await postJSON('/auth/passkey/login/begin');
  const cred = await navigator.credentials.get({
    publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(begin.options.publicKey),
  });
  return finish('/auth/passkey/login/finish', begin.challengeId, cred.toJSON());
}

// enroll registers a first (or replacement) passkey with an enrollment link's
// token, and signs the user in.
export async function enroll(token, label) {
  const begin = await postJSON('/auth/enroll/begin', { token, label });
  const cred = await create(begin.options);
  const done = await finish('/auth/enroll/finish', begin.challengeId, cred);
  return { ...done, username: begin.username };
}

// addPasskey registers another passkey for the signed-in user.
export async function addPasskey(label) {
  const begin = await postJSON('/auth/passkey/register/begin', { label });
  const cred = await create(begin.options);
  return finish('/auth/passkey/register/finish', begin.challengeId, cred);
}

// reauthenticate proves presence with one of the signed-in user's passkeys, so
// the server will accept a sensitive change for the next few minutes.
export async function reauthenticate() {
  const begin = await postJSON('/auth/reauth/begin');
  const cred = await navigator.credentials.get({
    publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(begin.options.publicKey),
  });
  return finish('/auth/reauth/finish', begin.challengeId, cred.toJSON());
}

// friendlyError turns what the browser and server throw into a sentence.
export function friendlyError(e) {
  if (e && e.name === 'NotAllowedError') return 'Cancelled, or it timed out. Try again.';
  if (e && e.name === 'InvalidStateError') return 'This device already has a passkey for your account.';
  if (e && e.name === 'SecurityError') return 'This page\'s address can\'t be used for passkeys. Passkeys need HTTPS (or localhost).';
  return (e && e.message) || 'Something went wrong.';
}

// deviceLabel suggests a name for the passkey being created.
export function deviceLabel() {
  const ua = navigator.userAgent;
  const os = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android'
    : /Mac/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows PC' : /Linux/.test(ua) ? 'Linux' : 'This device';
  const browser = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox'
    : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : '';
  return browser ? `${os} · ${browser}` : os;
}
