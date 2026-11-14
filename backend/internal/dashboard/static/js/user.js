// The signed-in user in the top nav: their name (linking to the account page)
// and, for administrators, a Users link. Every page behind sign-in calls
// initUser(); it resolves to the user ({id, username, role}), or never settles
// into a page if the session is gone, since api.js is already redirecting.
import { getJSON } from './api.js';

export async function initUser() {
  let user;
  try {
    ({ user } = await getJSON('/auth/me'));
  } catch (e) {
    // Signed out: api.js is already sending the browser to the sign-in page,
    // so don't let the page carry on (or log an error) in the meantime.
    if (e.status === 401) return new Promise(() => {});
    throw e;
  }
  const before = document.querySelector('[data-theme-toggle]');
  const here = location.pathname;

  const link = (href, text, extra = {}) => {
    const a = document.createElement('a');
    a.href = href;
    a.textContent = text;
    if (here === href) a.className = 'active';
    Object.assign(a, extra);
    nav().insertBefore(a, before);
    return a;
  };
  if (user.role === 'admin') link('/users.html', 'Users');
  const me = link('/account.html', user.username);
  me.title = 'Your account';
  me.classList.add('user-link');
  return user;
}

const nav = () => document.querySelector('.topnav');
