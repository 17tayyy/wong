document.documentElement.classList.add('js');

(() => {
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
  let sprite = '/static/icons.svg';

  function el(tag, attrs = {}, ...children) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v !== false && v != null) node.setAttribute(k, v === true ? '' : v);
    }
    node.append(...children);
    return node;
  }

  function icon(name) {
    const ns = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('class', 'i');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS(ns, 'use');
    use.setAttribute('href', sprite + '#' + name);
    svg.append(use);
    return svg;
  }

  const encPath = (p) => p.split('/').map(encodeURIComponent).join('/');
  const notePath = (p) => '/notes/' + encPath(p);
  const shelfOf = (p) => (p.startsWith('projects/') ? p.split('/')[1] : p.split('/')[0]);

  // page moves to another page, or shows this one again; the browser dashboard renders it in place when it runs here.
  const page = {
    to: (url) => location.assign(url),
    again: () => location.reload(),
  };

  async function post(url, data = {}) {
    const body = new URLSearchParams(data).toString();
    const res = await fetch(url, {
      method: 'POST',
      credentials: 'same-origin',
      keepalive: body.length < 60000,
      headers: { Accept: 'application/json', 'Content-Type': 'application/x-www-form-urlencoded' },
      body,
    });
    let out = null;
    try {
      out = await res.json();
    } catch {}
    if (!res.ok || !out) {
      const err = new Error(
        out && out.error ? out.error : res.redirected ? 'Your session ended. Sign in again in another tab, then retry.' : 'The server could not do that (' + res.status + '). Nothing changed.',
      );
      err.status = res.status;
      throw err;
    }
    return out;
  }

  const unsaved = new Set();
  addEventListener('beforeunload', (e) => {
    if (unsaved.size) {
      e.preventDefault();
      e.returnValue = '';
    }
  });

  const store = {
    take(key) {
      try {
        const v = JSON.parse(sessionStorage.getItem(key));
        sessionStorage.removeItem(key);
        return v;
      } catch {
        return null;
      }
    },
    put(key, v) {
      try {
        sessionStorage.setItem(key, JSON.stringify(v));
      } catch {}
    },
  };

  function announce(text) {
    const live = $('#live');
    if (live) live.textContent = text;
  }

  let statusTimer;
  function say(text, keep, bad) {
    const status = $('.saved');
    if (!status) return;
    status.textContent = text;
    status.classList.toggle('on', !!text);
    status.classList.toggle('bad', !!bad);
    clearTimeout(statusTimer);
    if (text && !keep) statusTimer = setTimeout(() => status.classList.remove('on'), 2200);
  }

  function snack(node, ms, lazy) {
    let timer;
    const hide = () => {
      node.classList.add('out');
      setTimeout(() => node.remove(), 200);
    };
    const arm = () => {
      clearTimeout(timer);
      timer = setTimeout(hide, ms);
    };
    node.addEventListener('pointerenter', () => clearTimeout(timer));
    node.addEventListener('pointerleave', arm);
    node.addEventListener('focusin', () => clearTimeout(timer));
    node.querySelector('.snack-close')?.addEventListener('click', (e) => {
      e.preventDefault();
      hide();
    });
    if (!lazy) return arm();
    const first = () => {
      removeEventListener('pointermove', first);
      removeEventListener('keydown', first);
      arm();
    };
    addEventListener('pointermove', first);
    addEventListener('keydown', first);
  }

  function showError(message, action) {
    $('.snack.error')?.remove();
    const node = el('div', { class: 'snack error', role: 'alert' }, icon('triangle-alert'), el('span', {}, message));
    if (action) {
      const button = el('button', { type: 'button', class: 'snack-undo' }, action.label);
      button.addEventListener('click', action.run);
      node.append(button);
    }
    node.append(el('a', { class: 'snack-close', href: '#', 'aria-label': 'Dismiss' }, icon('x')));
    document.body.append(node);
    snack(node, action ? 30000 : 12000);
  }

  function after(kind, res, path) {
    let url = new URL(location.href);
    if (path && decodeURIComponent(location.pathname) === '/notes/' + path) {
      url = new URL(res.url || '/', location.origin);
    }
    url.hash = '';
    url.searchParams.delete('op');
    url.searchParams.set('done', kind);
    if (res.op) url.searchParams.set('op', res.op);
    store.put('wong:place', {
      at: url.pathname,
      y: scrollY,
      tree: $('#side .tree')?.scrollTop || 0,
      open: $$('#side .tree details[open] > summary').map((s) => s.dataset.shelf),
    });
    page.to(url);
  }

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = el('textarea', { class: 'sr', 'aria-hidden': 'true' });
      ta.value = text;
      document.body.append(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    say('Copied ' + text);
  }

  let drawerFrom = null;
  function openDrawer(search, from) {
    const side = $('#side');
    drawerFrom = from;
    document.body.classList.add('drawer');
    side.setAttribute('role', 'dialog');
    side.setAttribute('aria-modal', 'true');
    $$('.frame, .tabbar').forEach((n) => (n.inert = true));
    void side.offsetWidth;
    (search ? $('#q') : $('.drawer-close', side))?.focus();
  }
  function closeDrawer() {
    if (!document.body.classList.contains('drawer')) return;
    document.body.classList.remove('drawer');
    const side = $('#side');
    side.removeAttribute('role');
    side.removeAttribute('aria-modal');
    $$('.frame, .tabbar').forEach((n) => (n.inert = false));
    drawerFrom?.focus({ preventScroll: true });
    drawerFrom = null;
  }

  let menu = null;
  let menuFrom = null;
  function closeMenu(refocus = true) {
    if (!menu) return;
    menu.remove();
    menu = null;
    if (refocus && menuFrom && document.contains(menuFrom)) menuFrom.focus({ preventScroll: true });
    menuFrom = null;
  }

  function openMenu(items, x, y, from, alignEnd) {
    closeMenu(false);
    menuFrom = from;
    menu = el('div', { class: 'menu', role: 'menu', 'aria-label': from.dataset.name || from.dataset.shelf || 'Actions' });
    for (const item of items) {
      if (item === '-') {
        menu.append(el('div', { class: 'menu-sep', role: 'separator' }));
        continue;
      }
      const button = el('button', { type: 'button', role: 'menuitem', class: item.danger ? 'danger' : false, tabindex: '-1' }, icon(item.icon), el('span', {}, item.label));
      if (item.hint) button.append(el('span', { class: 'hint' }, item.hint));
      button.addEventListener('click', () => {
        closeMenu(false);
        item.run();
      });
      menu.append(button);
    }
    document.body.append(menu);
    const r = menu.getBoundingClientRect();
    const left = alignEnd ? x - r.width : x;
    menu.style.left = Math.max(8, Math.min(left, innerWidth - r.width - 8)) + 'px';
    menu.style.top = Math.max(8, Math.min(y, innerHeight - r.height - 8)) + 'px';
    menu.querySelector('button').focus();
  }

  function noteItems(t) {
    const path = t.dataset.note;
    const name = t.dataset.name;
    return [
      { icon: 'external-link', label: 'Open in new tab', run: () => open(notePath(path), '_blank', 'noopener') },
      { icon: 'pencil-line', label: 'Rename', run: () => rename(t) },
      { icon: 'folder-input', label: 'Move to…', run: () => moveDialog(t) },
      { icon: 'link', label: 'Copy link', hint: '[[' + name + ']]', run: () => copy('[[' + name + ']]') },
      t.dataset.archived !== undefined
        ? { icon: 'archive-restore', label: 'Bring back', run: () => archive(path, true) }
        : { icon: 'archive', label: 'Archive', run: () => archive(path, false) },
      '-',
      { icon: 'trash-2', label: 'Delete…', danger: true, run: () => deleteDialog(t) },
    ];
  }

  function shelfItems(t) {
    const id = t.dataset.shelf;
    return [
      { icon: 'plus', label: 'New note here', run: () => page.to('/new?in=' + encodeURIComponent(id)) },
      { icon: 'book-open', label: 'Show in the library', run: () => page.to('/#' + encodeURIComponent(id)) },
    ];
  }

  const itemsFor = (t) => (t.dataset.note ? noteItems(t) : shelfItems(t));

  let press = null;
  function cancelPress() {
    if (press && !press.fired) {
      clearTimeout(press.timer);
      press = null;
    }
  }

  function dialog({ title, text, extra, confirm, danger, onConfirm }) {
    const d = el('dialog', { class: 'dialog', 'aria-labelledby': 'dialog-title' });
    const form = el('form', { method: 'dialog' }, el('h2', { id: 'dialog-title' }, title));
    for (const t of [].concat(text)) if (t) form.append(el('p', {}, t));
    if (extra) form.append(extra);
    const err = el('p', { class: 'dialog-error', role: 'alert', hidden: true });
    const cancel = el('button', { type: 'button', class: 'btn' }, 'Cancel');
    const ok = el('button', { type: 'submit', class: 'btn ' + (danger ? 'danger' : 'primary') }, confirm);
    form.append(err, el('div', { class: 'dialog-bar' }, cancel, ok));
    d.append(form);
    document.body.append(d);
    cancel.addEventListener('click', () => d.close());
    d.addEventListener('click', (e) => e.target === d && d.close());
    d.addEventListener('close', () => d.remove());
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      ok.disabled = true;
      try {
        await onConfirm(form);
      } catch (x) {
        err.textContent = x.message;
        err.hidden = false;
        ok.disabled = false;
      }
    });
    d.showModal();
    (danger ? cancel : $('input', form) || ok).focus();
  }

  function moveDialog(t) {
    const path = t.dataset.note;
    const here = shelfOf(path);
    const shelves = [...new Set(['me', ...$$('#side summary[data-shelf]').map((s) => s.dataset.shelf)])].filter((id) => id !== here);
    const list = el('div', { class: 'choices', role: 'radiogroup', 'aria-label': 'Shelves' });
    shelves.forEach((id, i) => {
      list.append(el('label', { class: 'choice' }, el('input', { type: 'radio', name: 'to', value: id, checked: i === 0 }), icon('folder'), el('span', {}, id)));
    });
    dialog({
      title: 'Move “' + t.dataset.name + '”',
      text: 'The note takes the scope of the shelf you pick, and its line moves to that shelf’s MEMORY.md.',
      extra: list,
      confirm: 'Move',
      onConfirm: async (form) => after('moved', await post('/move/' + encPath(path), { to: new FormData(form).get('to') }), path),
    });
  }

  async function archive(path, back) {
    try {
      after(back ? 'unarchived' : 'archived', await post((back ? '/unarchive/' : '/archive/') + encPath(path)), path);
    } catch (x) {
      showError(x.message);
    }
  }

  function deleteDialog(t) {
    const path = t.dataset.note;
    const links = Number(t.dataset.links) || 0;
    dialog({
      title: 'Delete “' + t.dataset.name + '”?',
      text: [
        'The note and its line in MEMORY.md go away, and each machine drops them on its next pull. Both stay in the history, and this can be undone.',
        links === 1 ? 'One note links here; that link will lead nowhere.' : links > 1 ? links + ' notes link here; those links will lead nowhere.' : '',
      ],
      confirm: 'Delete',
      danger: true,
      onConfirm: async () => after('deleted', await post('/delete/' + encPath(path)), path),
    });
  }

  function rename(t) {
    const path = t.dataset.note;
    const article = $('article[data-path]');
    const title = $('h1[data-field="name"]');
    if (title && article.dataset.path === path) {
      startField(title, true);
      return;
    }
    const label = t.querySelector('[data-label]');
    if (!label) return;
    const input = el('input', { class: 'rename', 'aria-label': 'New name', spellcheck: 'false' });
    input.value = t.dataset.name;
    label.replaceWith(input);
    input.focus();
    input.select();
    input.scrollLeft = 0;
    let done = false;
    const finish = async (save) => {
      if (done) return;
      done = true;
      const name = input.value.trim();
      if (!save || !name || name === t.dataset.name) {
        input.replaceWith(label);
        return;
      }
      input.disabled = true;
      try {
        after('renamed', await post('/rename/' + encPath(path), { name }), path);
      } catch (x) {
        input.replaceWith(label);
        showError(x.message);
      }
    };
    input.addEventListener('keydown', (e) => {
      e.stopPropagation();
      if (e.key === 'Enter') {
        e.preventDefault();
        finish(true);
      } else if (e.key === 'Escape') {
        e.preventDefault();
        finish(false);
      }
    });
    input.addEventListener('click', (e) => e.preventDefault());
    input.addEventListener('blur', () => finish(true));
  }

  function startField(f, selectAll) {
    f.focus();
    if (selectAll) {
      const range = document.createRange();
      range.selectNodeContents(f);
      getSelection().removeAllRanges();
      getSelection().addRange(range);
    }
  }

  function editableField(f) {
    try {
      f.contentEditable = 'plaintext-only';
    } catch {
      f.contentEditable = 'true';
    }
    const isName = f.dataset.field === 'name';
    f.spellcheck = !isName;
    f.setAttribute('role', 'textbox');
    f.setAttribute('aria-label', isName ? 'Name of the note' : 'Description of the note');
    let saved = f.textContent;
    f.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        f.blur();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        f.textContent = saved;
        f.blur();
      }
    });
    f.addEventListener('paste', (e) => {
      e.preventDefault();
      document.execCommand('insertText', false, e.clipboardData.getData('text/plain').replace(/\s+/g, ' '));
    });
    f.addEventListener('blur', async () => {
      const value = f.textContent.replace(/\s+/g, ' ').trim();
      if (!value || value === saved.trim()) {
        f.textContent = saved;
        f.classList.remove('failed');
        unsaved.delete(f);
        return;
      }
      const article = f.closest('article');
      try {
        if (isName) {
          say('Renaming…', true);
          const res = await post('/rename/' + encPath(article.dataset.path), { name: value });
          unsaved.delete(f);
          after('renamed', res, article.dataset.path);
          return;
        }
        say('Saving…', true);
        const res = await post('/description/' + encPath(article.dataset.path), { description: value, base: article.dataset.sha });
        article.dataset.sha = res.sha;
        saved = f.textContent = value;
        f.classList.remove('failed');
        unsaved.delete(f);
        say('Saved');
      } catch (x) {
        f.classList.add('failed');
        unsaved.add(f);
        say('Not saved', true, true);
        showError(x.message);
      }
    });
  }

  function size(ta) {
    ta.style.height = 'auto';
    ta.style.height = ta.scrollHeight + 'px';
  }

  // Where in a block's markdown a click on its rendered text lands, matching the text just before the click.
  function sourceOffset(blk, x, y) {
    const src = blk.dataset.md;
    let node = null;
    let offset = 0;
    if (document.caretPositionFromPoint) {
      const p = document.caretPositionFromPoint(x, y);
      if (p) [node, offset] = [p.offsetNode, p.offset];
    } else if (document.caretRangeFromPoint) {
      const r = document.caretRangeFromPoint(x, y);
      if (r) [node, offset] = [r.startContainer, r.startOffset];
    }
    if (!node || !blk.contains(node)) return src.length;
    const range = document.createRange();
    range.selectNodeContents(blk);
    range.setEnd(node, offset);
    const text = range.toString();
    if (!text) return 0;
    const guess = Math.round((text.length / Math.max(1, blk.textContent.length)) * src.length);
    for (let n = Math.min(24, text.length); n >= 3; n = Math.floor(n / 2)) {
      const tail = text.slice(-n);
      let best = -1;
      for (let i = src.indexOf(tail); i >= 0; i = src.indexOf(tail, i + 1)) {
        if (best < 0 || Math.abs(i - guess) < Math.abs(best - guess)) best = i;
      }
      if (best >= 0) return best + tail.length;
    }
    return Math.min(guess, src.length);
  }

  function kindOf(src) {
    if (/^(```|~~~)/.test(src)) return 'code';
    const m = /^(#{1,3})\s[^\n]*$/.exec(src);
    return m ? 'h' + m[1].length : '';
  }

  // The body of a note, edited a block at a time: click one, write, and leave it to save.
  function blocks(prose) {
    const article = prose.closest('article');
    let editor = null;
    let saving = Promise.resolve({ at: -1, delta: 0 });
    const keep = (ed) => {
      store.put('wong:draft', { path: article.dataset.path, src: ed.src, text: ed.ta.value });
      unsaved.clear();
      page.again();
    };

    function finish() {
      if (!editor) return saving;
      const ed = editor;
      editor = null;
      const text = ed.ta.value;
      if (text === ed.src) {
        unsaved.delete(ed.ta);
        ed.ta.remove();
        if (ed.blk) ed.blk.hidden = false;
        else if (!$('.blk', prose)) prose.append(el('p', { class: 'blk-empty' }, 'Nothing written yet.'));
        return (saving = Promise.resolve({ at: ed.index, delta: 0 }));
      }
      const count = $$('.blk', prose).length;
      ed.ta.readOnly = true;
      ed.ta.classList.add('busy');
      say('Saving…', true);
      saving = post('/blocks/' + encPath(article.dataset.path), { base: article.dataset.sha, from: ed.from, to: ed.to, text })
        .then((res) => {
          unsaved.delete(ed.ta);
          article.dataset.sha = res.sha;
          prose.innerHTML = res.html;
          say('Saved');
          return { at: ed.index, delta: $$('.blk', prose).length - count };
        })
        .catch((x) => {
          ed.ta.readOnly = false;
          ed.ta.classList.remove('busy');
          ed.ta.classList.add('failed');
          editor = ed;
          unsaved.add(ed.ta);
          say('Not saved', true, true);
          showError(x.message, x.status === 409 ? { label: 'Reload and keep my text', run: () => keep(ed) } : null);
          return null;
        });
      return saving;
    }

    function start(index, caret, shift) {
      if (!shift || editor) return;
      if (shift.at >= 0 && index > shift.at) index += shift.delta;
      const blk = index >= 0 ? $$('.blk', prose)[index] : null;
      const src = blk ? blk.dataset.md : '';
      const ta = el('textarea', { class: ('blk-edit ' + kindOf(src)).trim(), rows: '1', 'aria-label': 'Edit this part of the note. Leave it to save.' });
      ta.value = src;
      if (blk) {
        blk.hidden = true;
        blk.after(ta);
      } else {
        $('.blk-empty', prose)?.remove();
        prose.append(ta);
      }
      editor = { ta, blk, index: blk ? index : -1, from: blk ? blk.dataset.from : -1, to: blk ? blk.dataset.to : -1, src };
      size(ta);
      ta.focus({ preventScroll: true });
      const at = caret == null ? src.length : Math.min(caret, src.length);
      ta.setSelectionRange(at, at);
      ta.addEventListener('input', () => size(ta));
      ta.addEventListener('blur', () => finish());
      ta.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' || ((e.metaKey || e.ctrlKey) && (e.key === 'Enter' || e.key === 's'))) {
          e.preventDefault();
          ta.blur();
        }
      });
    }

    prose.addEventListener('click', (e) => {
      if (e.target.closest('a, textarea, button') || !getSelection().isCollapsed) return;
      const all = $$('.blk', prose);
      let blk = e.target.closest('.blk');
      if (!blk && all.length && e.clientY <= all.at(-1).getBoundingClientRect().bottom) {
        blk = all.reduce((a, b) => {
          const d = (n) => Math.abs((n.getBoundingClientRect().top + n.getBoundingClientRect().bottom) / 2 - e.clientY);
          return d(b) < d(a) ? b : a;
        });
      }
      const index = blk ? all.indexOf(blk) : -1;
      const caret = blk && blk.contains(e.target) ? sourceOffset(blk, e.clientX, e.clientY) : null;
      finish().then((shift) => start(index, caret, shift));
    });

    document.addEventListener('visibilitychange', () => document.visibilityState === 'hidden' && finish());

    const draft = store.take('wong:draft');
    if (draft && draft.path === article.dataset.path && draft.src != null) {
      start($$('.blk', prose).findIndex((b) => b.dataset.md === draft.src), null, { at: -1, delta: 0 });
      editor.ta.value = draft.text;
      size(editor.ta);
      unsaved.add(editor.ta);
      say('Your text is back; leave the block to save it', true);
      announce('Your text is back in the editor. Leave it to save it.');
    }
  }

  function source(form) {
    const ta = $('textarea', form);
    const base = $('[name="base"]', form);
    const article = form.closest('article');
    let saved = ta.value;
    const save = async () => {
      const text = ta.value;
      if (text === saved) return;
      say('Saving…', true);
      try {
        const res = await post(form.action, { base: base.value, content: text });
        base.value = article.dataset.sha = res.sha;
        saved = text;
        unsaved.delete(ta);
        say('Saved');
      } catch (x) {
        unsaved.add(ta);
        say('Not saved', true, true);
        showError(
          x.message,
          x.status === 409
            ? {
                label: 'Reload and keep my text',
                run: () => {
                  store.put('wong:draft', { path: article.dataset.path, text: ta.value });
                  unsaved.clear();
                  page.again();
                },
              }
            : null,
        );
      }
    };
    const draft = store.take('wong:draft');
    if (draft && draft.path === article.dataset.path && draft.src == null) {
      ta.value = draft.text;
      unsaved.add(ta);
      say('Your text is back; leave the editor to save it', true);
      announce('Your text is back in the editor. Leave it to save it.');
    }
    size(ta);
    ta.addEventListener('input', () => {
      size(ta);
      if (ta.value !== saved) unsaved.add(ta);
    });
    ta.addEventListener('blur', save);
    ta.addEventListener('keydown', (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 's') {
        e.preventDefault();
        save();
      }
    });
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      save();
    });
    document.addEventListener('visibilitychange', () => document.visibilityState === 'hidden' && save());
  }

  // ready prepares what a page edits in place, once it loads and again whenever the browser dashboard renders another.
  const ready = () => {
    const use = $('svg.i use');
    if (use) sprite = use.getAttribute('href').split('#')[0];

    $$('[data-field]').forEach(editableField);
    $$('.prose[data-blocks]').forEach(blocks);
    $$('form.source').forEach(source);

    const s = $('.snack');
    if (s) {
      const close = $('.snack-close', s);
      if (close) history.replaceState(null, '', close.getAttribute('href'));
      setTimeout(() => announce($('span', s).textContent + ($('.snack-undo', s) ? ' Press Control or Command and Z to undo.' : '')), 300);
      snack(s, 10000, true);
    }
    const place = store.take('wong:place');
    if (place) {
      for (const id of place.open || []) {
        const summary = $$('#side .tree summary').find((x) => x.dataset.shelf === id);
        if (summary) summary.parentElement.open = true;
      }
      const tree = $('#side .tree');
      if (tree) tree.scrollTop = place.tree;
      if (place.at === location.pathname) scrollTo(0, place.y);
    }
    if (location.hash === '#side') history.replaceState(null, '', location.pathname + location.search);
  };
  document.addEventListener('DOMContentLoaded', ready);

  document.addEventListener('click', (e) => {
    if (press && press.fired) {
      e.preventDefault();
      e.stopPropagation();
      press = null;
      return;
    }
    const opener = e.target.closest('a[href="#side"]');
    if (opener) {
      e.preventDefault();
      openDrawer(opener.hasAttribute('data-search'), opener);
      return;
    }
    if (e.target.closest('.scrim, .drawer-close')) {
      e.preventDefault();
      closeDrawer();
      return;
    }
    const more = e.target.closest('[data-menu]');
    if (more) {
      const r = more.getBoundingClientRect();
      openMenu(itemsFor(more), r.right, r.bottom + 4, more, true);
    }
  }, true);

  document.addEventListener('contextmenu', (e) => {
    const t = e.target.closest('[data-note], [data-shelf]');
    if (!t || e.target.closest('input, textarea, [contenteditable="true"], [contenteditable="plaintext-only"]')) return;
    e.preventDefault();
    if (press) {
      clearTimeout(press.timer);
      press = null;
    }
    let { clientX: x, clientY: y } = e;
    if (!x && !y) {
      const r = t.getBoundingClientRect();
      [x, y] = [r.left + 24, r.bottom];
    }
    openMenu(itemsFor(t), x, y, t);
  });

  document.addEventListener('pointerdown', (e) => {
    if (press && press.fired) press = null;
    if (menu && !menu.contains(e.target)) closeMenu(false);
    if (e.pointerType !== 'touch') return;
    const t = e.target.closest('[data-note], [data-shelf]');
    if (!t || e.target.closest('input, textarea')) return;
    const { clientX: x, clientY: y } = e;
    press = { x, y, fired: false };
    press.timer = setTimeout(() => {
      press.fired = true;
      openMenu(itemsFor(t), x, y, t);
    }, 500);
  });
  document.addEventListener('pointermove', (e) => {
    if (press && !press.fired && Math.hypot(e.clientX - press.x, e.clientY - press.y) > 10) cancelPress();
  });
  document.addEventListener('pointerup', cancelPress);
  document.addEventListener('pointercancel', cancelPress);

  document.addEventListener('keydown', (e) => {
    if (menu) {
      const items = $$('[role="menuitem"]', menu);
      const i = items.indexOf(document.activeElement);
      const go = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: items.length - 1 }[e.key];
      if (go !== undefined) {
        e.preventDefault();
        items[(go + items.length) % items.length].focus();
      } else if (e.key === 'Escape' || e.key === 'Tab') {
        e.preventDefault();
        closeMenu();
      }
      return;
    }
    if (e.key === 'Escape' && document.body.classList.contains('drawer')) closeDrawer();
    const undo = $('.snack .snack-undo')?.closest('form');
    if (undo && (e.metaKey || e.ctrlKey) && !e.shiftKey && (e.key || '').toLowerCase() === 'z' && !e.target.closest('input, textarea, [contenteditable]')) {
      e.preventDefault();
      undo.requestSubmit();
    }
  });
  addEventListener('resize', () => {
    closeMenu(false);
    if (innerWidth > 860) closeDrawer();
  });
  addEventListener('scroll', () => closeMenu(false), true);

  // Opening a sealed brain here. Where service workers run, one keeps a key for this browser, asks a machine that has
  // the brain's key to approve it, and then serves the dashboard on the notes it decrypts here. Where they do not, as
  // on iPhone browsers other than Safari, a worker does the same, and this page asks it for each page and renders it
  // in place. A screen over the app follows each step, and the app stays out of reach until it ends or is cancelled.
  const serverPath = /^\/(api|static|hooks|mcp|oauth|\.well-known|login|signin|account|device|webhooks|browser|logout|__wong)(\/|$)/;
  const ours = (url) => url.origin === location.origin && !serverPath.test(url.pathname);
  const within = (promise, ms, problem) =>
    Promise.race([promise, new Promise((_, no) => setTimeout(() => no(new Error(typeof problem === 'function' ? problem() : problem)), ms))]);
  const stages = {
    downloading: 'Getting the dashboard',
    compiling: 'Preparing the dashboard to run',
    starting: 'Starting the dashboard',
    ready: 'Making the key',
    asking: 'Asking the server for this browser’s place',
  };
  const asJSON = async (res) => {
    if (!(res.headers.get('Content-Type') || '').startsWith('application/json')) {
      throw new Error(`What keeps this browser's key did not answer (the page got ${res.status} from the server).`);
    }
    return res.json();
  };
  const remember = {
    get: () => { try { return localStorage.getItem('wong:browser'); } catch { return null; } },
    set: (v) => { try { localStorage.setItem('wong:browser', v); } catch {} },
  };
  const hasKey = () => new Promise((resolve) => {
    try {
      const open = indexedDB.open('wong', 1);
      open.onupgradeneeded = () => open.result.createObjectStore('browser');
      open.onerror = () => resolve(false);
      open.onsuccess = () => {
        const req = open.result.transaction('browser', 'readonly').objectStore('browser').getKey('self');
        req.onsuccess = () => resolve(req.result !== undefined);
        req.onerror = () => resolve(false);
      };
    } catch {
      resolve(false);
    }
  });

  const viaServiceWorker = {
    unlock: async () => asJSON(await fetch('/__wong/unlock', { method: 'POST' })),
    status: async () => asJSON(await fetch('/__wong/status')),
    stage: async () => (await asJSON(await fetch('/__wong/stage'))).stage,
    open: () => location.reload(),
  };

  const viaWorker = {
    worker: null,
    calls: new Map(),
    next: 0,
    last: '',
    call(op, ...args) {
      if (!this.worker) {
        this.worker = new Worker('/static/web/worker.js');
        this.worker.onmessage = ({ data }) => {
          if (data.stage) {
            this.last = data.stage;
            return;
          }
          const c = this.calls.get(data.id);
          this.calls.delete(data.id);
          if (c) data.error ? c.no(new Error(data.error)) : c.ok(data.result);
        };
        this.worker.onerror = (e) => {
          for (const c of this.calls.values()) c.no(new Error(e.message || 'The worker that opens your notes stopped.'));
          this.calls.clear();
        };
      }
      return new Promise((ok, no) => {
        const id = ++this.next;
        this.calls.set(id, { ok, no });
        this.worker.postMessage({ id, op, args });
      });
    },
    unlock() { return this.call('unlock'); },
    status() { return this.call('status'); },
    stage() { return Promise.resolve(this.last); },
    open() { routeHere(); },
  };

  // request asks the worker for a page, following redirects as a browser would; null means the page belongs to the
  // server, and the browser goes there.
  async function request(method, url, headers, body) {
    for (let hops = 0; hops < 6; hops++) {
      const res = await viaWorker.call('serve', method, url.href, headers, body);
      if (!res) {
        location.assign(url.href);
        return null;
      }
      const to = (res.headers.find(([k]) => k.toLowerCase() === 'location') || [])[1];
      if (!to || ![301, 302, 303, 307, 308].includes(res.status)) return { ...res, url };
      const next = new URL(to, url);
      if (!ours(next)) {
        location.assign(next.href);
        return null;
      }
      if (res.status === 303 || method === 'POST') {
        method = 'GET';
        body = null;
      }
      url = next;
    }
    throw new Error('The page kept sending this browser elsewhere.');
  }

  // route renders a page the worker answers in place of this one.
  async function route(href, push, method = 'GET', body = null, type = '') {
    const headers = [['Accept', 'text/html'], ['Origin', location.origin]];
    if (type) headers.push(['Content-Type', type]);
    let res;
    try {
      res = await request(method, new URL(href, location.href), headers, body);
    } catch {
      location.assign(href);
      return;
    }
    if (!res) return;
    const doc = new DOMParser().parseFromString(new TextDecoder().decode(res.body), 'text/html');
    document.title = doc.title;
    document.body.replaceWith(document.adoptNode(doc.body));
    for (const script of doc.querySelectorAll('script[src]')) {
      if (script.getAttribute('src').startsWith('/static/app.js')) continue;
      document.body.append(el('script', { src: script.getAttribute('src') }));
    }
    const at = res.url.pathname + res.url.search + res.url.hash;
    if (push) history.pushState(null, '', at);
    else history.replaceState(null, '', at);
    const target = res.url.hash && document.getElementById(decodeURIComponent(res.url.hash.slice(1)));
    if (target) target.scrollIntoView();
    else scrollTo(0, 0);
    ready();
  }

  // routeHere hands this page's links, forms and requests to the worker, and shows the page it is on.
  let routing = false;
  function routeHere() {
    if (!routing) {
      routing = true;
      page.to = (url) => route(url, true);
      page.again = () => route(location.href, false);
      document.addEventListener('click', (e) => {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        const a = e.target.closest('a[href]');
        if (!a || a.target || a.hasAttribute('download')) return;
        const url = new URL(a.href, location.href);
        if (!ours(url) || (url.pathname === location.pathname && url.search === location.search && url.hash)) return;
        e.preventDefault();
        route(url.href, true);
      });
      document.addEventListener('submit', async (e) => {
        if (e.defaultPrevented) return;
        const form = e.target;
        const url = new URL(form.getAttribute('action') || location.href, location.href);
        if (url.pathname === '/logout') {
          e.preventDefault();
          await viaWorker.call('forget').catch(() => {});
          form.submit();
          return;
        }
        if (!ours(url)) return;
        e.preventDefault();
        const data = new FormData(form);
        if (e.submitter && e.submitter.name) data.append(e.submitter.name, e.submitter.value);
        const params = new URLSearchParams(data).toString();
        if ((form.getAttribute('method') || 'GET').toUpperCase() === 'GET') {
          url.search = params;
          route(url.href, true);
        } else {
          route(url.href, true, 'POST', new TextEncoder().encode(params), 'application/x-www-form-urlencoded');
        }
      });
      addEventListener('popstate', () => route(location.href, false));
      const fetchFromServer = window.fetch.bind(window);
      window.fetch = async (input, init) => {
        const req = new Request(input, init);
        const url = new URL(req.url);
        if (!ours(url)) return fetchFromServer(input, init);
        const body = ['GET', 'HEAD'].includes(req.method) ? null : new Uint8Array(await req.arrayBuffer());
        const res = await request(req.method, url, [...req.headers, ['Origin', location.origin]], body);
        if (!res) return new Response(null, { status: 204 });
        return new Response([101, 204, 205, 304].includes(res.status) ? null : res.body, { status: res.status, headers: res.headers });
      };
    }
    return route(location.href, false);
  }

  const openHere = () => {
    const unlock = $('[data-unlock]');
    const overlay = $('[data-unlock-overlay]');
    if (!unlock || !overlay) return;
    if (!('serviceWorker' in navigator) && typeof Worker === 'undefined') {
      $('[data-unlock-start]', unlock).disabled = true;
      $('[data-unlock-unsupported]', unlock).hidden = false;
      return;
    }
    const app = $('.app');
    const order = ['get', 'key', 'approve', 'open'];
    const step = (name) => $(`[data-step="${name}"]`, overlay);
    const error = $('[data-unlock-error]', overlay);
    const retry = $('[data-unlock-retry]', overlay);
    const mb = (n) => (n / 1e6).toFixed(1) + ' MB';
    let run = 0;
    let driver = null;

    const at = (name, sub) => {
      const i = order.indexOf(name);
      order.forEach((n, j) => { step(n).dataset.state = j < i ? 'done' : j === i ? 'active' : 'pending'; });
      if (sub != null) $('[data-sub]', step(name)).textContent = sub;
    };
    const fail = (text) => {
      run++;
      const active = $('[data-state="active"]', overlay);
      if (active) {
        active.dataset.state = 'failed';
        const sub = $('[data-sub]', active);
        if (sub) sub.textContent = 'Did not finish.';
      }
      $('p', error).textContent = text;
      error.hidden = retry.hidden = false;
    };
    const show = () => {
      document.body.append(overlay);
      overlay.hidden = false;
      app.inert = true;
      $('#unlock-title', overlay).focus();
    };
    const close = () => {
      run++;
      overlay.hidden = true;
      app.inert = false;
      $('[data-unlock-start]', unlock).focus();
    };
    const controlled = () => navigator.serviceWorker.controller
      ? Promise.resolve()
      : new Promise((r) => navigator.serviceWorker.addEventListener('controllerchange', r, { once: true }));

    // pick uses a service worker where one runs, and the worker otherwise, remembering it for the next time.
    const pick = async () => {
      if (remember.get() !== 'worker' && 'serviceWorker' in navigator) {
        try {
          await within(navigator.serviceWorker.register('/sw.js', { scope: '/' }), 8000, 'slow');
          await within(controlled(), 8000, 'slow');
          await within(fetch('/__wong/ping').then(asJSON), 8000, 'slow');
          return viaServiceWorker;
        } catch {
          remember.set('worker');
        }
      }
      if (typeof Worker === 'undefined') throw new Error('This browser cannot run what decrypts your notes. Open this page in another browser.');
      return viaWorker;
    };

    const download = async (mine) => {
      at('get', 'Starting the download…');
      const res = await fetch('/static/web/wong.wasm', { cache: 'no-cache' });
      if (!res.ok) throw new Error(`The server did not send the dashboard (it answered ${res.status}). Try again in a minute.`);
      const total = Number(res.headers.get('X-Wong-Bytes')) || 0;
      const bar = $('.bar', step('get'));
      bar.hidden = !total;
      const reader = res.body.getReader();
      for (let got = 0; ;) {
        const { done, value } = await reader.read();
        if (done || mine !== run) break;
        got += value.length;
        $('[data-sub]', step('get')).textContent = total ? `Downloading… ${Math.min(99, Math.floor((got / total) * 100))}%` : `Downloading… ${mb(got)}`;
        if (total) bar.firstElementChild.style.width = Math.min(100, (got / total) * 100) + '%';
      }
      $('[data-sub]', step('get')).textContent = 'Ready in this browser.';
    };

    const follow = (s, mine) => {
      if (mine !== run) return;
      if (s.name) {
        $('[data-unlock-name]', overlay).hidden = false;
        $('[data-unlock-name] strong', overlay).textContent = s.name;
        $('[data-name]', overlay).textContent = s.name;
      }
      switch (s.state) {
        case 'waiting':
          at('approve');
          $('[data-pair]', overlay).hidden = false;
          $('[data-code]', overlay).replaceChildren(...(s.code || '').split(' ').map((g) => el('span', {}, g)));
          break;
        case 'starting':
          at('key', 'It never leaves this browser.');
          break;
        case 'opening':
          at('open', 'Decrypting your notes…');
          break;
        case 'open':
          at('open', 'Opening…');
          driver.open();
          return;
        case 'failed':
          fail(s.problem || 'This browser could not open your notes.');
          return;
      }
      setTimeout(() => poll(mine), 2000);
    };
    const poll = async (mine) => {
      if (mine !== run) return;
      try {
        follow(await driver.status(), mine);
      } catch {
        setTimeout(() => poll(mine), 4000);
      }
    };

    const begin = async () => {
      const mine = ++run;
      error.hidden = retry.hidden = true;
      $('[data-pair]', overlay).hidden = true;
      show();
      try {
        await download(mine);
        if (mine !== run) return;
        at('key', 'Checking what this browser can run…');
        driver = await pick();
        if (mine !== run) return;
        const since = Date.now();
        let stage = '';
        const tick = setInterval(async () => {
          stage = (await driver.stage().catch(() => '')) || stage;
          if (mine === run && stage) at('key', `${stages[stage] || stage}… ${Math.round((Date.now() - since) / 1000)} s`);
        }, 1000);
        try {
          follow(await within(driver.unlock(), 90000, () =>
            `This browser took more than 90 seconds at “${stages[stage] || 'Starting the dashboard'}” (${driver === viaWorker ? 'worker' : 'service worker'}). Try again, and if it stops at the same step, tell us which.`), mine);
        } finally {
          clearInterval(tick);
        }
      } catch (err) {
        if (mine === run) fail(err.message);
      }
    };

    $('[data-unlock-start]', unlock).addEventListener('click', begin);
    retry.addEventListener('click', begin);
    $('[data-unlock-cancel]', overlay).addEventListener('click', close);
    overlay.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(); });

    // A page loaded midway picks the screen up where this browser is; one that already holds its key opens the notes.
    const resume = async () => {
      if (navigator.serviceWorker && navigator.serviceWorker.controller && remember.get() !== 'worker') {
        driver = viaServiceWorker;
      } else if (remember.get() === 'worker' && (await hasKey())) {
        driver = viaWorker;
      } else {
        return;
      }
      try {
        const mine = ++run;
        const s = driver === viaWorker ? await driver.unlock() : await driver.status();
        if (!['starting', 'waiting', 'opening', 'open'].includes(s.state)) return;
        show();
        $('[data-sub]', step('get')).textContent = 'Ready in this browser.';
        follow(s, mine);
      } catch {}
    };
    resume();
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', openHere);
  else openHere();
})();
