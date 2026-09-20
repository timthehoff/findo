// Explicit light/dark/system theme toggle, layered on top of the
// prefers-color-scheme media queries in css/base.css: 'system' removes the
// data-theme attribute (falls back to the OS setting), 'light'/'dark' pin
// it regardless of the OS setting.

const STORAGE_KEY = 'findo-theme';
const MODES = ['system', 'light', 'dark'];
const ICONS = { system: '🖥️', light: '☀️', dark: '🌙' };

function readMode() {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return MODES.includes(stored) ? stored : 'system';
  } catch {
    return 'system';
  }
}

function writeMode(mode) {
  try {
    localStorage.setItem(STORAGE_KEY, mode);
  } catch {
    // Private browsing / blocked storage — the toggle still works for
    // this page load, it just won't persist across reloads.
  }
}

function applyMode(mode) {
  if (mode === 'system') {
    document.documentElement.removeAttribute('data-theme');
  } else {
    document.documentElement.setAttribute('data-theme', mode);
  }
}

function labelFor(mode) {
  return `${ICONS[mode]} ${mode}`;
}

// initTheme applies the saved theme immediately and wires up every
// [data-theme-toggle] button on the page to cycle system -> light -> dark.
export function initTheme() {
  let mode = readMode();
  applyMode(mode);

  document.querySelectorAll('[data-theme-toggle]').forEach(btn => {
    btn.textContent = labelFor(mode);
    btn.title = 'Toggle light/dark theme (currently: ' + mode + ')';
    btn.addEventListener('click', () => {
      mode = MODES[(MODES.indexOf(mode) + 1) % MODES.length];
      writeMode(mode);
      applyMode(mode);
      document.querySelectorAll('[data-theme-toggle]').forEach(b => {
        b.textContent = labelFor(mode);
        b.title = 'Toggle light/dark theme (currently: ' + mode + ')';
      });
    });
  });
}
