// Theme: "forest night" (dark) or "trail day" (light). "auto" follows the OS setting.
const KEY = 'workdesk.theme';

export function loadThemePref() {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'day' || v === 'night' ? v : 'auto';
  } catch {
    return 'auto';
  }
}

export function saveThemePref(v) {
  try {
    localStorage.setItem(KEY, v);
  } catch {
    // Private mode or blocked storage: the choice just won't persist.
  }
}

export function resolveTheme(pref) {
  if (pref !== 'auto') return pref;
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'day' : 'night';
}

export function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
}

export const nextPref = { auto: 'day', day: 'night', night: 'auto' };
