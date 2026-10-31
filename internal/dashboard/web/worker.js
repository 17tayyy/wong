// The worker that opens a sealed brain in a browser whose service workers do not run, as on iPhone browsers other
// than Safari: the page asks it for each page and renders what it answers, and the work stays off the page's thread.
try {
  importScripts('/static/web/wasm_exec.js', '/static/web/core.js');
} catch (err) {
  self.postMessage({ problem: 'The worker could not load its scripts: ' + err.message });
  throw err;
}

const ops = { unlock, status, forget, serve };

self.onstage = (s) => self.postMessage({ stage: s });
self.onstage('loaded');

self.onmessage = async ({ data: { id, op, args } }) => {
  try {
    const result = await ops[op](...(args || []));
    if (result && result.body instanceof Uint8Array) {
      self.postMessage({ id, result }, [result.body.buffer]);
    } else {
      self.postMessage({ id, result });
    }
  } catch (err) {
    self.postMessage({ id, error: err.message });
  }
};
