// A worker that tells, one piece at a time, how far this browser gets running the dashboard, for /browser/check.
const say = (step, ok, detail) => self.postMessage({ step, ok, detail });
const ms = (since) => Math.round(performance.now() - since) + ' ms';
say('worker starts', true, '');

self.onmessage = async ({ data }) => {
  if (data === 'scripts') {
    for (const src of ['/static/web/wasm_exec.js', '/static/web/core.js']) {
      const t = performance.now();
      try {
        importScripts(src);
        say('loads ' + src.split('/').pop(), true, ms(t));
      } catch (err) {
        say('loads ' + src.split('/').pop(), false, err.message);
      }
    }
  }
  if (data === 'tiny') {
    try {
      await WebAssembly.instantiate(new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]));
      say('runs a tiny WebAssembly', true, '');
    } catch (err) {
      say('runs a tiny WebAssembly', false, err.message);
    }
  }
  if (data === 'big') {
    let step = 'downloads the dashboard';
    try {
      let t = performance.now();
      const res = await fetch('/static/web/wong.wasm');
      const bytes = await res.arrayBuffer();
      say(step, true, ms(t) + ', ' + (bytes.byteLength / 1e6).toFixed(1) + ' MB');
      step = 'compiles it';
      t = performance.now();
      const module = await WebAssembly.compile(bytes);
      say(step, true, ms(t));
      step = 'instantiates it';
      t = performance.now();
      const go = new Go();
      const instance = await WebAssembly.instantiate(module, go.importObject);
      say(step, true, ms(t));
      step = 'starts it';
      t = performance.now();
      go.run(instance);
      while (!self.wong) await new Promise((r) => setTimeout(r, 5));
      say(step, true, ms(t));
    } catch (err) {
      say(step, false, err.message);
    }
  }
  self.postMessage({ done: data });
};
