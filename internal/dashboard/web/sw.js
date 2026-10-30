// The service worker that opens a sealed brain in this browser: it runs the wong dashboard on the notes it decrypts
// here with a key of its own, and lets everything that is the server's own through.
importScripts('/static/web/wasm_exec.js', '/static/web/core.js');

const server = /^\/(api|static|hooks|mcp|oauth|\.well-known|login|signin|account|device|webhooks|browser)(\/|$)|^\/(sw|worker)\.js$/;

const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const failed = (err) => json({ state: 'failed', problem: 'This browser could not start the dashboard: ' + err.message }, 500);

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname === '/__wong/ping') return e.respondWith(json({ ok: true }));
  if (url.pathname === '/__wong/unlock' && e.request.method === 'POST') return e.respondWith(unlock().then(json).catch(failed));
  if (url.pathname === '/__wong/status') return e.respondWith(status().then(json).catch(failed));
  if (url.pathname === '/logout' && e.request.method === 'POST') return e.respondWith(forget().catch(() => {}).then(() => fetch(e.request)));
  if (server.test(url.pathname)) return;
  e.respondWith(page(e.request));
});

async function page(request) {
  const body = ['GET', 'HEAD'].includes(request.method) ? null : new Uint8Array(await request.arrayBuffer());
  try {
    const res = await serve(request.method, request.url, [...request.headers], body);
    if (!res) return fetch(request);
    const empty = [101, 204, 205, 304].includes(res.status) || request.method === 'HEAD';
    return new Response(empty ? null : res.body, { status: res.status, headers: res.headers });
  } catch (err) {
    return new Response('This browser could not open the page: ' + err.message, { status: 500, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
  }
}
