// Explicit light/dark/system theme toggle, layered on top of the
// prefers-color-scheme media queries in css/base.css: 'system' removes the
// data-theme attribute (falls back to the OS setting), 'light'/'dark' pin
// it regardless of the OS setting.

import { lineIcon as svg } from './util.js';

const STORAGE_KEY = 'findo-theme';
const MODES = ['system', 'light', 'dark'];
const ICONS = {
  system: svg('<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/>'),
  light: svg('<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>'),
  dark: svg('<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>'),
};

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

function render(btn, mode) {
  btn.innerHTML = ICONS[mode];
  const label = `Theme: ${mode} (click to change)`;
  btn.title = label;
  btn.setAttribute('aria-label', label);
}

// initTheme applies the saved theme immediately and wires up every
// [data-theme-toggle] button on the page to cycle system -> light -> dark.
export function initTheme() {
  let mode = readMode();
  applyMode(mode);

  document.querySelectorAll('[data-theme-toggle]').forEach(btn => {
    render(btn, mode);
    btn.addEventListener('click', () => {
      mode = MODES[(MODES.indexOf(mode) + 1) % MODES.length];
      writeMode(mode);
      applyMode(mode);
      document.querySelectorAll('[data-theme-toggle]').forEach(b => render(b, mode));
    });
  });
}
