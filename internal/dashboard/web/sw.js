// The service worker that opens a sealed brain in this browser: it runs the wong dashboard, compiled to WebAssembly,
// on the notes it decrypts here with a key of its own, and lets everything that is the server's own through.
importScripts('/static/web/wasm_exec.js');

const server = /^\/(api|static|hooks|mcp|oauth|\.well-known|login|signin|account|device|webhooks|browser)(\/|$)|^\/sw\.js$/;

let runtime;

// dashboard starts wong once per worker, and opens the brain again with the key this browser kept.
function dashboard() {
  runtime ??= (async () => {
    const go = new Go();
    const { instance } = await WebAssembly.instantiateStreaming(fetch('/static/web/wong.wasm'), go.importObject);
    go.run(instance);
    while (!self.wong) await new Promise((r) => setTimeout(r, 5));
    const kept = await keep('readonly', (s) => s.get('self'));
    if (kept) self.wong.start(self.location.origin, kept.token, kept.device);
    return self.wong;
  })();
  return runtime;
}

// keep runs one request on the browser's own store, where it keeps its token and its device key.
function keep(mode, run) {
  return new Promise((resolve, reject) => {
    const open = indexedDB.open('wong', 1);
    open.onupgradeneeded = () => open.result.createObjectStore('browser');
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const req = run(open.result.transaction('browser', mode).objectStore('browser'));
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    };
  });
}

const failed = (err) => {
  runtime = undefined;
  return json({ state: 'failed', problem: 'This browser could not start the dashboard: ' + err.message }, 500);
};

const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname === '/__wong/unlock' && e.request.method === 'POST') return e.respondWith(unlock().catch(failed));
  if (url.pathname === '/__wong/status') return e.respondWith(status().catch(failed));
  if (url.pathname === '/logout' && e.request.method === 'POST') return e.respondWith(signOut(e.request));
  if (server.test(url.pathname)) return;
  e.respondWith(page(e.request));
});

// status says how far this browser got, and the name the server knows it by.
async function status() {
  const w = await dashboard();
  const kept = await keep('readonly', (s) => s.get('self'));
  return json({ ...w.status(), name: kept && kept.name });
}

// unlock gets this browser a token and a device key the first time, and asks for the brain's key with them.
async function unlock() {
  const w = await dashboard();
  let kept = await keep('readonly', (s) => s.get('self'));
  if (!kept) {
    const res = await fetch('/browser/token', { method: 'POST' });
    if (!res.ok || !(res.headers.get('Content-Type') || '').startsWith('application/json')) {
      return json({ state: 'failed', problem: 'The server did not give this browser a token. Sign in again and retry.' }, 502);
    }
    const given = await res.json();
    kept = { token: given.token, name: given.name, device: w.newDevice() };
    await keep('readwrite', (s) => s.put(kept, 'self'));
  }
  w.start(self.location.origin, kept.token, kept.device);
  return json({ ...w.status(), name: kept.name });
}

// signOut forgets the brain and this browser's key before the server ends the session.
async function signOut(request) {
  const w = await dashboard();
  w.forget();
  await keep('readwrite', (s) => s.delete('self'));
  return fetch(request);
}

async function page(request) {
  const w = await dashboard();
  for (let i = 0; i < 400 && ['starting', 'opening'].includes(w.status().state); i++) await new Promise((r) => setTimeout(r, 50));
  if (w.status().state !== 'open') return fetch(request);
  const body = ['GET', 'HEAD'].includes(request.method) ? null : new Uint8Array(await request.arrayBuffer());
  try {
    const res = await w.serve(request.method, request.url, [...request.headers], body);
    const empty = [101, 204, 205, 304].includes(res.status) || request.method === 'HEAD';
    return new Response(empty ? null : res.body, { status: res.status, headers: res.headers });
  } catch (err) {
    return new Response('This browser could not open the page: ' + err.message, { status: 500, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
  }
}
