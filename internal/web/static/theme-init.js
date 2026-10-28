'use strict';
// Sets data-theme before first paint; a sync <head> file because the CSP forbids inline scripts.
// Default is light (not the OS setting); legacy key "iperf-theme" is migrated once.
(function () {
  var KEY = 'wedigo-theme';
  var saved = null;
  try {
    saved = localStorage.getItem(KEY);
    var old = localStorage.getItem('iperf-theme');
    if (old !== null) {
      if (saved === null && (old === 'light' || old === 'dark')) { saved = old; localStorage.setItem(KEY, old); }
      localStorage.removeItem('iperf-theme');
    }
  } catch (e) {}
  var root = document.documentElement;
  root.setAttribute('data-theme', saved === 'dark' ? 'dark' : 'light');
  window.wedigoTheme = {
    key: KEY,
    // saved reports whether the user picked a theme explicitly.
    saved: function () { try { return localStorage.getItem(KEY); } catch (e) { return null; } },
  };
})();
