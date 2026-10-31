// What the browser dashboard's service worker and its worker share: they start wong, compiled to WebAssembly, keep
// this browser's token and device key, open the sealed brain with them, and answer the pages from it.

let runtime;
let stage = '';

// reached records how far starting got, for the page to say where it waits; a worker tells the page as it goes.
function reached(s) {
  stage = s;
  if (self.onstage) self.onstage(s);
}

// dashboard starts wong once, and opens the brain again with the key this browser kept.
function dashboard() {
  runtime ??= (async () => {
    reached('downloading');
    const res = await fetch('/static/web/wong.wasm');
    reached('compiling');
    const module = await WebAssembly.compileStreaming(res);
    reached('starting');
    const go = new Go();
    go.run(await WebAssembly.instantiate(module, go.importObject));
    while (!self.wong) await new Promise((r) => setTimeout(r, 5));
    reached('ready');
    const kept = await keep('readonly', (s) => s.get('self'));
    if (kept) self.wong.start(self.location.origin, kept.token, kept.device);
    return self.wong;
  })().catch((err) => {
    runtime = undefined;
    throw err;
  });
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

// status says how far this browser got, and the name the server knows it by.
async function status() {
  const w = await dashboard();
  const kept = await keep('readonly', (s) => s.get('self'));
  return { ...w.status(), name: kept && kept.name };
}

// unlock gets this browser a token and a device key the first time, and asks for the brain's key with them.
async function unlock() {
  const w = await dashboard();
  let kept = await keep('readonly', (s) => s.get('self'));
  reached('asking');
  if (!kept) {
    const res = await fetch('/browser/token', { method: 'POST' });
    if (!res.ok || !(res.headers.get('Content-Type') || '').startsWith('application/json')) {
      return { state: 'failed', problem: 'The server did not give this browser its place (it answered ' + res.status + '). Sign in again and retry.' };
    }
    const given = await res.json();
    kept = { token: given.token, name: given.name, device: w.newDevice() };
    await keep('readwrite', (s) => s.put(kept, 'self'));
  }
  w.start(self.location.origin, kept.token, kept.device);
  return { ...w.status(), name: kept.name };
}

// forget drops the brain and this browser's key, for a browser that signs out.
async function forget() {
  const w = await dashboard();
  w.forget();
  await keep('readwrite', (s) => s.delete('self'));
}

// serve answers a request for a page from the brain open in this browser, or returns null while it is not open.
async function serve(method, url, headers, body) {
  const w = await dashboard();
  for (let i = 0; i < 400 && ['starting', 'opening'].includes(w.status().state); i++) await new Promise((r) => setTimeout(r, 50));
  if (w.status().state !== 'open') return null;
  return w.serve(method, url, headers, body);
}
