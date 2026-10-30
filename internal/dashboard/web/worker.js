// The worker that opens a sealed brain in a browser whose service workers do not run, as on iPhone browsers other
// than Safari: the page asks it for each page and renders what it answers, and the work stays off the page's thread.
importScripts('/static/web/wasm_exec.js', '/static/web/core.js');

const ops = { unlock, status, forget, serve };

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
