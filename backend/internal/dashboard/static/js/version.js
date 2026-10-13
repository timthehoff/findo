// Shows the running version in the nav and, when the server reports a newer
// GitHub release, an "update available" badge linking to its notes. Any
// failure just leaves the slot empty — this is a courtesy, not core UI.
import { getJSON } from './api.js';

export async function initVersion() {
  const slot = document.querySelector('[data-version]');
  if (!slot) return;

  try {
    const v = await getJSON('/version');
    slot.textContent = v.current;
    if (!v.updateAvailable) return;

    const badge = document.createElement(v.url ? 'a' : 'span');
    badge.className = 'update-badge';
    badge.textContent = `${v.latest} available`;
    badge.title = 'Re-run the install command to update';
    if (v.url) {
      badge.href = v.url;
      badge.target = '_blank';
      badge.rel = 'noopener';
    }
    slot.after(badge);
  } catch {
    // Offline or old server without /version: leave the nav as is.
  }
}
