// The browser check: it tries, one piece at a time, what opening a sealed brain needs, shows each result, and sends
// them to the server's log, so a browser that cannot open the notes can be fixed. Nothing of the notes is involved.
(async () => {
  const list = document.getElementById('results');
  const results = [];
  const add = (step, ok, detail) => {
    results.push(`${ok ? 'ok' : 'FAIL'} ${step}${detail ? ' (' + detail + ')' : ''}`);
    const li = document.createElement('li');
    li.textContent = `${ok ? '✓' : '✗'} ${step}${detail ? ' — ' + detail : ''}`;
    li.className = ok ? 'ok' : 'bad';
    list.append(li);
  };
  const send = () => fetch('/browser/report', {
    method: 'POST',
    body: new URLSearchParams({ problem: results.join('; '), step: 'check', mode: 'check' }),
  }).catch(() => {});

  add('has service workers', 'serviceWorker' in navigator, navigator.serviceWorker && navigator.serviceWorker.controller ? 'one controls this page' : '');
  add('has workers', typeof Worker !== 'undefined', '');
  add('has WebAssembly', typeof WebAssembly !== 'undefined', typeof WebAssembly !== 'undefined' && WebAssembly.compileStreaming ? 'with streaming' : '');

  const probe = (url, label) => new Promise((resolve) => {
    let worker;
    const timer = setTimeout(() => {
      add(label + ': no answer', false, 'after 10 s');
      resolve(null);
    }, 10000);
    try {
      worker = new Worker(url);
    } catch (err) {
      clearTimeout(timer);
      add(label + ': cannot be made', false, err.message);
      resolve(null);
      return;
    }
    worker.onerror = (e) => {
      clearTimeout(timer);
      add(label + ': error', false, (e.message || 'no message') + (e.filename ? ` at ${e.filename.replace(location.origin, '')}:${e.lineno}` : ''));
      resolve(null);
    };
    worker.onmessage = ({ data }) => {
      if (data.step === 'worker starts') {
        clearTimeout(timer);
        add(label, true, 'answers');
        resolve(worker);
      }
    };
  });
  const run = (worker, what, ms) => new Promise((resolve) => {
    const timer = setTimeout(() => {
      add(what + ': stopped', false, `no answer after ${ms / 1000} s`);
      resolve();
    }, ms);
    worker.onmessage = ({ data }) => {
      if (data.done === what) {
        clearTimeout(timer);
        resolve();
      } else if (data.step) {
        add(data.step, data.ok, data.detail);
      }
    };
    worker.postMessage(what);
  });

  const plain = await probe('/static/web/probe.js?policy=none', 'a worker without a policy');
  const worker = await probe('/static/web/probe.js', 'a worker with the dashboard’s policy');
  const use = worker || plain;
  if (use) {
    await run(use, 'scripts', 20000);
    await run(use, 'tiny', 10000);
    await run(use, 'big', 180000);
  }
  add('finished', true, '');
  send();
})();
