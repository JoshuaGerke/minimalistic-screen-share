/* Host-Panel: steuert den lokalen Client über die kleine API.
 * Alles speichert sich selbst – es gibt keinen Speichern-Knopf. */
(() => {
  'use strict';

  const $ = (s) => document.querySelector(s);
  const $$ = (s) => Array.from(document.querySelectorAll(s));
  let cfg = {};
  let targets = { monitors: [], windows: [] };
  let st = {};
  let kind = 'primary';

  const store = {
    get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : v; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* egal */ } },
  };

  async function api(path, body) {
    const opts = body === undefined ? {} : { method: 'POST', body: JSON.stringify(body) };
    const res = await fetch(path, opts);
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || `Fehler ${res.status}`);
    return data;
  }

  let toastTimer = null;
  function toast(text) {
    let el = $('.toast');
    if (!el) {
      el = document.createElement('div');
      el.className = 'toast';
      document.body.appendChild(el);
    }
    el.textContent = text;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.remove(), 2600);
  }

  function showError(msg) {
    const el = $('#start-error');
    el.textContent = msg || '';
    el.classList.toggle('hidden', !msg);
  }

  /* ---------- Laden ---------- */

  async function load() {
    [cfg, targets] = await Promise.all([api('/api/config'), api('/api/targets')]);
    fillTargets();
    fillForm();
  }

  function fillTargets() {
    const fill = (sel, items, empty) => {
      const prev = sel.value;
      sel.innerHTML = '';
      if (items.length === 0) items = [{ value: '', text: empty }];
      for (const it of items) {
        const opt = document.createElement('option');
        opt.value = it.value;
        opt.textContent = it.text;
        sel.appendChild(opt);
      }
      if (prev && items.some((i) => i.value === prev)) sel.value = prev;
    };
    fill($('#window-select'), targets.windows.map((w) => ({
      value: w.title, text: w.w ? `${w.title}  (${w.w}×${w.h})` : w.title,
    })), 'Keine Fenster gefunden');
    fill($('#monitor-select'), targets.monitors.map((m) => ({
      value: String(m.index), text: `Monitor ${m.index + 1} – ${m.w}×${m.h}${m.x === 0 && m.y === 0 ? ' (Haupt)' : ''}`,
    })), 'Keine Monitore gefunden');
  }

  // Werte aus älteren Configs, die keine Option sind, trotzdem anzeigen.
  function setSelect(sel, value, label) {
    if (!Array.from(sel.options).some((o) => o.value === value)) {
      const opt = document.createElement('option');
      opt.value = value;
      opt.textContent = label;
      sel.appendChild(opt);
    }
    sel.value = value;
  }

  function fillForm() {
    setSelect($('#cfg-fps'), String(cfg.fps || 15), String(cfg.fps));
    setSelect($('#cfg-width'), String(cfg.max_width ?? 1600), `${cfg.max_width} px`);
    $('#cfg-quality').value = String(cfg.quality || 70);
    $('#quality-label').textContent = qualityText(cfg.quality || 70);
    $('#cfg-audio').checked = !!cfg.audio;
    $('#cfg-auto').checked = !!cfg.auto_accept;
    $('#cfg-cursor').checked = cfg.target ? !!cfg.target.cursor : true;
    $('#cfg-name').value = cfg.name || '';
    $('#cfg-room').value = cfg.room || '';
    $('#cfg-server').value = cfg.server || '';
    const reach = $(`input[name=reach][value="${cfg.reach || 'internet'}"]`);
    if (reach) reach.checked = true;
    $('#server-field').classList.toggle('hidden', cfg.reach !== 'server');

    const t = cfg.target || {};
    kind = store.get('mss-kind', '') || ({ window: 'window', screen: 'screen', monitor: 'monitor' }[t.kind] || 'primary');
    if (t.kind === 'monitor') $('#monitor-select').value = String(t.monitor ?? 0);
    if (t.kind === 'window' && t.window) $('#window-select').value = t.window;
    syncSources();
    markPreset();
  }

  function qualityText(q) {
    if (q <= 50) return `${q} – sparsam`;
    if (q <= 75) return `${q} – ausgewogen`;
    return `${q} – scharf`;
  }

  /* ---------- Quelle ---------- */

  function currentTarget() {
    const cursor = $('#cfg-cursor').checked;
    switch (kind) {
      case 'window': return { kind: 'window', window: $('#window-select').value, cursor };
      case 'monitor': return { kind: 'monitor', monitor: Number($('#monitor-select').value || 0), cursor };
      case 'screen': return { kind: 'screen', cursor };
      default: return { kind: 'primary', cursor };
    }
  }

  function syncSources() {
    for (const b of $$('.source')) {
      const on = b.dataset.kind === kind;
      b.classList.toggle('active', on);
      b.setAttribute('aria-checked', on ? 'true' : 'false');
    }
    $('#monitor-field').classList.toggle('hidden', kind !== 'monitor');
    $('#window-field').classList.toggle('hidden', kind !== 'window');
  }

  // Quelle wechseln – läuft schon ein Stream, wechselt das Bild sofort.
  async function applyTarget() {
    store.set('mss-kind', kind);
    try {
      await api('/api/target', currentTarget());
      if (st.streaming) toast('Quelle gewechselt');
    } catch (e) {
      showError(e.message);
    }
  }

  for (const b of $$('.source')) {
    b.addEventListener('click', async () => {
      kind = b.dataset.kind;
      syncSources();
      if (kind === 'window') await refreshTargets();
      applyTarget();
    });
  }
  $('#monitor-select').addEventListener('change', applyTarget);
  $('#window-select').addEventListener('change', applyTarget);

  async function refreshTargets() {
    targets = await api('/api/targets').catch(() => targets);
    fillTargets();
  }
  $('#window-refresh').addEventListener('click', refreshTargets);

  /* ---------- Einstellungen: speichern sich selbst ---------- */

  let saveTimer = null;
  function scheduleSave() {
    clearTimeout(saveTimer);
    saveTimer = setTimeout(save, 250);
  }

  async function save() {
    const body = {
      ...cfg,
      reach: ($('input[name=reach]:checked') || {}).value || 'internet',
      server: $('#cfg-server').value.trim(),
      room: $('#cfg-room').value.trim().toLowerCase(),
      name: $('#cfg-name').value.trim(),
      fps: Number($('#cfg-fps').value),
      quality: Number($('#cfg-quality').value),
      max_width: Number($('#cfg-width').value),
      audio: $('#cfg-audio').checked,
      auto_accept: $('#cfg-auto').checked,
      target: { ...(cfg.target || {}), cursor: $('#cfg-cursor').checked },
    };
    const hint = $('#save-hint');
    try {
      cfg = await api('/api/config', body);
      hint.textContent = st.streaming ? 'Gespeichert – gilt sofort im laufenden Stream.' : 'Gespeichert.';
      hint.className = 'link-note';
    } catch (e) {
      hint.textContent = e.message;
      hint.className = 'link-note warn';
    }
    markPreset();
  }

  for (const id of ['#cfg-fps', '#cfg-width', '#cfg-audio', '#cfg-auto', '#cfg-room', '#cfg-name', '#cfg-server']) {
    $(id).addEventListener('change', scheduleSave);
  }
  $('#cfg-quality').addEventListener('input', () => {
    $('#quality-label').textContent = qualityText(Number($('#cfg-quality').value));
    scheduleSave();
  });
  $('#cfg-cursor').addEventListener('change', () => { scheduleSave(); applyTarget(); });
  for (const r of $$('input[name=reach]')) {
    r.addEventListener('change', () => {
      $('#server-field').classList.toggle('hidden', r.value !== 'server' || !r.checked);
      if (r.value === 'server' && !$('#cfg-server').value.trim()) {
        $('#cfg-server').focus();
        return; // erst speichern, wenn eine Adresse drinsteht
      }
      scheduleSave();
    });
  }

  for (const p of $$('.preset')) {
    p.addEventListener('click', () => {
      $('#cfg-fps').value = p.dataset.fps;
      $('#cfg-quality').value = p.dataset.q;
      $('#cfg-width').value = p.dataset.w;
      $('#quality-label').textContent = qualityText(Number(p.dataset.q));
      save();
    });
  }

  function markPreset() {
    for (const p of $$('.preset')) {
      p.classList.toggle('active',
        Number(p.dataset.fps) === cfg.fps && Number(p.dataset.q) === cfg.quality && Number(p.dataset.w) === cfg.max_width);
    }
  }

  $('#btn-new-room').addEventListener('click', () => {
    if (!confirm('Neue Sitzungs-ID erzeugen? Bisherige Links funktionieren danach nicht mehr.')) return;
    const alphabet = 'abcdefghjkmnpqrstuvwxyz23456789';
    let code = (cfg.name || 'pc').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 20) || 'pc';
    code += '-';
    for (let i = 0; i < 4; i++) code += alphabet[Math.floor(Math.random() * alphabet.length)];
    $('#cfg-room').value = code;
    save();
  });

  $('#cfg-autostart').addEventListener('change', async (ev) => {
    try {
      await api('/api/autostart', { on: ev.target.checked });
      toast(ev.target.checked ? 'Startet ab jetzt beim Anmelden' : 'Autostart aus');
    } catch (e) {
      ev.target.checked = !ev.target.checked;
      toast(e.message);
    }
  });

  $('#btn-mixer').addEventListener('click', () => api('/api/openmixer', {}).catch(() => {}));

  /* ---------- Teilen ---------- */

  async function copyLink(silent) {
    if (!st.link_ready) return false;
    try {
      await navigator.clipboard.writeText(st.share_url);
      if (!silent) toast('Link kopiert – einfach im Chat einfügen');
      return true;
    } catch (e) {
      if (!silent) toast('Kopieren ging nicht – Link bitte markieren und mit Strg+C kopieren');
      return false;
    }
  }

  $('#btn-start').addEventListener('click', async () => {
    showError('');
    $('#btn-start').disabled = true;
    try {
      store.set('mss-kind', kind);
      await api('/api/start', currentTarget());
      await poll();
      if (st.link_ready) {
        await copyLink(true);
        toast('Läuft! Link ist kopiert – einfach im Chat einfügen.');
      } else {
        toast('Läuft! Der Link kommt gleich und wird automatisch kopiert.');
      }
    } catch (e) {
      showError(e.message);
    }
    $('#btn-start').disabled = false;
  });

  $('#btn-stop').addEventListener('click', async () => {
    await api('/api/stop', {}).catch(() => {});
    poll();
  });

  $('#btn-pause').addEventListener('click', async () => {
    const paused = !!(st.stream && st.stream.paused);
    await api('/api/pause', { on: !paused }).catch((e) => showError(e.message));
    poll();
  });

  $('#btn-copy').addEventListener('click', () => copyLink(false));
  $('#btn-open-viewer').addEventListener('click', () => { if (st.link_ready) window.open(st.share_url + '&self=1', '_blank'); });

  /* ---------- Status ---------- */

  async function poll() {
    try {
      st = await api('/api/status');
    } catch (e) {
      $('#status-label').textContent = 'App nicht erreichbar – läuft sie noch?';
      $('#status-pill').className = 'status-pill red';
      return;
    }
    const s = st.stream || {};
    const pending = s.pending || [];
    $('#version').textContent = 'v' + st.version;

    const [cls, label] = pill(st, s, pending);
    $('#status-pill').className = 'status-pill ' + cls;
    $('#status-label').textContent = label;
    document.title = (pending.length ? `(${pending.length}) Anfrage – ` : (s.state === 'live' && st.streaming ? '● ' : '')) + 'minimalistic screen share';

    renderRequests(pending);
    renderLink();
    renderLive(s);
    renderViewers(s);
    $('#cfg-autostart').checked = !!st.autostart;
    pollAudioSources();
    if (s.state === 'error' && s.error) showError(s.error);
    else if (st.streaming && s.error) showError(s.error);
  }

  function pill(view, s, pending) {
    if (pending.length === 1) return ['yellow', `${pending[0].name} möchte zuschauen`];
    if (pending.length > 1) return ['yellow', `${pending.length} möchten zuschauen`];
    if (!view.streaming) return ['', 'Bereit'];
    if (s.paused) return ['yellow', 'Pausiert'];
    switch (s.state) {
      case 'live': return ['green', `Live · ${s.viewers} ${s.viewers === 1 ? 'Zuschauer' : 'Zuschauer'}`];
      case 'paused': return ['green', 'Teilt – wartet auf Zuschauer'];
      case 'connecting': return ['yellow', 'Verbinde…'];
      case 'reconnecting': return ['yellow', 'Verbindung wird wiederhergestellt…'];
      case 'error': return ['red', 'Fehler'];
      default: return ['', 'Bereit'];
    }
  }

  let lastLinkReady = false;
  function renderLink() {
    const url = $('#share-url');
    url.textContent = st.link_ready ? st.share_url : (st.link_note || 'Wird vorbereitet…');
    url.classList.toggle('pending', !st.link_ready);
    url.title = st.link_ready ? st.share_url : '';
    $('#btn-copy').disabled = !st.link_ready;
    $('#btn-open-viewer').disabled = !st.link_ready || !st.streaming;
    const note = $('#link-note');
    if (st.link_error) {
      note.textContent = `${st.link_note} (${st.link_error})`;
      note.className = 'link-note warn';
    } else {
      note.textContent = st.link_ready ? `${st.link_note}. Sitzungs-ID: ${st.room}` : '';
      note.className = 'link-note';
    }
    // Kam der Link erst nach dem Start, landet er jetzt in der Zwischenablage.
    if (st.link_ready && !lastLinkReady && st.streaming) {
      copyLink(true).then((ok) => { if (ok) toast('Link ist da und kopiert – einfach im Chat einfügen'); });
    }
    lastLinkReady = st.link_ready;
  }

  function renderLive(s) {
    $('#go-row').classList.toggle('hidden', !!st.streaming);
    $('#live-row').classList.toggle('hidden', !st.streaming);
    if (!st.streaming) return;
    $('#btn-pause').textContent = s.paused ? 'Fortsetzen' : 'Pausieren';
    $('#btn-pause').className = s.paused ? 'btn success big' : 'btn big';
    const box = $('#live-stats');
    const fmt = (k) => (k >= 1000 ? `${(k / 1000).toFixed(1).replace('.', ',')} Mbit/s` : `${k} kbit/s`);
    const parts = [];
    if (s.paused) {
      parts.push('Zuschauer sehen „Pausiert“');
    } else if (s.state === 'paused') {
      parts.push('Spart Leistung, bis jemand zuschaut');
    } else if (s.state === 'live') {
      if (s.w) parts.push(`<b>${s.w}×${s.h}</b>`);
      parts.push(`<b>${s.fps ?? 0}</b> fps`);
      parts.push(`<b>${fmt(s.kbps ?? 0)}</b>`);
      if (s.rtt_ms) parts.push(`Ping <b>${s.rtt_ms} ms</b>`);
    }
    if (s.target) parts.push(escapeHTML(s.target));
    if (!s.audio) parts.push('ohne Ton');
    box.innerHTML = parts.map((p) => `<span>${p}</span>`).join('');
  }

  function renderViewers(s) {
    const box = $('#viewers');
    const names = (s.names || []).filter(Boolean);
    box.innerHTML = '';
    for (const n of names) {
      const c = document.createElement('span');
      c.className = 'chip';
      c.textContent = n;
      box.appendChild(c);
    }
  }

  function escapeHTML(t) {
    const d = document.createElement('div');
    d.textContent = t;
    return d.innerHTML;
  }

  /* ---------- Anfragen ---------- */

  let lastPendingKey = '';
  function renderRequests(pending) {
    $('#requests-card').classList.toggle('hidden', pending.length === 0);
    const key = pending.map((p) => p.id).join(',');
    if (key === lastPendingKey) return;
    lastPendingKey = key;
    const box = $('#requests');
    box.innerHTML = '';
    for (const p of pending) {
      const row = document.createElement('div');
      row.className = 'request-row';
      const name = document.createElement('span');
      name.className = 'request-name';
      name.textContent = p.name;
      const ok = document.createElement('button');
      ok.className = 'btn success';
      ok.type = 'button';
      ok.textContent = 'Reinlassen';
      ok.addEventListener('click', () => decide('/api/allow', p.id));
      const no = document.createElement('button');
      no.className = 'btn ghost';
      no.type = 'button';
      no.textContent = 'Ablehnen';
      no.addEventListener('click', () => decide('/api/deny', p.id));
      row.append(name, ok, no);
      box.appendChild(row);
    }
  }

  async function decide(url, id) {
    try {
      await api(url, { id });
    } catch (e) {
      showError(e.message);
    }
    poll();
  }

  /* ---------- Ton-Quellen ---------- */

  let audioPolled = 0;
  async function pollAudioSources() {
    const card = $('#audio-sources-card');
    card.classList.toggle('hidden', !st.streaming);
    if (!st.streaming || Date.now() - audioPolled < 3000) return;
    audioPolled = Date.now();
    const data = await api('/api/audiosessions').catch(() => null);
    if (data) renderAudioSources(data);
  }

  function renderAudioSources(data) {
    const box = $('#audio-sources');
    const muted = new Set((data.muted || []).map((n) => n.toLowerCase()));
    const hint = $('#audio-sources-hint');
    hint.textContent = data.error || 'Wird beim Beenden automatisch wieder laut gestellt. Soll ein Programm für dich hörbar bleiben, aber nicht im Stream landen: im Lautstärkemixer „Von Aufnahme ausschließen“.';
    hint.className = data.error ? 'link-note warn' : 'link-note';
    box.innerHTML = '';
    const sessions = data.sessions || [];
    if (sessions.length === 0) {
      const empty = document.createElement('p');
      empty.className = 'link-note';
      empty.textContent = 'Gerade spielt kein Programm Ton.';
      box.appendChild(empty);
      return;
    }
    for (const s of sessions) {
      const row = document.createElement('label');
      row.className = 'switch source-row';
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.checked = muted.has(s.name.toLowerCase());
      cb.addEventListener('change', async () => {
        await api('/api/audiosessions', { name: s.name, muted: cb.checked }).catch(() => {});
        audioPolled = 0;
      });
      const text = document.createElement('span');
      text.className = 'label';
      const b = document.createElement('b');
      b.textContent = s.label || s.name;
      text.appendChild(b);
      row.append(cb, text);
      box.appendChild(row);
    }
  }

  /* ---------- Start ---------- */

  $('#foot').textContent = navigator.userAgent.includes('Windows')
    ? 'Das Symbol sitzt im Infobereich (Windows 11: hinter dem Pfeil ^). Entfernen: Einstellungen → Apps → Minimalistic Screen Share.'
    : 'Linux: grim (Bild), libpulse (Ton) und wl-clipboard (Link kopieren) sollten installiert sein.';

  load().then(poll).catch((e) => showError(e.message));
  setInterval(poll, 1000);
})();
