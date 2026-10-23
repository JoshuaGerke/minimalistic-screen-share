/* Das Raum-Fenster. Läuft nur auf diesem PC (127.0.0.1) und bekommt von der
 * App den Raum-Schlüssel. Alles, was den PC verlässt – Stimme, Chat, Dateien,
 * Zeige-Klicks, der eigene Zustand –, wird hier verschlüsselt (e2e.js); der
 * Relay sieht nur Kopf und Größe. Den eigenen Bildschirm schickt die App
 * selbst (internal/share), ebenfalls verschlüsselt.
 *
 * Teilnehmer schicken ihren Zustand als Paket der Art "state":
 *   Person:     { type: 'person', name, voice, muted, deaf }
 *   Bildschirm: { type: 'screen', owner, title, audio, rate, paused, point, origin }
 * Chat-Pakete: { type: 'msg' | 'file', mid, from, ts, … } */
(() => {
  'use strict';

  const $ = (s) => document.querySelector(s);
  const E = window.MssE2E, K = E.KIND;
  const te = new TextEncoder(), td = new TextDecoder();
  const FILE_CHUNK = 60 << 10;
  const FILE_MAX = 2 * 1024 ** 3;

  const store = {
    get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : v; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* egal */ } },
  };

  async function api(path, body) {
    const init = body === undefined ? { cache: 'no-store' } : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
    const r = await fetch(path, init);
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || r.statusText);
    return d;
  }

  const unhex = (h) => new Uint8Array((h.match(/../g) || []).map((x) => parseInt(x, 16)));
  const rand32 = () => crypto.getRandomValues(new Uint32Array(1))[0] >>> 0;
  const randId = () => Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => b.toString(16).padStart(2, '0')).join('');

  let toastTimer = null;
  function toast(text, ms = 2800) {
    const t = $('#toast');
    t.textContent = text;
    t.classList.remove('hidden');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.add('hidden'), ms);
  }

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  }

  /* ======================================================================
   * Stand der App (Raum, Schlüssel, eigenes Teilen) – jede Sekunde
   * ==================================================================== */

  let status = null;
  let key = null, keyHex = '';

  async function poll() {
    try {
      const st = await api('/api/status');
      await apply(st);
    } catch (e) { /* App gerade nicht erreichbar – gleich wieder */ }
    setTimeout(poll, 1000);
  }

  async function apply(st) {
    const prev = status;
    status = st;
    const inRoom = !!st.room;
    $('#start').classList.toggle('hidden', inRoom);
    $('#room').classList.toggle('hidden', !inRoom);
    if (!prev || prev.room !== st.room) document.title = inRoom ? `${st.room} – Screen Share` : 'Screen Share';
    if (!inRoom) {
      disconnect();
      renderStart();
    } else {
      if (st.enc !== keyHex) {
        key = await E.importKey(unhex(st.enc));
        keyHex = st.enc;
      }
      if (st.relay !== wsURL) {
        disconnect();
        if (st.relay) connect(st.relay);
      }
      maybeAutoVoice();
    }
    renderBar();
    renderDock();
    renderSettings();
  }

  /* ======================================================================
   * Verbindung zum Relay
   * ==================================================================== */

  let ws = null, wsURL = '', me = 0, joined = false, wsError = '', retry = null, latency = 0;
  const members = new Map(); // id -> { id, kind, state }
  let rx = Promise.resolve(), tx = Promise.resolve();

  function connect(url) {
    wsURL = url;
    wsError = '';
    const sock = new WebSocket(url);
    sock.binaryType = 'arraybuffer';
    ws = sock;
    sock.onopen = () => {
      sock.send(JSON.stringify({ t: 'join', room: status.room, auth: status.auth, kind: 'person' }));
    };
    sock.onmessage = (ev) => {
      if (sock !== ws) return;
      if (typeof ev.data === 'string') {
        let m;
        try { m = JSON.parse(ev.data); } catch (e) { return; }
        onCtl(m);
        return;
      }
      const buf = ev.data;
      rx = rx.then(() => onPacket(buf)).catch(() => {});
    };
    sock.onclose = () => {
      if (sock !== ws) return;
      ws = null;
      resetRoom();
      renderBar();
      // Relay derselbe? Dann gleich neu versuchen. Zieht der Raum um, kommt
      // die neue Adresse über den Status.
      clearTimeout(retry);
      retry = setTimeout(() => { if (!ws && status && status.relay === wsURL && !wsError) connect(wsURL); }, 1500);
    };
  }

  function disconnect() {
    clearTimeout(retry);
    if (ws) {
      const s = ws;
      ws = null;
      s.onclose = null;
      try { s.close(); } catch (e) { /* egal */ }
    }
    wsURL = '';
    resetRoom();
  }

  function resetRoom() {
    joined = false;
    me = 0;
    members.clear();
    for (const id of [...sources.keys()]) dropSource(id);
    rx = Promise.resolve();
    tx = Promise.resolve();
    updateVoiceMembers();
    renderMembers();
  }

  function onCtl(m) {
    switch (m.t) {
      case 'welcome':
        me = m.id;
        joined = true;
        wsError = '';
        for (const x of m.members || []) members.set(x.id, { id: x.id, kind: x.kind, state: null });
        sendState();
        renderBar();
        renderMembers();
        break;
      case 'join':
        members.set(m.id, { id: m.id, kind: m.kind, state: null });
        break;
      case 'leave': {
        const x = members.get(m.id);
        members.delete(m.id);
        dropSource(m.id);
        if (x && x.state && x.state.type === 'person') toast(`${x.state.name} hat den Raum verlassen`);
        updateVoiceMembers();
        renderMembers();
        break;
      }
      case 'error':
        wsError = m.msg || 'Fehler';
        renderBar();
        break;
      case 'moved':
        // Betreiber gibt ab – die App sucht den neuen, der Status bringt ihn.
        break;
      case 'pong':
        latency = Date.now() - m.ts;
        break;
      case 'file-lost':
        toast('Eine Datei kam bei jemandem nicht vollständig an (Leitung zu langsam).', 5000);
        break;
    }
  }

  setInterval(() => {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ t: 'ping', ts: Date.now() }));
  }, 20000);

  // send verschlüsselt und verschickt – in der Reihenfolge der Aufrufe.
  function send(h, plain) {
    const sock = ws;
    if (!sock || !joined || !key) return Promise.resolve(false);
    const k = key;
    tx = tx.then(async () => {
      const pkt = await E.seal(k, h, plain);
      if (sock.readyState !== WebSocket.OPEN) return false;
      sock.send(pkt);
      return true;
    }).catch(() => false);
    return tx;
  }

  const sendJSON = (kind, obj) => send({ kind, seq: 0 }, te.encode(JSON.stringify(obj)));

  function myName() { return (status && status.config.name) || 'Ich'; }

  function sendState() {
    const v = voice.state;
    sendJSON(K.state, { type: 'person', name: myName(), voice: v.joined, muted: v.muted, deaf: v.deaf });
  }

  async function onPacket(buf) {
    if (!key) return;
    let res;
    try { res = await E.open(key, buf); } catch (e) { return; } // falscher Schlüssel oder manipuliert
    const { h, plain } = res;
    const from = h.sender;
    switch (h.kind) {
      case K.state: onState(from, JSON.parse(td.decode(plain))); break;
      case K.chat: onChat(from, JSON.parse(td.decode(plain))); break;
      case K.voice: voice.onPacket(from, h.seq, h.flags & 1, plain); break;
      case K.video: onVideo(from, h, plain); break;
      case K.system: onSystem(from, h, plain); break;
      case K.file: onFileChunk(from, h, plain); break;
    }
  }

  function onState(from, st) {
    let m = members.get(from);
    if (!m) { m = { id: from, kind: st.type === 'screen' ? 'media' : 'person', state: null }; members.set(from, m); }
    const first = !m.state;
    m.state = st;
    if (st.type === 'screen') {
      if (playable(from)) {
        const src = source(from);
        src.label.textContent = screenLabel(st);
        if (from === focusedId) applySourceAudio(src);
        if (first) toast(`${st.owner} teilt jetzt den Bildschirm`);
      }
      renderStage();
    } else if (st.type === 'person' && first && joined) {
      toast(`${st.name} ist da`);
    }
    updateVoiceMembers();
    renderMembers();
  }

  // playable: Bild und Ton eines Bildschirms nur, wenn er sich beschrieben
  // hat und nicht von diesem PC kommt (sonst Rückkopplung).
  function playable(id) {
    const m = members.get(id);
    if (!m || !m.state || m.state.type !== 'screen') return false;
    if (status && (m.state.origin === status.origin || (status.share && status.share.id === id))) return false;
    return true;
  }

  const screenLabel = (st) => `${st.owner}${st.title ? ' – ' + st.title : ''}${st.paused ? ' (Pause)' : ''}`;

  /* ======================================================================
   * Bildschirme der anderen
   * ==================================================================== */

  const sources = new Map();
  let focusedId = 0;

  function source(id) {
    let src = sources.get(id);
    if (src) return src;
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d', { alpha: false, desynchronized: true });
    const tile = el('div', 'tile');
    const label = el('span', 'label');
    tile.append(label);
    tile.addEventListener('click', () => focus(id));
    canvas.addEventListener('dblclick', toggleFullscreen);
    canvas.addEventListener('pointerdown', (e) => onPointerDown(e, id));
    src = { id, canvas, ctx, tile, label, vasm: null, vdec: { dec: null, codec: '', waitKey: true, ts: 0 }, hasFrame: false };
    sources.set(id, src);
    if (!focusedId) focus(id);
    return src;
  }

  function dropSource(id) {
    const src = sources.get(id);
    if (!src) return;
    if (src.vdec.dec && src.vdec.dec.state !== 'closed') src.vdec.dec.close();
    src.canvas.remove();
    src.tile.remove();
    sources.delete(id);
    if (focusedId === id) {
      focusedId = 0;
      const next = sources.keys().next();
      if (!next.done) focus(next.value);
    }
    renderStage();
  }

  function focus(id) {
    if (!sources.has(id)) return;
    if (focusedId !== id) {
      focusedId = id;
      applySourceAudio(sources.get(id));
    }
    renderStage();
  }

  function renderStage() {
    const stage = $('#stage'), tiles = $('#tiles');
    let shown = 0;
    for (const src of sources.values()) {
      const m = members.get(src.id);
      if (m && m.state) src.label.textContent = screenLabel(m.state);
      if (src.id === focusedId) {
        if (src.canvas.parentNode !== stage) stage.prepend(src.canvas);
        src.tile.remove();
      } else {
        if (src.canvas.parentNode !== src.tile) src.tile.prepend(src.canvas);
        if (src.tile.parentNode !== tiles) tiles.append(src.tile);
        shown++;
      }
    }
    tiles.classList.toggle('hidden', shown === 0);
    const f = sources.get(focusedId);
    const fm = f && members.get(f.id);
    $('#stage-empty').classList.toggle('hidden', !!(f && f.hasFrame));
    $('#stage-empty .big').textContent = f ? 'Bild kommt gleich…' : 'Noch teilt niemand.';
    $('#stage-label').classList.toggle('hidden', !f);
    $('#stage-label').textContent = fm && fm.state ? screenLabel(fm.state) : '';
    $('#point-hint').classList.toggle('hidden', !(fm && fm.state && fm.state.point && f.hasFrame));
  }

  function onVideo(from, h, part) {
    if (!playable(from)) return;
    const src = source(from);
    let a = src.vasm;
    if (!a || a.seq !== h.seq) a = src.vasm = { seq: h.seq, parts: new Array(h.count), got: 0 };
    if (!a.parts[h.idx]) { a.parts[h.idx] = part; a.got++; }
    if (a.got !== h.count) return;
    src.vasm = null;
    const total = a.parts.reduce((n, p) => n + p.length, 0);
    const au = new Uint8Array(total);
    let off = 0;
    for (const p of a.parts) { au.set(p, off); off += p.length; }
    decodeAU(src, au, (h.flags & 1) !== 0);
  }

  function spsCodec(au) {
    for (let i = 0; i + 7 < au.length; i++) {
      if (au[i] === 0 && au[i + 1] === 0 && au[i + 2] === 1 && (au[i + 3] & 0x1f) === 7) {
        const x = (b) => b.toString(16).padStart(2, '0');
        return `avc1.${x(au[i + 4])}${x(au[i + 5])}${x(au[i + 6])}`;
      }
    }
    return '';
  }

  // H.264 über WebCodecs: Die Grafikkarte dekodiert, das Bild landet direkt im Canvas.
  function decodeAU(src, au, isKey) {
    if (!('VideoDecoder' in window)) return;
    const vd = src.vdec;
    if (isKey) {
      const codec = spsCodec(au);
      if (codec && (codec !== vd.codec || !vd.dec || vd.dec.state === 'closed')) {
        try {
          if (vd.dec && vd.dec.state !== 'closed') vd.dec.close();
          vd.dec = new VideoDecoder({ output: (f) => drawFrame(src, f), error: () => { vd.waitKey = true; vd.codec = ''; } });
          vd.dec.configure({ codec, optimizeForLatency: true });
          vd.codec = codec;
          vd.waitKey = true;
        } catch (e) { return; }
      }
    }
    if (!vd.dec || vd.dec.state !== 'configured') return;
    if (vd.waitKey) { if (!isKey) return; vd.waitKey = false; }
    // Kommt der Decoder nicht hinterher, lieber zum nächsten Keyframe springen.
    if (!isKey && vd.dec.decodeQueueSize > 8) { vd.waitKey = true; return; }
    vd.ts += 16667;
    try {
      vd.dec.decode(new EncodedVideoChunk({ type: isKey ? 'key' : 'delta', timestamp: vd.ts, data: au }));
    } catch (e) { vd.waitKey = true; }
  }

  function drawFrame(src, frame) {
    if (!sources.has(src.id)) { frame.close(); return; }
    const w = frame.displayWidth, h = frame.displayHeight;
    if (src.canvas.width !== w || src.canvas.height !== h) { src.canvas.width = w; src.canvas.height = h; }
    src.ctx.drawImage(frame, 0, 0);
    frame.close();
    if (!src.hasFrame) { src.hasFrame = true; renderStage(); }
  }

  /* ---------- Zeigen: 5× schnell auf dieselbe Stelle ---------- */

  const clicks = [];
  function onPointerDown(e, id) {
    if (id !== focusedId || e.button !== 0) return;
    const m = members.get(id);
    if (!m || !m.state || !m.state.point) return;
    const now = performance.now();
    while (clicks.length && now - clicks[0].t > 1500) clicks.shift();
    if (clicks.length && Math.hypot(e.clientX - clicks[0].x, e.clientY - clicks[0].y) > 40) clicks.length = 0;
    clicks.push({ t: now, x: e.clientX, y: e.clientY });
    ripple(e.clientX, e.clientY, false);
    if (clicks.length < 5) return;
    clicks.length = 0;
    const pos = imagePosition(e.currentTarget, e.clientX, e.clientY);
    if (!pos) return;
    sendJSON(K.event, { type: 'point', target: id, x: pos.x, y: pos.y, from: myName() });
    ripple(e.clientX, e.clientY, true);
    toast(`${m.state.owner} sieht jetzt, wo du hinzeigst`);
  }

  // Klick in Bildkoordinaten 0..1 – das Bild ist eingepasst (object-fit: contain).
  function imagePosition(canvas, cx, cy) {
    const r = canvas.getBoundingClientRect();
    const aspect = canvas.width / canvas.height;
    let dw = r.width, dh = r.height;
    if (r.width / r.height > aspect) dw = dh * aspect; else dh = dw / aspect;
    const x = (cx - r.left - (r.width - dw) / 2) / dw, y = (cy - r.top - (r.height - dh) / 2) / dh;
    return x < 0 || x > 1 || y < 0 || y > 1 ? null : { x, y };
  }

  function ripple(x, y, big) {
    const r = el('div', 'ripple' + (big ? ' big' : ''));
    r.style.left = x + 'px';
    r.style.top = y + 'px';
    document.body.append(r);
    setTimeout(() => r.remove(), 1300);
  }

  function toggleFullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else $('#stage').requestFullscreen().catch(() => {});
  }

  /* ======================================================================
   * Systemton des groß gezeigten Bildschirms
   * ==================================================================== */

  const PLAYER = `const audioCore = ${audioCore.toString()};
class MssPlayer extends AudioWorkletProcessor {
  constructor() { super(); this.core = audioCore(sampleRate, 0.08); this.spare = new Float32Array(128);
    this.port.onmessage = (e) => { const m = e.data;
      if (m.l) this.core.push(m.l, m.r); else if (m.silence) this.core.pushSilence(m.silence);
      else if (m.rate) this.core.setRate(m.rate); else if (m.reset) this.core.reset(); }; }
  process(i, outputs) { const o = outputs[0]; if (this.spare.length !== o[0].length) this.spare = new Float32Array(o[0].length);
    this.core.pull(o[0], o[1] || this.spare); return true; }
}
registerProcessor('mss-player', MssPlayer);`;

  const sound = { ctx: null, gain: null, node: null, rate: 48000, on: store.get('mss-sound', '1') === '1', volume: Number(store.get('mss-volume', '80')), warned: false };

  async function ensureSound() {
    if (sound.ctx) { if (sound.ctx.state !== 'running') sound.ctx.resume().catch(() => {}); return; }
    const ctx = new AudioContext({ latencyHint: 'interactive' });
    sound.ctx = ctx;
    sound.gain = ctx.createGain();
    sound.gain.gain.value = 0;
    sound.gain.connect(ctx.destination);
    const url = URL.createObjectURL(new Blob([PLAYER], { type: 'application/javascript' }));
    await ctx.audioWorklet.addModule(url);
    URL.revokeObjectURL(url);
    sound.node = new AudioWorkletNode(ctx, 'mss-player', { numberOfInputs: 0, outputChannelCount: [2] });
    sound.node.port.postMessage({ rate: sound.rate });
    sound.node.connect(sound.gain);
    applyVolume();
  }

  // blockedByOwnShare: Unter Linux nimmt das eigene Teilen alles auf, was
  // dieser PC abspielt – der Ton der anderen käme bei ihnen als Echo zurück
  // und würde sich aufschaukeln. Dann bleibt er hier aus.
  function blockedByOwnShare() {
    return !!(status && status.platform !== 'windows' && status.share && status.share.audio);
  }

  function applyVolume() {
    if (!sound.gain) return;
    const v = sound.on && !blockedByOwnShare() ? sound.volume / 100 : 0;
    sound.gain.gain.setTargetAtTime(v, sound.ctx.currentTime, 0.03);
  }

  function applySourceAudio(src) {
    if (!sound.node) return;
    sound.node.port.postMessage({ reset: true });
    const m = members.get(src.id);
    const rate = m && m.state && m.state.rate;
    if (rate && rate !== sound.rate) { sound.rate = rate; sound.node.port.postMessage({ rate }); }
  }

  function onSystem(from, h, plain) {
    if (from !== focusedId || !playable(from)) return;
    if (!sound.node) { ensureSound().catch(() => {}); return; }
    if (blockedByOwnShare()) {
      if (!sound.warned) { sound.warned = true; toast('Solange du unter Linux mit Ton teilst, hörst du den Ton der anderen nicht – sonst gäbe es ein Echo.', 6000); }
      return;
    }
    if (h.flags & 1) { sound.node.port.postMessage({ silence: Math.round(sound.rate / 50) }); return; }
    const perCh = Math.round(sound.rate / 50) * 2;
    const ch = Math.max(1, Math.round(plain.length / perCh));
    const frames = Math.floor(plain.length / (2 * ch));
    const dv = new DataView(plain.buffer, plain.byteOffset, plain.byteLength);
    const l = new Float32Array(frames), r = new Float32Array(frames);
    for (let i = 0; i < frames; i++) {
      l[i] = dv.getInt16(i * 2 * ch, true) / 32768;
      r[i] = ch > 1 ? dv.getInt16(i * 2 * ch + 2, true) / 32768 : l[i];
    }
    sound.node.port.postMessage({ l, r }, [l.buffer, r.buffer]);
  }

  /* ======================================================================
   * Sprachkanal
   * ==================================================================== */

  const overlay = MssOverlay.create({ base: '', platform: () => (status ? status.platform : '') });
  const voice = MssVoice.create({
    send: (opus, stereo) => send({ kind: K.voice, flags: stereo ? 1 : 0, seq: voiceSeq++ }, opus),
    onState: () => { sendState(); updateVoiceMembers(); renderMembers(); },
    onChange: (v) => { overlay.publish(v.snapshot()); renderMembers(); },
    onOverlay: (v) => overlay.toggle(v.snapshot()),
    onSettings: () => openSettings('voice'),
    canJoin: () => joined,
    joinHint: () => 'Erst verbinden…',
  });
  let voiceSeq = 0;
  voice.mount($('#voice'), $('#voice-settings'));

  function updateVoiceMembers() {
    const list = [];
    if (voice.state.joined && me) list.push({ id: me, name: myName(), muted: voice.state.muted, deaf: voice.state.deaf });
    for (const m of members.values()) {
      const s = m.state;
      if (m.id !== me && s && s.type === 'person' && s.voice) list.push({ id: m.id, name: s.name, muted: !!s.muted, deaf: !!s.deaf });
    }
    list.sort((a, b) => a.id - b.id);
    voice.setMembers(list, me);
  }

  let autoVoiceDone = false;
  async function maybeAutoVoice() {
    if (autoVoiceDone || !status.auto_voice || !joined || voice.state.joined) return;
    autoVoiceDone = true;
    await voice.join();
    if (status.auto_muted) voice.setMuted(true);
    api('/api/voice-joined', {}).catch(() => {});
  }

  /* ---------- Teilnehmerliste ---------- */

  const ICON = {
    mic: '<svg viewBox="0 0 24 24" width="14" height="14"><rect x="9" y="3" width="6" height="11" rx="3" fill="currentColor"/><path d="M5.5 11a6.5 6.5 0 0013 0M12 17.5V21" fill="none" stroke="currentColor" stroke-width="1.8"/><path d="M4 4l16 16" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg>',
    deaf: '<svg viewBox="0 0 24 24" width="14" height="14"><path d="M4 14v-2a8 8 0 0116 0v2" fill="none" stroke="currentColor" stroke-width="1.8"/><rect x="3" y="13" width="4" height="7" rx="1.5" fill="currentColor"/><rect x="17" y="13" width="4" height="7" rx="1.5" fill="currentColor"/><path d="M4 4l16 16" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg>',
    screen: '<svg viewBox="0 0 24 24" width="14" height="14"><rect x="3" y="4" width="18" height="12" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 20h8" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>',
  };

  let openVol = 0;
  function renderMembers() {
    const box = $('#members');
    if (!box) return;
    const speaking = new Map(voice.state.members.map((m) => [m.id, !!m.speaking]));
    const screens = new Set([...members.values()].filter((m) => m.state && m.state.type === 'screen').map((m) => m.state.owner));
    const people = [];
    if (joined) people.push({ id: me, name: myName(), voice: voice.state.joined, muted: voice.state.muted, deaf: voice.state.deaf, self: true });
    for (const m of members.values()) {
      if (m.id !== me && m.state && m.state.type === 'person') people.push({ id: m.id, ...m.state });
    }
    people.sort((a, b) => a.id - b.id);
    $('#count').textContent = people.length ? String(people.length) : '';
    box.innerHTML = '';
    for (const p of people) {
      const row = el('div', 'member' + (speaking.get(p.id) ? ' speaking' : '') + (p.voice ? ' in-voice' : ''));
      const av = el('span', 'avatar', (p.name || '?').slice(0, 1).toUpperCase());
      const name = el('span', 'name', p.name + (p.self ? ' (du)' : ''));
      row.append(av, name);
      const flags = el('span', 'flags');
      if (screens.has(p.name) || (p.self && status && status.share)) flags.insertAdjacentHTML('beforeend', `<i class="live" title="teilt den Bildschirm">${ICON.screen}</i>`);
      if (p.voice && p.deaf) flags.insertAdjacentHTML('beforeend', `<i class="off" title="taub">${ICON.deaf}</i>`);
      else if (p.voice && p.muted) flags.insertAdjacentHTML('beforeend', `<i class="off" title="stumm">${ICON.mic}</i>`);
      row.append(flags);
      if (!p.self && p.voice && voice.state.joined) {
        row.title = 'Klicken für Lautstärke';
        row.addEventListener('click', () => { openVol = openVol === p.id ? 0 : p.id; renderMembers(); });
        if (openVol === p.id) {
          const vol = el('input');
          vol.type = 'range'; vol.min = 0; vol.max = 200; vol.step = 5;
          vol.value = voice.settings.volumes[p.name] ?? 100;
          vol.addEventListener('click', (e) => e.stopPropagation());
          vol.addEventListener('input', () => voice.setVolume(p.name, Number(vol.value)));
          const wrap = el('div', 'member-vol');
          wrap.append(vol);
          box.append(row, wrap);
          continue;
        }
      }
      box.append(row);
    }
    if (!people.length) box.append(el('p', 'dim small', joined ? '' : 'Verbinde…'));
  }

  /* ======================================================================
   * Chat und Dateien
   * ==================================================================== */

  const seen = new Set();
  const files = new Map(); // fid -> { meta, parts, got, count, url, el, own }

  function onChat(from, msg) {
    if (!msg || !msg.mid || seen.has(msg.mid)) return;
    seen.add(msg.mid);
    addMessage(msg, false);
    if (document.hidden && msg.type === 'msg') toast(`${msg.from}: ${msg.text}`.slice(0, 120));
  }

  function fmtSize(n) {
    if (n < 1024) return n + ' B';
    if (n < 1 << 20) return (n / 1024).toFixed(0) + ' KB';
    if (n < 1 << 30) return (n / (1 << 20)).toFixed(1) + ' MB';
    return (n / (1 << 30)).toFixed(2) + ' GB';
  }

  function linkify(parent, text) {
    const re = /https?:\/\/[^\s<>"]+/g;
    let last = 0, m;
    while ((m = re.exec(text))) {
      parent.append(text.slice(last, m.index));
      const a = el('a', '', m[0]);
      a.href = m[0];
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      parent.append(a);
      last = m.index + m[0].length;
    }
    parent.append(text.slice(last));
  }

  let lastFrom = '', lastTs = 0;
  function addMessage(msg, own) {
    const list = $('#messages');
    const stick = list.scrollHeight - list.scrollTop - list.clientHeight < 40;
    const grouped = msg.from === lastFrom && msg.ts - lastTs < 5 * 60000;
    lastFrom = msg.from; lastTs = msg.ts;
    const item = el('div', 'msg' + (own ? ' own' : '') + (grouped ? ' grouped' : ''));
    if (!grouped) {
      const head = el('div', 'msg-head');
      head.append(el('b', '', msg.from), el('span', 'dim', new Date(msg.ts).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })));
      item.append(head);
    }
    if (msg.type === 'msg') {
      const body = el('div', 'msg-text');
      linkify(body, String(msg.text || ''));
      item.append(body);
    } else if (msg.type === 'file') {
      const card = el('div', 'file');
      card.append(el('span', 'file-icon', '📄'));
      const info = el('div', 'file-info');
      info.append(el('b', '', msg.name), el('span', 'dim small file-state', fmtSize(msg.size)));
      card.append(info);
      const f = fileEntry(msg.fid);
      f.meta = msg;
      f.el = card;
      f.own = own;
      if (!own) setTimeout(() => { if (!f.got && !f.url) fileState(f, 'Nur für die, die beim Senden da waren'); }, 15000);
      renderFile(f);
      item.append(card);
    }
    list.append(item);
    if (stick || own) list.scrollTop = list.scrollHeight;
  }

  function fileEntry(fid) {
    let f = files.get(fid);
    if (!f) { f = { meta: null, parts: [], got: 0, count: 0, url: '', el: null, own: false, sent: 0 }; files.set(fid, f); }
    return f;
  }

  function fileState(f, text) {
    const s = f.el && f.el.querySelector('.file-state');
    if (s) s.textContent = text;
  }

  function renderFile(f) {
    if (!f.el || !f.meta) return;
    const size = fmtSize(f.meta.size);
    if (f.own) {
      fileState(f, f.sent >= 1 ? `${size} · gesendet` : `${size} · sende ${Math.round(f.sent * 100)} %`);
    } else if (f.url) {
      fileState(f, size);
      if (!f.el.querySelector('a')) {
        const a = el('a', 'btn primary small-btn', 'Speichern');
        a.href = f.url;
        a.download = f.meta.name;
        f.el.append(a);
      }
    } else if (f.count) {
      fileState(f, `${size} · ${Math.round(f.got / f.count * 100)} %`);
    }
  }

  function onFileChunk(from, h, part) {
    const f = fileEntry(h.seq);
    if (f.url || f.own) return;
    f.count = h.count;
    if (!f.parts[h.idx]) { f.parts[h.idx] = part; f.got++; }
    if (f.got === f.count) {
      const blob = new Blob(f.parts, { type: (f.meta && f.meta.mime) || 'application/octet-stream' });
      f.parts = [];
      f.url = URL.createObjectURL(blob);
    }
    if (f.got % 16 === 0 || f.url) renderFile(f);
  }

  function sendChat(text) {
    text = text.trim();
    if (!text || !joined) return;
    const msg = { type: 'msg', mid: randId(), from: myName(), ts: Date.now(), text };
    seen.add(msg.mid);
    sendJSON(K.chat, msg);
    addMessage(msg, true);
  }

  const wait = (ms) => new Promise((r) => setTimeout(r, ms));

  async function sendFile(file) {
    if (!joined) { toast('Erst verbinden…'); return; }
    if (file.size > FILE_MAX) { toast(`${file.name}: höchstens 2 GB`); return; }
    const fid = rand32();
    const msg = { type: 'file', mid: randId(), fid, from: myName(), ts: Date.now(), name: file.name, size: file.size, mime: file.type };
    seen.add(msg.mid);
    await sendJSON(K.chat, msg);
    addMessage(msg, true);
    const f = fileEntry(fid);
    const count = Math.max(1, Math.ceil(file.size / FILE_CHUNK));
    const sock = ws;
    for (let i = 0; i < count; i++) {
      // Nicht mehr als ein paar MB vorauslaufen – sonst stauen sich Stimme und Bild dahinter.
      while (sock === ws && sock && sock.bufferedAmount > 4 << 20) await wait(25);
      if (sock !== ws) { fileState(f, 'abgebrochen – Verbindung weg'); return; }
      const part = new Uint8Array(await file.slice(i * FILE_CHUNK, (i + 1) * FILE_CHUNK).arrayBuffer());
      await send({ kind: K.file, seq: fid, idx: i, count }, part);
      f.sent = (i + 1) / count;
      if (i % 16 === 0 || i === count - 1) renderFile(f);
    }
  }

  $('#chat-form').addEventListener('submit', (e) => {
    e.preventDefault();
    sendChat($('#chat-input').value);
    $('#chat-input').value = '';
  });
  $('#btn-attach').addEventListener('click', () => $('#file-input').click());
  $('#file-input').addEventListener('change', (e) => {
    for (const f of e.target.files) sendFile(f);
    e.target.value = '';
  });

  // Dateien ins Fenster ziehen.
  let dragDepth = 0;
  const hasFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes('Files');
  window.addEventListener('dragenter', (e) => { if (!hasFiles(e) || !status || !status.room) return; dragDepth++; $('#drop').classList.remove('hidden'); });
  window.addEventListener('dragleave', () => { if (--dragDepth <= 0) { dragDepth = 0; $('#drop').classList.add('hidden'); } });
  window.addEventListener('dragover', (e) => { if (hasFiles(e)) e.preventDefault(); });
  window.addEventListener('drop', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth = 0;
    $('#drop').classList.add('hidden');
    if (status && status.room) for (const f of e.dataTransfer.files) sendFile(f);
  });
  // Bilder aus der Zwischenablage einfügen.
  $('#chat-input').addEventListener('paste', (e) => {
    for (const item of e.clipboardData.items) if (item.kind === 'file') { sendFile(item.getAsFile()); e.preventDefault(); }
  });

  /* ======================================================================
   * Kopfleiste, Teilen, Start
   * ==================================================================== */

  function renderBar() {
    if (!status || !status.room) return;
    $('#room-id').textContent = status.room;
    let dot = 'yellow', text = 'Suche den Raum…';
    if (wsError) { dot = 'red'; text = wsError; }
    else if (joined) { dot = 'green'; text = status.hosting ? 'Verbunden · dieser PC betreibt den Raum' : 'Verbunden'; }
    else if (status.relay) text = 'Verbinde…';
    else if (status.error) text = 'Starte den Raum… (' + status.error + ')';
    else if (status.state === 'suche') text = 'Suche den Raum – sonst wird er hier angelegt…';
    $('#room-dot').className = 'dot ' + dot;
    $('#room-state').textContent = text;
    $('#btn-share').disabled = !status.room;
  }

  function renderDock() {
    if (!status || !status.room) return;
    const sh = status.share;
    const b = $('#btn-share');
    b.classList.toggle('primary', !sh);
    b.classList.toggle('danger', !!sh);
    b.querySelector('span').textContent = sh ? 'Teilen beenden' : 'Bildschirm teilen';
    $('#btn-source').classList.toggle('hidden', !sh);
    $('#btn-pause').classList.toggle('hidden', !sh);
    if (sh) {
      $('#btn-pause').textContent = sh.paused ? 'Fortsetzen' : 'Pausieren';
      const parts = [sh.target];
      if (sh.live) parts.push(`${sh.fps} fps`, `${sh.kbps} kbit/s`);
      if (sh.encoder) parts.push(sh.encoder);
      $('#share-info').textContent = sh.error ? sh.error : 'Du teilst: ' + parts.join(' · ');
      $('#share-info').classList.toggle('error', !!sh.error);
    } else {
      $('#share-info').textContent = '';
    }
    const snd = $('#btn-sound');
    snd.classList.toggle('off', !sound.on);
    $('#vol').value = String(sound.volume);
    applyVolume();
  }

  $('#room-id').addEventListener('click', () => {
    navigator.clipboard.writeText(status.room).then(() => toast('Raum-ID kopiert – das Passwort sagst du am besten mündlich.'), () => {});
  });
  $('#btn-leave').addEventListener('click', async () => {
    voice.leave();
    disconnect();
    await api('/api/leave', {}).catch(() => {});
    poll1();
  });
  $('#btn-sound').addEventListener('click', () => {
    sound.on = !sound.on;
    store.set('mss-sound', sound.on ? '1' : '0');
    ensureSound().catch(() => {});
    renderDock();
  });
  $('#vol').addEventListener('input', (e) => {
    sound.volume = Number(e.target.value);
    store.set('mss-volume', String(sound.volume));
    ensureSound().catch(() => {});
    applyVolume();
  });
  $('#btn-full').addEventListener('click', toggleFullscreen);
  $('#btn-share').addEventListener('click', () => {
    if (status.share) api('/api/share/stop', {}).then(poll1, (e) => toast(e.message));
    else openPicker();
  });
  $('#btn-source').addEventListener('click', openPicker);
  $('#btn-pause').addEventListener('click', () => api('/api/share/pause', { on: !status.share.paused }).then(poll1));

  async function poll1() { try { await apply(await api('/api/status')); } catch (e) { /* egal */ } }

  /* ---------- Quelle wählen ---------- */

  async function openPicker() {
    const dlg = $('#pick');
    const list = $('#pick-list');
    list.innerHTML = '<p class="dim">Lade…</p>';
    $('#pick-audio').checked = !!status.config.audio;
    dlg.showModal();
    let t = { monitors: [], windows: [] };
    try { t = await api('/api/targets'); } catch (e) { /* nur Hauptbildschirm */ }
    const cursor = !!(status.config.target && status.config.target.cursor);
    const opts = [{ label: 'Hauptbildschirm', sub: 'empfohlen', target: { kind: 'primary', cursor } }];
    if (t.monitors.length > 1) {
      for (const m of t.monitors) opts.push({ label: `Monitor ${m.index + 1}`, sub: `${m.w}×${m.h}`, target: { kind: 'monitor', monitor: m.index, cursor } });
      if (t.platform === 'windows') opts.push({ label: 'Alle Monitore', sub: 'zusammen', target: { kind: 'screen', cursor } });
    }
    for (const w of t.windows) opts.push({ label: w.title, sub: 'Fenster', target: { kind: 'window', window: w.title, cursor }, win: true });
    list.innerHTML = '';
    for (const o of opts) {
      const b = el('button', 'pick' + (o.win ? ' win' : ''));
      b.type = 'button';
      b.append(el('b', '', o.label), el('span', 'dim small', o.sub));
      b.addEventListener('click', async () => {
        dlg.close();
        try {
          if ($('#pick-audio').checked !== !!status.config.audio) await api('/api/share/audio', { on: $('#pick-audio').checked });
          await api('/api/share/start', o.target);
          poll1();
        } catch (e) { toast(e.message, 5000); }
      });
      list.append(b);
    }
  }

  /* ---------- Start ---------- */

  let startFilled = false;
  function renderStart() {
    if (!startFilled && status) {
      startFilled = true;
      $('#j-name').value = status.config.name || '';
    }
    const saved = (status && status.saved) || [];
    $('#saved').classList.toggle('hidden', !saved.length);
    const box = $('#saved-list');
    const k = saved.join('|');
    if (box.dataset.k === k) return;
    box.dataset.k = k;
    box.innerHTML = '';
    for (const id of saved) {
      const row = el('div', 'saved-row');
      const go = el('button', 'saved-go');
      go.type = 'button';
      go.append(el('span', 'avatar', id.slice(0, 1).toUpperCase()), el('b', '', id));
      go.addEventListener('click', () => join(() => api('/api/join-saved', { room: id })));
      const x = el('button', 'btn icon small-x', '✕');
      x.type = 'button';
      x.title = 'Vergessen';
      x.addEventListener('click', () => api('/api/forget', { room: id }).then(poll1));
      row.append(go, x);
      box.append(row);
    }
  }

  async function saveName() {
    const name = $('#j-name').value.trim();
    if (!name) throw new Error('Bitte gib deinen Namen ein.');
    if (name !== status.config.name) await api('/api/config', { ...status.config, name });
  }

  async function join(fn) {
    const err = $('#j-error');
    err.classList.add('hidden');
    try {
      await saveName();
      await fn();
      await poll1();
    } catch (e) {
      err.textContent = e.message;
      err.classList.remove('hidden');
    }
  }

  $('#join-form').addEventListener('submit', (e) => {
    e.preventDefault();
    join(() => api('/api/join', { room: $('#j-room').value, password: $('#j-pass').value, remember: $('#j-remember').checked }))
      .then(() => { $('#j-pass').value = ''; });
  });
  $('#j-show').addEventListener('click', () => {
    const p = $('#j-pass');
    p.type = p.type === 'password' ? 'text' : 'password';
    $('#j-show').textContent = p.type === 'password' ? 'zeigen' : 'verbergen';
  });
  $('#j-random').addEventListener('click', () => {
    const words = ['blau', 'fuchs', 'wolke', 'stern', 'kaffee', 'nebel', 'tiger', 'welle', 'berg', 'funke'];
    const w = () => words[rand32() % words.length];
    $('#j-room').value = `${w()}-${w()}-${(rand32() % 900) + 100}`;
  });
  $('#j-room').addEventListener('input', (e) => { e.target.value = e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ''); });

  /* ======================================================================
   * Einstellungen
   * ==================================================================== */

  function openSettings(tab) {
    showTab(tab || 'general');
    voice.listDevices();
    loadApps();
    $('#settings').showModal();
  }
  function showTab(tab) {
    for (const b of document.querySelectorAll('.tabs button')) b.classList.toggle('active', b.dataset.tab === tab);
    for (const s of document.querySelectorAll('section.tab')) s.classList.toggle('hidden', s.dataset.tab !== tab);
  }
  $('#btn-settings').addEventListener('click', () => openSettings());
  $('#start-settings').addEventListener('click', () => openSettings());
  for (const b of document.querySelectorAll('.tabs button')) b.addEventListener('click', () => showTab(b.dataset.tab));
  for (const b of document.querySelectorAll('[data-close]')) b.addEventListener('click', () => b.closest('dialog').close());
  for (const d of document.querySelectorAll('dialog')) d.addEventListener('click', (e) => { if (e.target === d) d.close(); });

  function renderSettings() {
    if (!status) return;
    const c = status.config;
    const dlg = $('#settings');
    const focusIn = dlg.contains(document.activeElement) ? document.activeElement : null;
    for (const i of dlg.querySelectorAll('[data-cfg]')) {
      if (i === focusIn) continue;
      const v = c[i.dataset.cfg];
      if (i.type === 'checkbox') i.checked = !!v;
      else if (i.tagName === 'SELECT' && i.dataset.cfg === 'auto_join') {
        const k = (status.saved || []).join('|');
        if (i.dataset.k !== k) {
          i.dataset.k = k;
          i.innerHTML = '';
          i.append(new Option('– nein –', ''));
          for (const id of status.saved || []) i.append(new Option(id, id));
        }
        i.value = v || '';
      } else i.value = v ?? '';
    }
    const cur = dlg.querySelector('[data-cfg-target=cursor]');
    if (cur !== focusIn) cur.checked = !!(c.target && c.target.cursor);
    dlg.querySelector('.auto-only').classList.toggle('hidden', !c.auto_join);
    if (document.activeElement !== $('#s-autostart')) $('#s-autostart').checked = !!status.autostart;
    $('#fps-label').textContent = c.fps + ' fps';
    $('#br-label').textContent = (c.bitrate / 1000).toFixed(1).replace('.', ',') + ' Mbit/s';
    $('#s-version').textContent = status.version;
    const u = status.update || {};
    const ub = $('#s-update');
    ub.classList.toggle('hidden', !u.available);
    ub.disabled = !!u.applying;
    ub.textContent = u.applying ? 'Aktualisiere…' : `Auf ${u.latest} aktualisieren`;
    $('#s-update-text').textContent = u.error ? 'Update: ' + u.error : (u.available ? '' : 'Aktuell.');
  }

  async function saveConfig(patch) {
    try {
      await api('/api/config', { ...status.config, ...patch });
      await poll1();
    } catch (e) { toast(e.message, 4000); }
  }

  $('#settings').addEventListener('change', (e) => {
    const i = e.target;
    if (i.dataset.cfg) {
      let v = i.type === 'checkbox' ? i.checked : i.value;
      if (i.type === 'range') v = Number(v);
      saveConfig({ [i.dataset.cfg]: v });
      if (i.dataset.cfg === 'name') sendState();
    } else if (i.dataset.cfgTarget) {
      saveConfig({ target: { ...status.config.target, cursor: i.checked } });
    } else if (i.id === 's-autostart') {
      api('/api/autostart', { on: i.checked }).then(poll1, (err) => toast(err.message));
    }
  });
  $('#settings').addEventListener('input', (e) => {
    const i = e.target;
    if (i.dataset.cfg === 'fps') $('#fps-label').textContent = i.value + ' fps';
    if (i.dataset.cfg === 'bitrate') $('#br-label').textContent = (i.value / 1000).toFixed(1).replace('.', ',') + ' Mbit/s';
  });
  $('#s-update').addEventListener('click', () => api('/api/update', {}).then(poll1));
  $('#s-log').addEventListener('click', () => api('/api/openlog', {}));
  $('#s-quit').addEventListener('click', () => { if (confirm('App beenden? Du verlässt dabei auch den Raum.')) api('/api/quit', {}).then(() => window.close()); });

  async function loadApps() {
    const box = $('#apps');
    let d;
    try { d = await api('/api/audiosessions'); } catch (e) { box.textContent = ''; return; }
    $('#apps-note').textContent = d.note || (d.exclusive
      ? 'Windows nimmt ein Programm aus dem geteilten Ton – du hörst es selbst weiter. Die Stimmen aus dem Raum sind nie dabei.'
      : 'Das gewählte Programm wird beim Teilen stumm geschaltet.');
    box.innerHTML = '';
    if (!d.sessions.length) box.append(el('p', 'dim small', 'Gerade spielt kein Programm Ton ab.'));
    for (const s of d.sessions) {
      const row = el('label', 'switch');
      const cb = el('input');
      cb.type = 'checkbox';
      cb.checked = (d.muted || []).includes(s.name);
      cb.addEventListener('change', () => api('/api/audiosessions', { name: s.name, muted: cb.checked }).then(loadApps, (e) => toast(e.message)));
      const lab = el('span', 'label');
      lab.append(el('b', '', s.label || s.name));
      row.append(cb, lab);
      box.append(row);
    }
  }

  window.addEventListener('keydown', (e) => {
    if (e.key === 'F11' && status && status.room) { e.preventDefault(); toggleFullscreen(); }
  });

  // Für lautlose Tests von außen.
  window.__mss = { members, sources, voice, files, get joined() { return joined; }, get me() { return me; }, sendChat, sendFile };

  poll();
})();
