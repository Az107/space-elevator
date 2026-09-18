// space-elevator UI behaviors: nav state, drop deploy, log streaming.

// CSRF token for fetch() POSTs (HTML forms carry the hidden input).
const csrfToken = (document.querySelector('meta[name="csrf-token"]') || {}).content || '';

// PWA: register the service worker (installable dashboard + offline
// shell). Secure contexts only; failures are non-fatal.
if ('serviceWorker' in navigator && location.protocol === 'https:') {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {});
  });
}

// forms with data-confirm open the styled <dialog> instead of the
// native confirm() (which a strict CSP can't theme). Without JS the
// form simply submits, same as before.
(function () {
  const dlg = document.getElementById('confirm-dialog');
  let pending = null;

  document.addEventListener('submit', e => {
    const f = e.target;
    if (!(f instanceof HTMLFormElement) || !f.dataset.confirm) return;
    if (f.dataset.confirmed === '1') { delete f.dataset.confirmed; return; }
    if (!dlg || typeof dlg.showModal !== 'function') return;
    e.preventDefault();
    pending = f;
    const title = document.getElementById('confirm-title');
    const msg = document.getElementById('confirm-message');
    const accept = document.getElementById('confirm-accept');
    const danger = f.dataset.confirmVariant === 'danger';
    const action = f.querySelector('button[type="submit"] .btn-label');
    if (title) title.textContent = f.dataset.confirmTitle || 'Are you sure?';
    if (msg) msg.textContent = f.dataset.confirm;
    if (accept) {
      accept.textContent = f.dataset.confirmLabel || (action ? action.textContent : 'Confirm');
      accept.classList.toggle('danger', danger);
    }
    dlg.classList.toggle('confirm-danger', danger);
    dlg.showModal();
  }, true);

  if (!dlg) return;
  const close = () => { pending = null; dlg.close(); };
  const cancel = document.getElementById('confirm-cancel');
  const accept = document.getElementById('confirm-accept');
  if (cancel) cancel.addEventListener('click', close);
  dlg.addEventListener('cancel', () => { pending = null; });
  dlg.addEventListener('click', e => { if (e.target === dlg) close(); });
  if (accept) accept.addEventListener('click', () => {
    const f = pending;
    pending = null;
    dlg.close();
    if (!f) return;
    f.dataset.confirmed = '1';
    if (f.requestSubmit) f.requestSubmit(); else f.submit();
  });
})();

// Show an in-flight spinner on the submit button of any form that
// actually submits (the confirm dialog's first, prevented pass is
// skipped so the button doesn't flash).
document.addEventListener('submit', e => {
  const f = e.target;
  if (!(f instanceof HTMLFormElement) || e.defaultPrevented) return;
  const btn = f.querySelector('button[type="submit"]');
  if (btn && !btn.disabled) btn.setAttribute('aria-busy', 'true');
});

// Flash messages (server-set after a redirect) float as toasts and
// auto-dismiss; the markup renders inline first so it still works
// without JS.
(function () {
  document.querySelectorAll('[data-flash]').forEach(el => {
    const dismiss = () => {
      el.classList.add('flash-out');
      setTimeout(() => el.remove(), 260);
    };
    el.classList.add('toast');
    const btn = el.querySelector('.flash-close');
    if (btn) btn.addEventListener('click', dismiss);
    setTimeout(dismiss, 6000);
  });
})();

// Deploy-from-git form: show the submit as in-flight while the POST
// round-trip runs (the server redirects to the app page, where the
// build panel picks up the live progress).
(function () {
  const f = document.querySelector('form[action="/apps/new"]');
  if (!f) return;
  const label = () => f.querySelector('button[type="submit"] .btn-label');
  f.addEventListener('submit', () => {
    const btn = f.querySelector('button[type="submit"]');
    if (btn) {
      btn.disabled = true;
      btn.setAttribute('aria-busy', 'true');
      const l = label();
      if (l) l.textContent = 'Deploying…';
    }
  });
  // Back/forward cache restores the page with the button still disabled.
  window.addEventListener('pageshow', () => {
    const btn = f.querySelector('button[type="submit"]');
    if (btn) {
      btn.disabled = false;
      btn.removeAttribute('aria-busy');
      const l = label();
      if (l) l.textContent = 'Deploy';
    }
  });
})();

// Copy-to-clipboard buttons: [data-copy] holds the payload.
(function () {
  const CHECK = '<svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="20 6 9 17 4 12"/></svg>';
  document.addEventListener('click', e => {
    const btn = e.target.closest('[data-copy]');
    if (!btn) return;
    const text = btn.getAttribute('data-copy') || '';
    const done = () => {
      if (btn.dataset.copied) return;
      btn.dataset.copied = '1';
      const prev = btn.innerHTML;
      btn.classList.add('copied');
      btn.innerHTML = CHECK;
      setTimeout(() => {
        btn.classList.remove('copied');
        btn.innerHTML = prev;
        delete btn.dataset.copied;
      }, 1400);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done).catch(() => fallbackCopy(text, done));
    } else {
      fallbackCopy(text, done);
    }
  });
  function fallbackCopy(text, done) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.className = 'clipboard-helper';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); done(); } catch (_) {}
    ta.remove();
  }
})();

// Client-side filter for the apps table.
(function () {
  const input = document.getElementById('apps-filter');
  const table = document.getElementById('apps-table');
  const noMatch = document.getElementById('apps-no-match');
  if (!input || !table) return;
  const rows = Array.from(table.querySelectorAll('tbody tr'));
  input.addEventListener('input', () => {
    const q = input.value.trim().toLowerCase();
    let visible = 0;
    rows.forEach(tr => {
      const match = !q || tr.textContent.toLowerCase().indexOf(q) !== -1;
      tr.hidden = !match;
      if (match) visible++;
    });
    if (noMatch) noMatch.hidden = !(q && visible === 0);
  });
})();

// Rename modal on the app detail page.
(function () {
  const dlg = document.getElementById('rename-dialog');
  if (!dlg) return;
  const open = document.querySelector('[data-rename-open]');
  const close = dlg.querySelector('[data-rename-close]');
  if (open) open.addEventListener('click', () => {
    dlg.showModal();
    const input = dlg.querySelector('input[name="name"]');
    if (input) { input.focus(); input.select(); }
  });
  if (close) close.addEventListener('click', () => dlg.close());
  dlg.addEventListener('click', e => { if (e.target === dlg) dlg.close(); });
})();

// Highlight the matching nav items (topnav + mobile bottom bar).
(function () {
  const p = location.pathname;
  let key = '';
  if (p.startsWith('/apps/new') || p === '/apps/drop') key = 'deploy';
  else if (p.startsWith('/apps')) key = 'apps';
  else if (p.startsWith('/settings')) key = 'settings';
  if (!key) return;
  document.querySelectorAll('[data-nav="' + key + '"]').forEach(a => a.classList.add('current'));
})();

// Drop zone for app deployments via tarball upload.
(function () {
  const dz = document.getElementById('dropzone');
  if (!dz) return;
  const status = document.getElementById('dropzone-status');
  const fileInput = document.getElementById('drop-input');
  let dragCounter = 0;

  dz.addEventListener('click', () => fileInput.click());
  dz.addEventListener('keydown', e => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileInput.click(); }
  });
  fileInput.addEventListener('change', e => upload(e.target.files[0]));

  ['dragenter', 'dragover'].forEach(ev => {
    document.body.addEventListener(ev, e => { e.preventDefault(); });
  });
  dz.addEventListener('dragenter', e => { e.preventDefault(); dragCounter++; dz.classList.add('over'); });
  dz.addEventListener('dragleave', e => { dragCounter--; if (!dragCounter) dz.classList.remove('over'); });
  dz.addEventListener('drop', e => {
    e.preventDefault();
    dragCounter = 0;
    dz.classList.remove('over');
    const f = e.dataTransfer.files[0];
    if (f) upload(f);
  });

  async function upload(file) {
    if (!file) {
      status.textContent = '✕ No file — drop a single .tar.gz or .zip, not a folder';
      status.classList.add('error');
      return;
    }
    status.classList.remove('error');
    status.textContent = 'Uploading ' + file.name + ' (' + Math.ceil(file.size / 1024) + ' KB)…';
    const fd = new FormData();
    fd.append('tarball', file);
    try {
      const res = await fetch('/apps/drop', {
        method: 'POST',
        body: fd,
        headers: csrfToken ? { 'X-CSRF-Token': csrfToken } : {}
      });
      let json = {};
      try { json = await res.json(); } catch (_) {}
      if (!res.ok) {
        status.textContent = '✕ ' + ((json && json.error) ? json.error : (res.status + ' ' + res.statusText));
        status.classList.add('error');
        return;
      }
      status.textContent = '✓ Deployed as ' + json.name + '. Opening it…';
      setTimeout(() => location.href = '/apps/' + json.name, 800);
    } catch (e) {
      status.textContent = '✕ ' + e.message;
      status.classList.add('error');
    }
  }
})();

// Unified deploy wizard: progressive disclosure on kind/source and a
// file drop target that fills the hidden tarball input.
(function () {
  const form = document.getElementById('deploy-wizard');
  if (!form) return;
  const checked = name => {
    const el = form.querySelector('input[name="' + name + '"]:checked');
    return el ? el.value : '';
  };
  function setEnabled(container, enabled) {
    container.hidden = !enabled;
    container.querySelectorAll('input, textarea, select').forEach(el => {
      if (el.type === 'radio' || el.type === 'hidden') return;
      el.disabled = !enabled;
    });
  }
  function sync() {
    const kind = checked('kind');
    const source = checked('source');
    form.querySelectorAll('[data-when-kind], [data-when-source]').forEach(el => {
      let ok = true;
      if (el.dataset.whenKind) {
        ok = ok && el.dataset.whenKind.split(' ').indexOf(kind) !== -1;
      }
      if (el.dataset.whenSource) {
        ok = ok && el.dataset.whenSource.split(' ').indexOf(source) !== -1;
      }
      setEnabled(el, ok);
    });
  }
  form.querySelectorAll('input[name="kind"], input[name="source"]').forEach(el => el.addEventListener('change', sync));
  sync();

  const dz = document.getElementById('wiz-dropzone');
  const input = document.getElementById('wiz-drop-input');
  const hint = document.getElementById('wiz-drop-hint');
  function show(file) {
    if (hint && file) hint.textContent = file.name + ' (' + Math.ceil(file.size / 1024) + ' KB)';
  }
  if (dz && input) {
    dz.addEventListener('click', () => input.click());
    dz.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); input.click(); }
    });
    input.addEventListener('change', () => show(input.files[0]));
    ['dragenter', 'dragover'].forEach(ev => dz.addEventListener(ev, e => { e.preventDefault(); dz.classList.add('over'); }));
    dz.addEventListener('dragleave', () => dz.classList.remove('over'));
    dz.addEventListener('drop', e => {
      e.preventDefault();
      dz.classList.remove('over');
      if (e.dataTransfer.files.length) {
        input.files = e.dataTransfer.files;
        show(input.files[0]);
      }
    });
  }
})();

// Logs panel: container logs (tail + SSE follow). While a deploy is
// in flight the same panel streams the live build feed instead, with a
// progress indicator; container logs take over once it settles.
(function () {
  const pre = document.getElementById('log-output');
  if (!pre) return;
  const app = pre.dataset.app;
  const panel = document.getElementById('logs-panel');
  const sel = document.getElementById('log-service');
  const followBtn = document.getElementById('log-follow');
  const progress = document.getElementById('deploy-progress');
  const stageEl = document.getElementById('deploy-stage');
  const steps = document.getElementById('deploy-steps');
  const lamp = document.querySelector('.app-title .lamp');
  const stageLabels = {
    clone: 'Cloning repo…',
    compose: 'Resolving compose…',
    deploy: 'Building & starting…',
    route: 'Publishing route…'
  };
  let es = null;
  let timer = null;
  let seq = panel ? (parseInt(panel.dataset.seq, 10) || 0) : 0;

  function autoscroll() { pre.scrollTop = pre.scrollHeight; }

  function setControls(enabled) {
    if (sel) sel.disabled = !enabled;
    if (followBtn) followBtn.disabled = !enabled;
  }

  function loadContainerLogs() {
    fetch('/apps/' + encodeURIComponent(app) + '/logs?tail=100')
      .then(r => r.text()).then(t => { pre.textContent = t; autoscroll(); });
  }

  function markStage(stage) {
    if (!steps) return;
    let seen = false;
    steps.querySelectorAll('li').forEach(li => {
      if (li.dataset.stage === stage) {
        li.classList.add('active');
        li.classList.remove('done');
        seen = true;
      } else {
        li.classList.toggle('done', !seen);
        li.classList.remove('active');
      }
    });
    if (stageEl) stageEl.textContent = stageLabels[stage] || 'Deploying…';
  }

  function finishDeploy(data) {
    if (timer) { clearTimeout(timer); timer = null; }
    if (progress) progress.hidden = true;
    if (panel) panel.classList.remove('deploying');
    setControls(true);
    const ok = data.status === 'running';
    if (!ok) {
      // Keep the build output for debugging; make sure the reason is visible.
      if (data.last_error && pre.textContent.indexOf(data.last_error) === -1) {
        pre.textContent += '✕ ' + data.last_error + '\n';
        autoscroll();
      }
      return;
    }
    loadContainerLogs();
  }

  function pollDeploy() {
    fetch('/apps/' + encodeURIComponent(app) + '/deploy-status?after=' + seq).then(res => {
      if (res.status === 404) return null; // app removed; stop quietly
      if (!res.ok) throw new Error('status ' + res.status);
      return res.json();
    }).then(data => {
      if (!data) return;
      (data.lines || []).forEach(line => {
        if (line.stage) markStage(line.stage);
        if (line.text) { pre.textContent += line.text + '\n'; autoscroll(); }
      });
      seq = data.seq || seq;
      if (data.status && data.status !== 'pending') { finishDeploy(data); return; }
      timer = setTimeout(pollDeploy, 1500);
    }).catch(() => {
      // Transient network/proxy hiccup — back off and retry.
      timer = setTimeout(pollDeploy, 4000);
    });
  }

  const deploying = panel && panel.dataset.deploying === 'true';

  // Follow toggle: SSE streaming of container logs.
  followBtn.addEventListener('click', () => {
    if (es) {
      es.close(); es = null;
      followBtn.textContent = 'Follow';
      followBtn.classList.remove('active');
      return;
    }
    pre.textContent = '';
    followBtn.classList.add('active');
    const svc = sel.value;
    const url = '/apps/' + encodeURIComponent(app) + '/logs?stream=1' + (svc ? '&service=' + svc : '');
    es = new EventSource(url);
    es.onmessage = e => {
      pre.textContent += e.data + '\n';
      autoscroll();
    };
    es.onerror = () => {
      es.close(); es = null;
      followBtn.textContent = 'Reconnect';
      followBtn.classList.remove('active');
    };
    followBtn.textContent = 'Stop';
  });

  if (deploying) {
    // The build feed takes over the panel until the deploy settles.
    setControls(false);
    pollDeploy();
  } else {
    loadContainerLogs();
  }
})();
