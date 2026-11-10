// @ts-check

/*
 * Version switcher injected into each versioned Redoc page.
 * Fetches ../versions.json and renders a floating dropdown so users can jump between versions without going back to the index page.
 */
(function () {
  const CURRENT_VERSION = (window.PIPER_DOCS_VERSION || '').trim();

  function getVersionsUrl() {
    // Each page sits at /<version>/index.html on gh-pages, so versions.json is one level up.
    const path = window.location.pathname;
    const idx = path.lastIndexOf('/');
    const parentPath = path.substring(0, idx).replace(/\/[^/]+$/, '/');
    return parentPath + 'versions.json';
  }

  function styles() {
    return `
      #piper-version-switcher {
        position: fixed;
        top: 0.85rem;
        right: 1rem;
        z-index: 9999;
        font: 13px -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
        background: #171717;
        color: #f2f2f2;
        border: 1px solid #404040;
        border-radius: 6px;
        box-shadow: 0 1px 2px rgba(0,0,0,0.4);
        padding: 4px 8px;
      }
      #piper-version-switcher label { color: #a3a3a3; margin-right: 6px; }
      #piper-version-switcher select {
        border: none;
        background: transparent;
        font: inherit;
        color: inherit;
        padding: 2px 4px;
        cursor: pointer;
      }
      #piper-version-switcher a {
        margin-left: 8px;
        color: #60a5fa;
        text-decoration: none;
      }
    `;
  }

  function render(versions) {
    const style = document.createElement('style');
    style.textContent = styles();
    document.head.appendChild(style);

    const container = document.createElement('div');
    container.id = 'piper-version-switcher';

    const label = document.createElement('label');
    label.textContent = 'version';
    label.setAttribute('for', 'piper-version-select');
    container.appendChild(label);

    const select = document.createElement('select');
    select.id = 'piper-version-select';
    versions.forEach(v => {
      const opt = document.createElement('option');
      opt.value = v;
      opt.textContent = v;
      if (v === CURRENT_VERSION) opt.selected = true;
      select.appendChild(opt);
    });
    select.addEventListener('change', function () {
      window.location.href = '../' + select.value + '/';
    });
    container.appendChild(select);

    const indexLink = document.createElement('a');
    indexLink.href = '../';
    indexLink.textContent = 'all versions';
    container.appendChild(indexLink);

    document.body.appendChild(container);
  }

  fetch(getVersionsUrl(), { cache: 'no-store' })
    .then(r => r.json())
    .then(render)
    .catch(() => { /* silent: docs are still readable without the switcher */ });
})();
