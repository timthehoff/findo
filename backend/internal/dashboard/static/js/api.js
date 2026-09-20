// Thin fetch wrapper for the findo-server JSON API: throws on a non-2xx
// response with the server's {"error": "..."} message, so callers can just
// try/catch instead of checking res.ok everywhere.

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
    throw new Error((data && data.error) || res.statusText);
  }
  return data;
}

export const getJSON = (url) => request('GET', url);
export const postJSON = (url, body) => request('POST', url, body);
export const putJSON = (url, body) => request('PUT', url, body);
export const deleteJSON = (url) => request('DELETE', url);
