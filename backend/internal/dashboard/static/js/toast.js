// A minimal toast notification stack, used instead of alert()/silent
// failures for action feedback (save, delete, reindex, test connection).

let container;

function ensureContainer() {
  if (!container) {
    container = document.createElement('div');
    container.className = 'toast-container';
    document.body.appendChild(container);
  }
  return container;
}

// toast shows a transient message. kind is 'info' | 'success' | 'error'.
export function toast(message, kind = 'info') {
  const el = document.createElement('div');
  el.className = `toast toast-${kind}`;
  el.textContent = message;
  ensureContainer().appendChild(el);

  requestAnimationFrame(() => el.classList.add('show'));

  setTimeout(() => {
    el.classList.remove('show');
    setTimeout(() => el.remove(), 200);
  }, 3500);
}
