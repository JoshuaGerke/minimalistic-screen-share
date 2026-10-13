/* Host-Panel: steuert den lokalen Client über die kleine API. */
(() => {
  'use strict';

  const $ = (s) => document.querySelector(s);
  let cfg = {};
  let targets = { monitors: [], windows: [] };

  /* ---------- laden & speichern ---------- */

  async function load() {
    const [c, t] = await Promise.all([
      fetch('/api/config').then((r) => r.json()),
      fetch('/api/targets').then((r) => r.json()),
    ]);
    cfg = c;
    targets = t;
    fillTargets();
    fillForm();
    showPlatformHint();
  }

  function fillForm() {
    $('#cfg-server').value = cfg.server || '';
    $('#cfg-room').value = cfg.room || '';
    $('#cfg-name').value = cfg.name || '';
    $('#cfg-fps').value = String(cfg.fps || 15);
    $('#cfg-quality').value = String(cfg.quality || 70);
    $('#cfg-width').value = String(cfg.max_width ?? 1600);
    $('#cfg-audio').checked = !!cfg.audio;
    $('#cfg-auto').checked = !!cfg.auto_accept;
    $('#cfg-cursor').checked = cfg.target ? !!cfg.target.cursor : true;

    const kindMap = { window: 'window', monitor: 'monitor', screen: 'screen', test: 'test', region: 'screen' };
    const value = kindMap[cfg.target && cfg.target.kind] || 'primary';
    const radio = document.querySelector(`input[name=target][value="${value}"]`);
    if (radio) radio.checked = true;
    if (cfg.target && cfg.target.kind === 'monitor') {
      $('#monitor-select').value = String(cfg.target.monitor ?? 0);
    }
    if (cfg.target && cfg.target.kind === 'window' && cfg.target.window) {
      $('#window-select').value = cfg.target.window;
    }
    syncFields();
  }

  function fillTargets() {
    const winSelect = $('#window-select');
    winSelect.innerHTML = '';
    if (targets.windows.length === 0) {
      const opt = document.createElement('option');
      opt.textContent = '(keine Fenster gefunden)';
      opt.value = '';
      winSelect.appendChild(opt);
    }
    targets.windows.forEach((w) => {
      const opt = document.createElement('option');
      opt.value = w.title;
      opt.textContent = w.w ? `${w.title} (${w.w}×${w.h})` : w.title;
      winSelect.appendChild(opt);
    });

    const monSelect = $('#monitor-select');
    monSelect.innerHTML = '';
    targets.monitors.forEach((m) => {
      const opt = document.createElement('option');
      opt.value = String(m.index);
      opt.textContent = `Monitor ${m.index + 1} (${m.w}×${m.h})`;
      monSelect.appendChild(opt);
    });
    if (targets.monitors.length === 0) {
      const opt = document.createElement('option');
      opt.textContent = '(keine Monitore gefunden)';
      opt.value = '0';
      monSelect.appendChild(opt);
    }
  }

  function showPlatformHint() {
    const hint = $('#platform-hint');
    if (targets.platform === 'linux') {
      hint.textContent = 'Linux/Wayland: grim + slurp für die Aufnahme, libpulse für den Ton (parec). Fensteraufnahme läuft über hyprctl (Hyprland), der Bereich über slurp.';
    } else {
      hint.textContent = 'Tipp: Das Symbol sitzt in der Taskleiste im Infobereich – bei Windows 11 hinter dem kleinen Pfeil.';
    }
  }

  async function save(silent) {
    const body = {
      server: $('#cfg-server').value.trim(),
      room: $('#cfg-room').value.trim().toLowerCase(),
      name: $('#cfg-name').value.trim(),
      fps: Number($('#cfg-fps').value),
      quality: Number($('#cfg-quality').value),
      max_width: Number($('#cfg-width').value),
      audio: $('#cfg-audio').checked,
      auto_accept: $('#cfg-auto').checked,
      target: currentTarget(),
    };
    const res = await fetch('/api/config', { method: 'POST', body: JSON.stringify(body) });
    const data = await res.json();
    if (!res.ok) {
      if (!silent) hint('#save-hint', data.error || 'Speichern fehlgeschlagen', true);
      return false;
    }
    cfg = data;
    if (!silent) hint('#save-hint', 'Gespeichert.', false);
    return true;
  }

  /* ---------- Ziel aus der Oberfläche bauen ---------- */

  function currentTarget() {
    const kind = document.querySelector('input[name=target]:checked').value;
    const cursor = $('#cfg-cursor').checked;
    switch (kind) {
      case 'window':
        return { kind: 'window', window: $('#window-select').value, cursor };
      case 'monitor':
        return { kind: 'monitor', monitor: Number($('#monitor-select').value), cursor };
      case 'screen':
        return { kind: 'screen', cursor };
      case 'test':
        return { kind: 'test' };
      default: {
        // Hauptbildschirm: der Monitor oben links, sonst der erste.
        const mons = targets.monitors;
        let index = 0;
        for (const m of mons) {
          if (m.x === 0 && m.y === 0) { index = m.index; break; }
        }
        return { kind: 'monitor', monitor: index, cursor };
      }
    }
  }

  function syncFields() {
    const kind = document.querySelector('input[name=target]:checked').value;
    $('#window-field').classList.toggle('hidden', kind !== 'window');
    $('#monitor-field').classList.toggle('hidden', kind !== 'monitor');
  }

  for (const radio of document.querySelectorAll('input[name=target]')) {
    radio.addEventListener('change', syncFields);
  }

  /* ---------- Statusecke ---------- */

  async function poll() {
    try {
      const st = await fetch('/api/status').then((r) => r.json());
      $('#version').textContent = 'v' + st.version;
      $('#share-url').value = st.share_url || '';

      const s = st.stream || {};
      const pill = $('#status-pill');
      pill.className = 'status-pill ' + pillClass(s.state, s.pending);
      $('#status-label').textContent = pillLabel(s);

      renderRequests(s.pending || []);

      $('#stat-viewers').textContent = viewerText(s);
      $('#stat-res').textContent = s.w ? `${s.w}×${s.h}` : '–';
      $('#stat-fps').textContent = s.state === 'live' || s.state === 'paused' ? `${s.fps ?? 0}` : '–';
      const kbps = s.kbps ?? 0;
      $('#stat-rate').textContent = kbps >= 1000
        ? `${(kbps / 1000).toFixed(1).replace('.', ',')} Mbit/s`
        : `${kbps} kbit/s`;
      $('#stat-ping').textContent = s.rtt_ms ? `${s.rtt_ms} ms` : '–';

      $('#btn-start').disabled = !!st.streaming;
      $('#btn-stop').disabled = !st.streaming;
      if (s.state === 'error' && s.error) {
        hint('#start-error', s.error, true);
      }
    } catch (e) {
      $('#status-label').textContent = 'Client nicht erreichbar';
      $('#status-pill').className = 'status-pill red';
    }
  }

  function pillClass(state, pending) {
    if (pending && pending.length > 0) return 'yellow';
    switch (state) {
      case 'live': return 'green';
      case 'connecting':
      case 'reconnecting': return 'yellow';
      case 'paused': return 'yellow';
      case 'error': return 'red';
      default: return '';
    }
  }

  function pillLabel(s) {
    const pending = s.pending || [];
    if (pending.length === 1) return 'Anfrage von ' + pending[0].name;
    if (pending.length > 1) return pending.length + ' offene Anfragen';
    switch (s.state) {
      case 'connecting': return 'Verbinde…';
      case 'live': return 'Live';
      case 'paused': return 'Pausiert – niemand schaut zu';
      case 'reconnecting': return 'Neu verbinden…';
      case 'error': return 'Fehler';
      default: return 'Bereit';
    }
  }

  function viewerText(s) {
    const names = (s.names || []).filter((n) => n);
    if (names.length === 0) return String(s.viewers ?? 0);
    return `${names.join(', ')} (${names.length})`;
  }

  /* ---------- Anfragen ---------- */

  function renderRequests(pending) {
    const card = $('#requests-card');
    const box = $('#requests');
    card.classList.toggle('hidden', pending.length === 0);
    box.innerHTML = '';
    pending.forEach((p) => {
      const row = document.createElement('div');
      row.className = 'request-row';
      const name = document.createElement('span');
      name.className = 'request-name';
      name.textContent = p.name;
      const ok = document.createElement('button');
      ok.className = 'btn primary';
      ok.type = 'button';
      ok.textContent = 'Annehmen';
      ok.addEventListener('click', () => decide('/api/allow', p.id));
      const no = document.createElement('button');
      no.className = 'btn';
      no.type = 'button';
      no.textContent = 'Ablehnen';
      no.addEventListener('click', () => decide('/api/deny', p.id));
      row.append(name, ok, no);
      box.appendChild(row);
    });
  }

  async function decide(url, id) {
    const res = await fetch(url, { method: 'POST', body: JSON.stringify({ id }) });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      hint('#start-error', data.error || 'Aktion fehlgeschlagen', true);
    }
    poll();
  }

  function hint(sel, text, isError) {
    const el = $(sel);
    el.textContent = text;
    el.style.color = isError ? 'var(--red)' : 'var(--dim)';
  }

  /* ---------- Aktionen ---------- */

  $('#btn-start').addEventListener('click', async () => {
    hint('#start-error', '');
    if (!(await save(true))) {
      hint('#start-error', 'Einstellungen stimmen nicht – bitte Server und Raum prüfen.', true);
      return;
    }
    const res = await fetch('/api/start', { method: 'POST', body: JSON.stringify(currentTarget()) });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      hint('#start-error', data.error || 'Start fehlgeschlagen', true);
      return;
    }
    poll();
  });

  $('#btn-stop').addEventListener('click', async () => {
    await fetch('/api/stop', { method: 'POST' });
    poll();
  });

  $('#btn-save').addEventListener('click', () => save(false));

  $('#btn-copy').addEventListener('click', async () => {
    const url = $('#share-url').value;
    try {
      await navigator.clipboard.writeText(url);
      hint('#copy-hint', 'Link kopiert.', false);
    } catch (e) {
      const input = $('#share-url');
      input.select();
      document.execCommand('copy');
      hint('#copy-hint', 'Link kopiert.', false);
    }
  });

  $('#btn-open-viewer').addEventListener('click', () => {
    const url = $('#share-url').value;
    if (url) window.open(url, '_blank');
  });

  $('#btn-new-room').addEventListener('click', () => {
    const alphabet = 'abcdefghjkmnpqrstuvwxyz23456789';
    let code = '';
    for (let i = 0; i < 6; i++) {
      code += alphabet[Math.floor(Math.random() * alphabet.length)];
    }
    $('#cfg-room').value = code;
  });

  $('#window-refresh').addEventListener('click', async () => {
    targets = await fetch('/api/targets').then((r) => r.json());
    fillTargets();
  });

  load();
  poll();
  setInterval(poll, 1000);
})();
