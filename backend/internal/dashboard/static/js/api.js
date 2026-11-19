// Thin fetch wrapper for the findo-server JSON API: throws on a non-2xx
// response with the server's {"error": "..."} message, so callers can just
// try/catch instead of checking res.ok everywhere.
//
// A 401 on any page but the sign-in and enrollment pages means the session
// ended (or never existed), so it sends the browser to the sign-in page and
// brings it back here afterwards.

export class ApiError extends Error {
  constructor(message, status, data) {
    super(message);
    this.status = status;
    this.data = data;
    // The server wants a recent passkey confirmation first (see confirm.js).
    this.reauthRequired = !!(data && data.reauthRequired);
  }
}

// Longer than the slowest legitimate call (a failed connection test is padded to 8 s).
const REQUEST_TIMEOUT_MS = 30000;

const OPEN_PAGES = ['/login.html', '/enroll'];

function signInAddress() {
  const back = location.pathname + location.search;
  return '/login.html' + (back === '/' ? '' : '?next=' + encodeURIComponent(back));
}

let redirecting = false;

async function request(method, url, body) {
  const opts = { method };
  if (body !== undefined) {
    opts.headers = { 'Content-Type': 'application/json' };
    opts.body = JSON.stringify(body);
  }

  // A request that never answers (a stalled connection) must end in an error
  // the person can see and retry, not a page that waits forever.
  opts.signal = AbortSignal.timeout(REQUEST_TIMEOUT_MS);
  let res;
  try {
    res = await fetch(url, opts);
  } catch (e) {
    if (e && e.name === 'TimeoutError') {
      throw new ApiError('The server did not answer in time. Try again.', 0, null);
    }
    throw e;
  }
  let data = null;
  try {
    data = await res.json();
  } catch {
    // No/invalid JSON body — fine for e.g. a 204 No Content.
  }

  if (!res.ok) {
    if (res.status === 401 && !OPEN_PAGES.includes(location.pathname) && !redirecting) {
      redirecting = true;
      location.replace(signInAddress());
    }
    throw new ApiError((data && data.error) || res.statusText, res.status, data);
  }
  return data;
}

export const getJSON = (url) => request('GET', url);
export const postJSON = (url, body) => request('POST', url, body);
export const putJSON = (url, body) => request('PUT', url, body);
export const deleteJSON = (url) => request('DELETE', url);
