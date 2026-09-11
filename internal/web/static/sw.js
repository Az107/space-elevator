// space-elevator service worker: makes the dashboard installable and
// keeps a cached shell for offline opens. Deliberately conservative —
// it only handles the dashboard's own navigations and content-hashed
// static assets. Everything else (hosted apps under /app/, log SSE,
// API calls, POSTs) passes through untouched.
const SHELL_CACHE = 'se-shell-v1';
const STATIC_CACHE = 'se-static-v1';

self.addEventListener('install', (e) => {
  e.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys
        .filter(k => k !== SHELL_CACHE && k !== STATIC_CACHE)
        .map(k => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== location.origin) return;

  // Content-hashed statics are immutable — cache-first.
  if (url.pathname.startsWith('/static/') && url.searchParams.has('v')) {
    e.respondWith(cacheFirst(STATIC_CACHE, req));
    return;
  }
  // Dashboard navigations — network-first, cached shell as offline fallback.
  if (req.mode === 'navigate' && isDashboardNav(url)) {
    e.respondWith(networkFirst(SHELL_CACHE, req));
    return;
  }
  // Everything else: no respondWith → default browser handling.
});

function isDashboardNav(url) {
  return url.pathname === '/' ||
    url.pathname.startsWith('/apps') ||
    url.pathname.startsWith('/settings') ||
    url.pathname.startsWith('/login') ||
    url.pathname.startsWith('/setup');
}

async function cacheFirst(cacheName, req) {
  const hit = await caches.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) {
    const c = await caches.open(cacheName);
    c.put(req, res.clone());
  }
  return res;
}

async function networkFirst(cacheName, req) {
  try {
    const res = await fetch(req);
    if (res.ok) {
      const c = await caches.open(cacheName);
      c.put(req, res.clone());
    }
    return res;
  } catch (err) {
    const hit = await caches.match(req);
    if (hit) return hit;
    throw err;
  }
}
