// Runs before the first paint (a classic script in <head>, allowed by `script-src 'self'`): puts the person's theme and accent on <html>
// from the copy kept in localStorage, so the page does not flash the default colour on every load. src/lib/appearance.ts does the same,
// from the account's preferences, once they have loaded; appearance.test.ts checks that both choose the same text colour.
(function () {
  try {
    var root = document.documentElement;
    var theme = localStorage.getItem('nasa.theme');
    if (theme === 'light' || theme === 'dark') root.setAttribute('data-theme', theme);
    var accent = localStorage.getItem('nasa.accent');
    if (!/^#[0-9a-f]{6}$/i.test(accent || '')) return;           // anything else: the default, never a broken page
    function lum(i) { var v = parseInt(accent.slice(i, i + 2), 16) / 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }
    var l = 0.2126 * lum(1) + 0.7152 * lum(3) + 0.0722 * lum(5);
    var light = 1.05 / (l + 0.05), dark = (l + 0.05) / (0.00713 + 0.05);   // contrast with #ffffff and with #14131f (luminance 0.00713)
    root.style.setProperty('--accent', accent);
    root.style.setProperty('--on-accent', light >= dark ? '#ffffff' : '#14131f');
    var meta = document.querySelector('meta[name=theme-color]');
    if (meta) meta.setAttribute('content', accent);
  } catch (e) { /* storage blocked: the defaults */ }
})();
