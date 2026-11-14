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
  }
}

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

  const res = await fetch(url, opts);
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
