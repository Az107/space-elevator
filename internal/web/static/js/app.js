// Drop zone for app deployments via tarball upload.
(function () {
  const dz = document.getElementById('dropzone');
  if (!dz) return;
  const status = document.getElementById('dropzone-status');
  const fileInput = document.getElementById('drop-input');
  let dragCounter = 0;

  dz.addEventListener('click', () => fileInput.click());
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
      status.textContent = '✗ no file — drag a single .tar.gz or .zip, not a folder';
      return;
    }
    status.textContent = 'Uploading ' + file.name + ' (' + Math.ceil(file.size / 1024) + ' KB)…';
    const fd = new FormData();
    fd.append('tarball', file);
    try {
      const res = await fetch('/apps/drop', { method: 'POST', body: fd });
      let json = {};
      try { json = await res.json(); } catch (_) {}
      if (!res.ok) {
        const msg = (json && json.error) ? json.error : (res.status + ' ' + res.statusText);
        status.textContent = '✗ ' + msg;
        status.classList.add('error');
        return;
      }
      status.textContent = '✓ Deployed as ' + json.name + '. Redirecting…';
      status.classList.remove('error');
      setTimeout(() => location.href = '/apps/' + json.name, 800);
    } catch (e) {
      status.textContent = '✗ ' + e.message;
      status.classList.add('error');
    }
  }
})();

// Live log streaming via SSE.
(function () {
  const pre = document.getElementById('log-output');
  if (!pre) return;
  const app = pre.dataset.app;
  const sel = document.getElementById('log-service');
  const followBtn = document.getElementById('log-follow');
  let es = null;
  followBtn.addEventListener('click', () => {
    if (es) { es.close(); es = null; followBtn.textContent = 'Follow'; return; }
    pre.textContent = '';
    const svc = sel.value;
    const url = '/apps/' + encodeURIComponent(app) + '/logs?stream=1' + (svc ? '&service=' + svc : '');
    es = new EventSource(url);
    es.onmessage = e => {
      pre.textContent += e.data + '\n';
      pre.scrollTop = pre.scrollHeight;
    };
    es.onerror = () => { followBtn.textContent = 'Reconnect'; es.close(); es = null; };
    followBtn.textContent = 'Stop';
  });
  // initial fetch of last 100 lines
  fetch('/apps/' + encodeURIComponent(app) + '/logs?tail=100')
    .then(r => r.text()).then(t => pre.textContent = t);
})();