// space-elevator service worker: makes the dashboard installable and
// caches only content-hashed static assets. Authenticated pages, app
// content, logs, API calls, and POSTs always pass through untouched.
const STATIC_CACHE = 'se-static-v2';

self.addEventListener('install', (e) => {
  e.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys
        .filter(k => k !== STATIC_CACHE)
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
  // Never cache navigations: even the public auth pages contain a CSRF
  // token, while authenticated pages contain operator and deployment data.
  // Everything else: no respondWith → default browser handling.
});

async function cacheFirst(cacheName, req) {
  const hit = await caches.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) {
    const c = await caches.open(cacheName);
    await c.put(req, res.clone());
  }
  return res;
}
