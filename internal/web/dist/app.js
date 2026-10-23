/* Viewer und Konferenz: WebSocket rein, Bildschirme und Ton raus.
 *
 * Jeder geteilte Bildschirm ist eine "Quelle": 0 ist der Gastgeber, alle
 * anderen sind Teilnehmer, die über ihre App in den Raum teilen (deren
 * Pakete kommen verpackt als MsgSourced mit ihrer Kennung). Eine Quelle ist
 * groß zu sehen, die anderen als Kacheln darunter. */
(() => {
  'use strict';

  const $ = (s) => document.querySelector(s);
  const params = new URLSearchParams(location.search);

  const store = {
    get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : v; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* egal */ } },
  };

  let room = (params.get('room') || '').trim().toLowerCase();
  // Der Gastgeber öffnet die Konferenz mit dem Schlüssel seiner App: kein
  // Anklopfen, und sein eigenes Bild kommt nicht zurück.
  const hostKey = params.get('key') || '';
  // Relay woanders als diese Seite (Konferenz-Fenster der App).
  const relayURL = params.get('server') || '';
  let myName = store.get('mss-name', '');
  let ws = null;
  let wantConnect = false;
  let state = 'idle';
  let viewers = 0;
  let framesDrawn = 0, bytesIn = 0, fps = 0, kbps = 0, rtt = 0;
  let lastPing = 0, unanswered = 0;
  let reconnectTimer = null, reconnectDelay = 1000;
  let lastError = '';

  /* ======================================================================
   * Ton des Streams
   *
   * Ein Ringpuffer mit durchgehender Umrechnung auf die Rate des Geräts
   * (audio-core.js): Der Puffer wächst bei Aussetzern im Netz und schrumpft
   * langsam wieder, Taktdrift wird über eine winzige Tempoanpassung
   * ausgeglichen. Es spielt immer der Ton des groß gezeigten Bildschirms.
   * ==================================================================== */

  const WORKLET = `const audioCore = ${audioCore.toString()};
class MssPlayer extends AudioWorkletProcessor {
  constructor() {
    super();
    this.core = audioCore(sampleRate, 0.08);
    this.n = 0;
    this.spare = new Float32Array(128);
    this.port.onmessage = (e) => {
      const m = e.data;
      if (m.l) this.core.push(m.l, m.r);
      else if (m.silence) this.core.pushSilence(m.silence);
      else if (m.rate) this.core.setRate(m.rate);
      else if (m.reset) this.core.reset();
    };
  }
  process(inputs, outputs) {
    const out = outputs[0];
    if (this.spare.length !== out[0].length) this.spare = new Float32Array(out[0].length);
    this.core.pull(out[0], out[1] || this.spare);
    this.n += out[0].length;
    if (this.n >= sampleRate / 2) { this.n = 0; this.port.postMessage(this.core.stats()); }
    return true;
  }
}
registerProcessor('mss-player', MssPlayer);`;

  const audio = {
    ctx: null, gain: null, node: null, core: null, ready: false, starting: false,
    rate: 48000, enabled: false, muted: store.get('mss-muted', '0') === '1',
    volume: Number(store.get('mss-volume', '100')) / 100, peak: 0, stats: null,
  };

  // start muss aus einer Nutzeraktion heraus laufen (Klick, Taste) –
  // Browser lassen Ton sonst nicht zu.
  function audioStart() {
    if (audio.ctx) {
      if (audio.ctx.state !== 'running') audio.ctx.resume().then(updateAudioUI).catch(() => {});
      return;
    }
    if (audio.starting) return;
    audio.starting = true;
    let ctx;
    try {
      ctx = new AudioContext({ latencyHint: 'interactive' });
    } catch (e) {
      audio.starting = false;
      return;
    }
    audio.ctx = ctx;
    audio.gain = ctx.createGain();
    audio.gain.gain.value = 0; // erst nach dem Rückkopplungs-Check lauter
    audio.gain.connect(ctx.destination);
    applyVolume();
    ctx.onstatechange = updateAudioUI;
    ctx.resume().catch(() => {});
    setupPlayer(ctx).finally(() => { audio.starting = false; updateAudioUI(); });
  }

  async function setupPlayer(ctx) {
    if (ctx.audioWorklet && window.isSecureContext) {
      try {
        const url = URL.createObjectURL(new Blob([WORKLET], { type: 'application/javascript' }));
        await ctx.audioWorklet.addModule(url);
        URL.revokeObjectURL(url);
        const node = new AudioWorkletNode(ctx, 'mss-player', { numberOfInputs: 0, outputChannelCount: [2] });
        node.port.onmessage = (e) => { audio.stats = e.data; };
        node.port.postMessage({ rate: audio.rate });
        node.connect(audio.gain);
        audio.node = node;
        audio.ready = true;
        return;
      } catch (e) { /* weiter mit dem Notfallweg */ }
    }
    // Ohne sicheren Kontext (http im LAN) gibt es kein AudioWorklet.
    audio.core = audioCore(ctx.sampleRate, 0.15);
    audio.core.setRate(audio.rate);
    const sp = ctx.createScriptProcessor(2048, 0, 2);
    sp.onaudioprocess = (e) => {
      audio.core.pull(e.outputBuffer.getChannelData(0), e.outputBuffer.getChannelData(1));
    };
    sp.connect(audio.gain);
    audio.node = sp;
    audio.ready = true;
  }

  function audioSend(msg, transfer) {
    if (!audio.ready) return;
    if (audio.core) {
      if (msg.l) audio.core.push(msg.l, msg.r);
      else if (msg.silence) audio.core.pushSilence(msg.silence);
      else if (msg.rate) audio.core.setRate(msg.rate);
      else if (msg.reset) audio.core.reset();
    } else {
      audio.node.port.postMessage(msg, transfer || []);
    }
  }

  function onPCM(bytes) {
    if (!audio.ready) return;
    const frames = bytes.byteLength >> 2;
    if (frames <= 0) return;
    const dv = new DataView(bytes.buffer, bytes.byteOffset, frames * 4);
    const l = new Float32Array(frames), r = new Float32Array(frames);
    let peak = audio.peak;
    for (let i = 0; i < frames; i++) {
      const a = dv.getInt16(i * 4, true) / 32768;
      const b = dv.getInt16(i * 4 + 2, true) / 32768;
      l[i] = a;
      r[i] = b;
      const m = Math.max(a < 0 ? -a : a, b < 0 ? -b : b);
      if (m > peak) peak = m;
    }
    audio.peak = peak;
    audioSend({ l, r }, [l.buffer, r.buffer]);
  }

  function onSilence() {
    audioSend({ silence: Math.round(audio.rate / 50) });
  }

  // Tonrate und -zustand der groß gezeigten Quelle übernehmen.
  function applySourceAudio(src) {
    audioSend({ reset: true });
    const m = src.meta || {};
    if (m.rate && m.rate !== audio.rate) {
      audio.rate = m.rate;
      audioSend({ rate: m.rate });
    }
    audio.enabled = !!m.audio;
    updateAudioUI();
  }

  /* Rückkopplungsschutz: Schaut man auf dem PC zu, der gerade streamt,
   * nimmt dessen Systemton die eigene Wiedergabe wieder auf – das schaukelt
   * sich in Sekunden ohrenbetäubend laut hoch. Darum bleibt der Ton aus, bis
   * geklärt ist, dass hier nicht gestreamt wird. Der Gastgeber in seiner
   * Konferenz bekommt sein eigenes Bild gar nicht erst, da passt es. */
  const selfGuard = { pending: !hostKey, self: params.get('self') === '1' };

  function silenced() { return audio.muted || selfGuard.pending || selfGuard.self; }

  let localApp = { base: '', platform: '' }; // die App auf diesem PC, falls sie läuft

  async function checkSelf() {
    selfGuard.pending = !hostKey;
    applyVolume();
    const ask = (port) => fetch(`http://127.0.0.1:${port}/api/hello?room=${encodeURIComponent(room)}`,
      { cache: 'no-store', signal: AbortSignal.timeout(1500) })
      .then((r) => r.json().then((d) => {
        localApp = { base: `http://127.0.0.1:${port}`, platform: d.platform || '' };
        return d.self ? 'self' : 'app';
      }, () => 'other'), () => 'none');
    // Die App nimmt den ersten freien Port ab 7777 und läuft nur einmal. Also
    // der Reihe nach fragen und bei der ersten Antwort (oder dem ersten
    // geschlossenen Port) aufhören – so landen keine Fehlerzeilen für leere
    // Ports in der Konsole.
    let self = false;
    for (let p = 7777; p < 7782; p++) {
      const r = await ask(p);
      if (r === 'self') self = true;
      if (r !== 'other') break; // fremdes Programm auf dem Port: weiter suchen
    }
    selfGuard.self = selfGuard.self || (self && !hostKey);
    selfGuard.pending = false;
    applyVolume();
    updateAudioUI();
    updateShareButton();
    maybeAutoPresent();
    if (selfGuard.self) toast('Du schaust auf dem PC zu, der gerade streamt – Ton bleibt aus, sonst gibt es eine laute Rückkopplung.', 6000);
  }

  // Ton trotz eigenem PC einschalten nur nach ausdrücklicher Warnung.
  function allowUnmute() {
    if (!selfGuard.self) return true;
    if (!confirm('Achtung: Dieser PC streamt gerade selbst seinen Ton.\n\nWenn du hier den Ton einschaltest, nimmt der Stream ihn wieder auf – das wird in Sekunden extrem laut (Rückkopplung).\n\nTrotzdem einschalten?')) return false;
    selfGuard.self = false;
    return true;
  }

  function applyVolume() {
    if (!audio.gain) return;
    const v = silenced() ? 0 : audio.volume * audio.volume; // gehörrichtiger
    audio.gain.gain.setTargetAtTime(v, audio.gain.context.currentTime, 0.02);
  }

  function updateAudioUI() {
    const off = silenced() || audio.volume === 0;
    $('#btn-mute .ico-on').classList.toggle('hidden', off);
    $('#btn-mute .ico-off').classList.toggle('hidden', !off);
    $('#volume').value = String(Math.round(audio.volume * 100));
    const blocked = audio.enabled && (!audio.ctx || audio.ctx.state !== 'running');
    $('#audio-hint').classList.toggle('hidden', !(blocked && !silenced() && state === 'live'));
    $('#btn-mute').classList.toggle('attention', blocked && !audio.muted);
  }

  /* ======================================================================
   * Quellen: ein Objekt pro geteiltem Bildschirm
   * ==================================================================== */

  const sources = new Map();
  let focusedId = 0;

  function makeSource(id, name) {
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d', { alpha: false, desynchronized: true });
    const tile = document.createElement('div');
    tile.className = 'tile';
    const label = document.createElement('span');
    label.className = 'label';
    tile.append(label);
    tile.addEventListener('click', () => focus(id));
    canvas.addEventListener('dblclick', toggleFullscreen);
    canvas.addEventListener('pointerdown', (e) => onPointerDown(e, id));
    const src = {
      id, name, canvas, ctx, tile, label,
      asm: { seq: -1, parts: [], got: 0 },
      vasm: { seq: -1, parts: [], got: 0, key: false },
      vdec: { dec: null, codec: '', waitKey: true, ts: 0 },
      decoding: false, pendingBlob: null,
      hasFrame: false, w: 0, h: 0, paused: false, meta: {},
    };
    sources.set(id, src);
    return src;
  }

  function source(id) { return sources.get(id) || makeSource(id, id === 0 ? '' : 'Teilnehmer'); }
  function focused() { return source(focusedId); }

  function displayName(src) {
    if (src.id === 0) return (src.meta && src.meta.name) || room;
    return src.name;
  }

  function dropSource(id) {
    const src = sources.get(id);
    if (!src || id === 0) return;
    if (src.vdec.dec && src.vdec.dec.state !== 'closed') src.vdec.dec.close();
    src.canvas.remove();
    src.tile.remove();
    sources.delete(id);
    if (focusedId === id) {
      const next = [...sources.values()].find((s) => s.hasFrame) || source(0);
      focus(next.id);
    } else {
      layout();
    }
  }

  // Groß zeigen: Leinwand in die Bühne, alle anderen als Kacheln.
  function focus(id) {
    if (!sources.has(id)) return;
    const changed = focusedId !== id;
    focusedId = id;
    if (changed) applySourceAudio(focused());
    layout();
    syncState();
    renderTitle();
  }

  function layout() {
    const wrap = $('#screen-wrap');
    const tiles = $('#tiles');
    let shown = 0;
    for (const src of sources.values()) {
      if (src.id === focusedId) {
        if (src.canvas.parentNode !== wrap) wrap.appendChild(src.canvas);
        src.tile.remove();
        continue;
      }
      src.label.textContent = displayName(src);
      if (src.hasFrame) {
        if (src.canvas.parentNode !== src.tile) src.tile.prepend(src.canvas);
        if (src.tile.parentNode !== tiles) tiles.appendChild(src.tile);
        shown++;
      } else {
        src.tile.remove();
      }
    }
    tiles.classList.toggle('hidden', shown === 0);
  }

  // Gesamtzustand aus der groß gezeigten Quelle ableiten.
  function syncState() {
    if (!['live', 'waiting', 'paused'].includes(state)) return;
    const src = focused();
    if (src.paused && src.hasFrame) setState('paused');
    else if (src.hasFrame) setState('live');
    else setState(src.paused ? 'paused' : 'waiting');
  }

  /* ---------- Bild ---------- */

  function spsCodec(au) {
    for (let i = 0; i + 7 < au.length; i++) {
      if (au[i] === 0 && au[i + 1] === 0 && au[i + 2] === 1 && (au[i + 3] & 0x1f) === 7) {
        const h = (b) => b.toString(16).padStart(2, '0');
        return `avc1.${h(au[i + 4])}${h(au[i + 5])}${h(au[i + 6])}`;
      }
    }
    return '';
  }

  /* H.264 über WebCodecs: Die Grafikkarte des Zuschauers dekodiert, das Bild
   * landet ohne Umweg im Canvas. Geht nur in sicheren Kontexten (https,
   * localhost) – darum ist der Internet-Link der Normalfall. */
  let videoUnsupported = false;
  function onVideoAU(src, au, key) {
    if (!('VideoDecoder' in window)) {
      if (!videoUnsupported) {
        videoUnsupported = true;
        lastError = 'Dieser Browser kann das Video hier nicht abspielen. Öffne den Link über https (Streamer: Verbindung „Überall“) oder bitte den Streamer um den Kompatibilitätsmodus.';
        setState('error');
      }
      return;
    }
    const vd = src.vdec;
    if (key) {
      const codec = spsCodec(au) || (src.meta && src.meta.codec) || '';
      if (codec && (codec !== vd.codec || !vd.dec || vd.dec.state === 'closed')) {
        try {
          if (vd.dec && vd.dec.state !== 'closed') vd.dec.close();
          vd.dec = new VideoDecoder({
            output: (frame) => drawFrame(src, frame),
            error: () => { vd.waitKey = true; vd.codec = ''; },
          });
          vd.dec.configure({ codec, optimizeForLatency: true });
          vd.codec = codec;
          vd.waitKey = true;
        } catch (e) { return; }
      }
    }
    if (!vd.dec || vd.dec.state !== 'configured') return;
    if (vd.waitKey) {
      if (!key) return;
      vd.waitKey = false;
    }
    // Kommt der Decoder nicht hinterher, lieber zum nächsten Keyframe springen.
    if (!key && vd.dec.decodeQueueSize > 8) {
      vd.waitKey = true;
      return;
    }
    vd.ts += 16667;
    try {
      vd.dec.decode(new EncodedVideoChunk({ type: key ? 'key' : 'delta', timestamp: vd.ts, data: au }));
    } catch (e) {
      vd.waitKey = true;
    }
  }

  // drawFrame malt ein Bild (VideoFrame oder ImageBitmap) in die Quelle.
  function drawFrame(src, img) {
    const w = img.displayWidth || img.width, h = img.displayHeight || img.height;
    if (src.canvas.width !== w || src.canvas.height !== h) {
      src.canvas.width = w;
      src.canvas.height = h;
    }
    src.w = w;
    src.h = h;
    src.ctx.drawImage(img, 0, 0);
    img.close();
    if (src.id === focusedId) framesDrawn++;
    if (!src.hasFrame) {
      src.hasFrame = true;
      if (!focused().hasFrame) focus(src.id);
      else {
        layout();
        if (src.id !== 0) toast(`${src.name} teilt jetzt – unten anklicken zum Großmachen`, 3500);
      }
      syncState();
    }
  }

  // Immer nur das neueste Bild dekodieren – liegt eins in der Warteschlange,
  // wird es durch das neue ersetzt.
  function showJPEG(src, blob) {
    if (src.decoding) { src.pendingBlob = blob; return; }
    src.decoding = true;
    createImageBitmap(blob).then((bmp) => drawFrame(src, bmp))
      .catch(() => { /* kaputtes Bild, egal */ })
      .finally(() => {
        src.decoding = false;
        if (src.pendingBlob) {
          const next = src.pendingBlob;
          src.pendingBlob = null;
          showJPEG(src, next);
        }
      });
  }

  function onBinary(buf) {
    bytesIn += buf.byteLength;
    const type = new DataView(buf).getUint8(0);
    if (type === 6) { voice.onPacket(buf); return; }
    if (type === 7) {
      if (buf.byteLength < 6) return;
      const id = new DataView(buf).getUint32(1, true);
      onSourcePacket(source(id), buf.slice(5));
      return;
    }
    onSourcePacket(source(0), buf);
  }

  function onSourcePacket(src, buf) {
    const v = new DataView(buf);
    const type = v.getUint8(0);
    if (type === 4 || type === 5) {
      if (buf.byteLength < 17) return;
      const seq = v.getUint32(1, true);
      const idx = v.getUint16(13, true);
      const count = v.getUint16(15, true);
      const asmKey = type === 4 ? 'asm' : 'vasm';
      let a = src[asmKey];
      if (seq !== a.seq) a = src[asmKey] = { seq, parts: new Array(count), got: 0, key: v.getUint16(9, true) === 1 };
      if (!a.parts[idx]) {
        a.parts[idx] = new Uint8Array(buf, 17);
        a.got++;
      }
      if (a.got !== count) return;
      src[asmKey] = { seq: -1, parts: [], got: 0, key: false };
      if (type === 4) {
        showJPEG(src, new Blob(a.parts, { type: 'image/jpeg' }));
      } else {
        const total = a.parts.reduce((n, p) => n + p.length, 0);
        const au = new Uint8Array(total);
        let off = 0;
        for (const p of a.parts) { au.set(p, off); off += p.length; }
        onVideoAU(src, au, a.key);
      }
    } else if (type === 1) {
      if (buf.byteLength <= 13) return;
      showJPEG(src, new Blob([new Uint8Array(buf, 13)], { type: 'image/jpeg' }));
    } else if (type === 2 || type === 3) {
      if (src.id !== focusedId) return; // es spielt der Ton des großen Bildschirms
      if (type === 2) onPCM(new Uint8Array(buf, 9));
      else onSilence();
    }
  }

  /* ======================================================================
   * Verbindung
   * ==================================================================== */

  // Ein Token pro Sitzung und Browser: Wer einmal freigegeben wurde, kommt
  // nach Abbrüchen und Stream-Neustarts ohne neues Anklopfen wieder rein.
  function tokenFor(roomName) {
    if (hostKey) return hostKey;
    const key = 'mss-token-' + roomName;
    let token = store.get(key, '');
    if (!token) {
      const bytes = new Uint8Array(16);
      crypto.getRandomValues(bytes);
      token = Array.from(bytes).map((b) => b.toString(16).padStart(2, '0')).join('');
      store.set(key, token);
    }
    return token;
  }

  function relayBase() {
    if (relayURL) return relayURL.replace(/\/+$/, '');
    return location.origin;
  }

  function connect() {
    cleanup();
    wantConnect = true;
    unanswered = 0;
    setState(state === 'offline' ? 'offline' : 'connecting');
    const base = relayBase().replace(/^http/, 'ws');
    try {
      ws = new WebSocket(`${base}/ws`);
    } catch (e) {
      scheduleReconnect();
      return;
    }
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => {
      ws.send(JSON.stringify({ t: 'join', room, role: 'viewer', name: myName || 'Gast', token: tokenFor(room) }));
    };
    ws.onmessage = (ev) => {
      unanswered = 0; // jedes Paket zeigt: Die Leitung lebt
      if (typeof ev.data === 'string') {
        let m;
        try { m = JSON.parse(ev.data); } catch (e) { return; }
        onControl(m);
      } else {
        onBinary(ev.data);
      }
    };
    ws.onclose = () => {
      ws = null;
      if (!wantConnect) return;
      if (state !== 'offline' && state !== 'denied' && state !== 'error') setState('reconnecting');
      scheduleReconnect();
    };
  }

  function cleanup() {
    clearTimeout(reconnectTimer);
    if (ws) {
      ws.onopen = ws.onmessage = ws.onclose = null;
      try { ws.close(); } catch (e) { /* egal */ }
      ws = null;
    }
  }

  function scheduleReconnect() {
    clearTimeout(reconnectTimer);
    const delay = state === 'offline' ? 4000 : reconnectDelay;
    reconnectTimer = setTimeout(() => { if (wantConnect) connect(); }, delay);
    reconnectDelay = Math.min(reconnectDelay * 2, 8000);
  }

  setInterval(() => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      lastPing = performance.now();
      if (!unanswered) unanswered = lastPing;
      ws.send(JSON.stringify({ t: 'ping', ts: Date.now() }));
    }
  }, 3000);

  function send(o) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(o));
  }

  function onControl(m) {
    switch (m.t) {
      case 'joined':
        reconnectDelay = 1000;
        setState('approval');
        break;
      case 'allowed':
        setState('waiting');
        syncState();
        maybeAutoVoice();
        maybeAutoPresent();
        break;
      case 'denied':
        wantConnect = false;
        setState('denied');
        break;
      case 'meta': {
        const src = source(m.src || 0);
        src.meta = m;
        if (m.src) src.name = m.name || src.name;
        if (src.id === focusedId) applySourceAudio(src);
        layout();
        renderTitle();
        updatePointHint();
        break;
      }
      case 'pause': {
        const src = source(m.src || 0);
        src.paused = !!m.on;
        if (src.id === focusedId) {
          audioSend({ reset: true });
          syncState();
        }
        break;
      }
      case 'sources': {
        const ids = new Set((m.members || []).map((x) => x.id));
        for (const x of m.members || []) source(x.id).name = x.name;
        for (const id of [...sources.keys()]) if (id !== 0 && !ids.has(id)) dropSource(id);
        layout();
        break;
      }
      case 'source-gone':
        dropSource(m.src);
        break;
      case 'voice':
      case 'voice-you':
        voice.onControl(m);
        $('#btn-voice').classList.toggle('on', voice.state.joined);
        break;
      case 'viewers':
        viewers = m.n || 0;
        renderTitle();
        break;
      case 'pong':
        rtt = Math.max(0, performance.now() - lastPing);
        break;
      case 'reconnecting':
        setState('reconnecting');
        break;
      case 'waiting':
        setState('approval');
        break;
      case 'live':
        setState('waiting');
        syncState();
        break;
      case 'ended':
        for (const src of sources.values()) src.hasFrame = false;
        setState('offline');
        break;
      case 'error': {
        const msg = m.msg || 'Unbekannter Fehler';
        if (/nicht gefunden/i.test(msg)) {
          setState('offline');
          findElsewhere();
        } else {
          lastError = msg;
          wantConnect = /voll|Anfragen/i.test(msg);
          setState('error');
        }
        break;
      }
    }
  }

  /* Sitzungs-ID nachschlagen: Läuft die Sitzung nicht auf diesem Server,
   * hat ihre App vielleicht eine neue Adresse veröffentlicht (ntfy.sh,
   * Kanal aus der ID abgeleitet – wie in internal/rendezvous). */
  let lookedUp = false;
  async function topicFor(id) {
    const data = new TextEncoder().encode('mss-share:' + id);
    const hash = new Uint8Array(await crypto.subtle.digest('SHA-256', data));
    return 'mss-' + Array.from(hash).map((b) => b.toString(16).padStart(2, '0')).join('').slice(0, 24);
  }

  async function findElsewhere() {
    if (lookedUp || relayURL || !window.crypto || !crypto.subtle) return;
    lookedUp = true;
    try {
      const res = await fetch(`https://ntfy.sh/${await topicFor(room)}/json?poll=1&since=12h`, { signal: AbortSignal.timeout(8000) });
      let url = '';
      for (const line of (await res.text()).split('\n')) {
        try {
          const ev = JSON.parse(line);
          if (ev.event === 'message') url = JSON.parse(ev.message).url || url;
        } catch (e) { /* leere Zeile */ }
      }
      if (url && url.startsWith('https://') && new URL(url).origin !== location.origin) {
        const next = new URL(url);
        next.search = location.search;
        toast('Sitzung gefunden – wechsle zur aktuellen Adresse…', 3000);
        setTimeout(() => { location.href = next.href; }, 800);
      }
    } catch (e) { /* nicht gefunden, weiter warten */ }
  }

  /* ======================================================================
   * Zeigen per 5-fach-Klick
   *
   * Fünf schnelle Klicks auf dieselbe Stelle schicken dem, dessen Bildschirm
   * groß zu sehen ist, eine Markierung – nur wenn er das erlaubt hat. Einzelne
   * Klicks tun nichts, damit es beim normalen Zuschauen nicht stört.
   * ==================================================================== */

  const clicks = [];
  function onPointerDown(e, id) {
    if (id !== focusedId || e.button !== 0) return;
    const src = focused();
    if (!src.meta || !src.meta.point || !src.w) return;
    const now = performance.now();
    while (clicks.length && now - clicks[0].t > 1500) clicks.shift();
    if (clicks.length && Math.hypot(e.clientX - clicks[0].x, e.clientY - clicks[0].y) > 40) clicks.length = 0;
    clicks.push({ t: now, x: e.clientX, y: e.clientY });
    ripple(e.clientX, e.clientY, false);
    if (clicks.length < 5) return;
    clicks.length = 0;
    const pos = imagePosition(src.canvas, e.clientX, e.clientY);
    if (!pos) return;
    send({ t: 'point', src: id, x: pos.x, y: pos.y });
    ripple(e.clientX, e.clientY, true);
    toast(`${displayName(src)} sieht jetzt, wo du hinzeigst`);
  }

  // imagePosition rechnet den Klick in die Bildkoordinaten 0..1 um – auch
  // wenn das Bild eingepasst ist und Ränder hat.
  function imagePosition(canvas, cx, cy) {
    const r = canvas.getBoundingClientRect();
    const fit = getComputedStyle(canvas).objectFit;
    let dw = r.width, dh = r.height;
    if (fit === 'contain') {
      const aspect = canvas.width / canvas.height;
      if (r.width / r.height > aspect) { dh = r.height; dw = dh * aspect; } else { dw = r.width; dh = dw / aspect; }
    } else if (fit === 'none') {
      dw = canvas.width; dh = canvas.height;
    }
    const ox = r.left + (r.width - dw) / 2, oy = r.top + (r.height - dh) / 2;
    const x = (cx - ox) / dw, y = (cy - oy) / dh;
    if (x < 0 || x > 1 || y < 0 || y > 1) return null;
    return { x, y };
  }

  function ripple(x, y, big) {
    const el = document.createElement('div');
    el.className = 'ripple' + (big ? ' big' : '');
    el.style.left = x + 'px';
    el.style.top = y + 'px';
    document.body.appendChild(el);
    setTimeout(() => el.remove(), 1300);
  }

  function updatePointHint() {
    const src = focused();
    const on = !!(src.meta && src.meta.point) && state === 'live';
    $('#point-hint').classList.toggle('hidden', !on);
  }

  /* ======================================================================
   * Oberfläche
   * ==================================================================== */

  const STATES = {
    connecting: { badge: ['wait', 'Verbinde'], text: 'Verbinde…', spin: true },
    approval: { badge: ['wait', 'Wartet'], text: 'Warte auf Freigabe…', fine: 'Der andere bekommt eine Meldung und lässt dich mit einem Klick rein.', spin: true },
    waiting: { badge: ['wait', 'Gleich'], text: 'Gleich geht es los…', fine: hostKey ? 'Hier erscheinen die Bildschirme der anderen, sobald sie teilen.' : '', spin: true },
    live: { badge: ['', 'Live'] },
    paused: { badge: ['off', 'Pause'], text: 'Pausiert', fine: 'Das Teilen ist kurz angehalten. Es geht automatisch weiter.' },
    reconnecting: { badge: ['wait', 'Verbinde'], text: 'Verbindung unterbrochen…', fine: 'Wird automatisch wiederhergestellt.', spin: true },
    offline: { badge: ['off', 'Offline'], text: 'Gerade wird nichts geteilt', fine: 'Sobald der andere wieder teilt, bist du automatisch dabei – einfach offen lassen.', spin: true },
    denied: { badge: ['off', 'Abgelehnt'], text: 'Der andere hat die Anfrage abgelehnt.', btn: 'Erneut anfragen' },
    error: { badge: ['off', 'Fehler'], btn: 'Neu versuchen' },
  };

  function setState(next) {
    state = next;
    const s = STATES[next] || STATES.connecting;
    const badge = $('#badge');
    badge.className = 'badge ' + s.badge[0];
    badge.textContent = s.badge[1];
    const ov = $('#overlay-msg');
    ov.classList.toggle('hidden', next === 'live');
    ov.querySelector('.spinner').classList.toggle('hidden', !s.spin);
    $('#overlay-text').textContent = next === 'error' ? lastError : (s.text || '');
    $('#overlay-fine').textContent = s.fine || '';
    const btn = $('#overlay-btn');
    btn.classList.toggle('hidden', !s.btn);
    if (s.btn) btn.textContent = s.btn;
    // Bei Pause bleibt das letzte Bild gedimmt sichtbar.
    ov.style.background = next === 'paused' && focused().hasFrame ? 'rgba(17,18,20,.6)' : '';
    if (next !== 'live') document.body.classList.remove('idle');
    renderTitle();
    updateAudioUI();
    updatePointHint();
    updateShareButton();
  }

  function renderTitle() {
    const src = focused();
    const who = displayName(src) || room;
    $('#title').textContent = who;
    const bits = [];
    if (src.meta && src.meta.title) bits.push(src.meta.title);
    if (viewers > 1) bits.push(`${viewers} schauen zu`);
    $('#sub').textContent = bits.join(' · ');
    document.title = (state === 'live' ? '● ' : '') + who + ' – Zuschauen';
  }

  function fmtRate(k) {
    return k >= 1000 ? `${(k / 1000).toFixed(1).replace('.', ',')} Mbit/s` : `${k} kbit/s`;
  }

  setInterval(() => {
    fps = framesDrawn;
    kbps = Math.round(bytesIn * 8 / 1000);
    framesDrawn = 0;
    bytesIn = 0;

    if (audio.core) audio.stats = audio.core.stats();
    if (!$('#stats-panel').classList.contains('hidden')) {
      const src = focused();
      const kind = src.meta && src.meta.video === 'h264' ? 'H.264' : 'JPEG';
      $('#stat-res').textContent = src.w ? `${src.w}×${src.h} · ${kind}` : '–';
      $('#stat-fps').textContent = String(fps);
      $('#stat-rate').textContent = fmtRate(kbps);
      $('#stat-ping').textContent = rtt ? `${Math.round(rtt)} ms` : '–';
      const st = audio.stats;
      $('#stat-buffer').textContent = !audio.enabled ? 'kein Ton' : (st
        ? `${Math.round(st.level * 1000)} / ${Math.round(st.target * 1000)} ms${st.underruns ? ` · ${st.underruns}× nachgeladen` : ''}`
        : '–');
      $('#stat-audio').textContent = audio.enabled ? `${Math.round(audio.peak * 100)} %` : '–';
      $('#stat-viewers').textContent = String(viewers);
    }
    audio.peak = 0;

    // Tot ist die Leitung erst, wenn auf einen gesendeten Ping 12 s lang gar
    // nichts mehr kommt. Nicht einfach "letztes Pong ist alt": Im
    // Hintergrund-Tab laufen Timer nur noch einmal pro Minute – dann wäre
    // jedes Pong alt, und der Viewer flog minütlich raus.
    if (ws && ws.readyState === WebSocket.OPEN && unanswered && performance.now() - unanswered > 12000) {
      setState('reconnecting');
      cleanup();
      scheduleReconnect();
    }
  }, 1000);

  /* ---------- Bedienung ---------- */

  function toggleFullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else $('#stage').requestFullscreen().catch(() => {});
  }

  // Bild-in-Bild zeigt den groß gezeigten Bildschirm.
  let pipVideo = null, pipSource = -1;
  async function togglePiP() {
    try {
      if (document.pictureInPictureElement) {
        await document.exitPictureInPicture();
        return;
      }
      const src = focused();
      if (!pipVideo || pipSource !== src.id) {
        pipVideo = document.createElement('video');
        pipVideo.muted = true;
        pipVideo.playsInline = true;
        pipVideo.srcObject = src.canvas.captureStream();
        pipSource = src.id;
        pipVideo.addEventListener('leavepictureinpicture', () => $('#btn-pip').classList.remove('on'));
        pipVideo.addEventListener('enterpictureinpicture', () => $('#btn-pip').classList.add('on'));
      }
      // Einmal neu malen, damit der Videostrom sofort ein Bild hat.
      if (src.canvas.width) src.ctx.drawImage(src.canvas, 0, 0);
      await pipVideo.play();
      await pipVideo.requestPictureInPicture();
    } catch (e) {
      toast('Bild-in-Bild geht in diesem Browser nicht');
    }
  }
  if (!document.pictureInPictureEnabled || !HTMLCanvasElement.prototype.captureStream) $('#btn-pip').classList.add('hidden');

  function toggleFit() {
    const native = $('#screen-wrap').classList.toggle('native');
    $('#btn-fit').classList.toggle('on', native);
    store.set('mss-native', native ? '1' : '0');
    toast(native ? 'Originalgröße (1:1)' : 'Eingepasst');
  }

  function toggleStats() {
    const hidden = $('#stats-panel').classList.toggle('hidden');
    $('#btn-stats').classList.toggle('on', !hidden);
    store.set('mss-stats', hidden ? '0' : '1');
  }

  function setVolume(v) {
    audio.volume = Math.max(0, Math.min(1, v));
    if (audio.volume > 0 && silenced() && !allowUnmute()) { updateAudioUI(); return; }
    if (audio.volume > 0) audio.muted = false;
    store.set('mss-volume', String(Math.round(audio.volume * 100)));
    store.set('mss-muted', audio.muted ? '1' : '0');
    applyVolume();
    updateAudioUI();
  }

  function toggleMute() {
    audioStart();
    if (silenced()) {
      if (!allowUnmute()) return;
      audio.muted = false;
    } else {
      audio.muted = true;
    }
    if (!audio.muted && audio.volume === 0) audio.volume = 0.5;
    store.set('mss-muted', audio.muted ? '1' : '0');
    applyVolume();
    updateAudioUI();
  }

  let toastTimer = null;
  function toast(text, ms) {
    let el = $('.toast');
    if (!el) {
      el = document.createElement('div');
      el.className = 'toast';
      document.body.appendChild(el);
    }
    el.textContent = text;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.remove(), ms || 1800);
  }

  // Leiste blendet sich aus, wenn die Maus ruht – das Bild gehört dem Stream.
  let idleTimer = null;
  function wake() {
    document.body.classList.remove('idle');
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      if (state === 'live' && !$('.topbar').matches(':hover')) document.body.classList.add('idle');
    }, 2500);
  }
  document.addEventListener('mousemove', wake);
  document.addEventListener('touchstart', wake, { passive: true });

  // Jede Interaktion darf den Ton freischalten.
  document.addEventListener('pointerdown', audioStart, true);
  document.addEventListener('keydown', audioStart, true);

  $('#btn-mute').addEventListener('click', toggleMute);
  $('#volume').addEventListener('input', (e) => { audioStart(); setVolume(Number(e.target.value) / 100); });
  $('#audio-hint').addEventListener('click', () => {
    audioStart();
    if (!allowUnmute()) return;
    audio.muted = false;
    applyVolume();
    updateAudioUI();
  });
  $('#btn-fullscreen').addEventListener('click', toggleFullscreen);
  $('#btn-pip').addEventListener('click', togglePiP);
  $('#btn-fit').addEventListener('click', toggleFit);
  $('#btn-stats').addEventListener('click', toggleStats);

  $('#overlay-btn').addEventListener('click', () => {
    reconnectDelay = 1000;
    connect();
  });

  /* ---------- Eigenen Bildschirm in die Konferenz teilen ---------- */

  // Teilt die App auf diesem PC. Die Seite fragt nur – die App zeigt die
  // Rückfrage in ihrem eigenen Fenster, erst dann wird gestreamt.
  function updateShareButton() {
    const can = !!localApp.base && !hostKey && ['live', 'waiting', 'paused'].includes(state);
    $('#btn-share').classList.toggle('hidden', !can);
  }

  // Konferenz-Fenster der App mit ?present=<Code>: gleich teilen, ohne
  // Rückfrage (die App kennt den Code, nur sie hat das Fenster geöffnet).
  let autoPresentDone = false;
  function maybeAutoPresent() {
    const nonce = params.get('present');
    if (autoPresentDone || !nonce || !localApp.base || !['live', 'waiting', 'paused'].includes(state)) return;
    autoPresentDone = true;
    fetch(localApp.base + '/api/present', {
      method: 'POST',
      body: JSON.stringify({ server: relayBase(), room, token: tokenFor(room), host: displayName(source(0)), nonce }),
    }).then(() => toast('Dein Bildschirm wird jetzt geteilt', 3000)).catch(() => { autoPresentDone = false; });
  }

  $('#btn-share').addEventListener('click', async () => {
    try {
      const res = await fetch(localApp.base + '/api/present', {
        method: 'POST',
        body: JSON.stringify({ server: relayBase(), room, token: tokenFor(room), host: displayName(source(0)) }),
      });
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || 'App antwortet nicht');
      toast('Bestätige im Fenster deiner App, was du teilen willst', 5000);
    } catch (e) {
      toast('Teilen ging nicht: ' + e.message, 5000);
    }
  });

  /* ---------- Sprachkanal ---------- */

  const overlay = MssOverlay.create({ base: () => localApp.base || null, platform: () => localApp.platform });
  const voice = MssVoice.create({
    send: (b) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(b); },
    sendCtl: send,
    canJoin: () => !selfGuard.self && !!ws && ws.readyState === WebSocket.OPEN && ['live', 'waiting', 'paused'].includes(state),
    joinHint: () => (selfGuard.self
      ? 'Du schaust auf dem PC zu, der streamt – öffne im Panel „Konferenz öffnen“, dort geht der Sprachkanal ohne Rückkopplung.'
      : 'Geht, sobald der Streamer dich reingelassen hat.'),
    onChange: (v) => { overlay.publish(v.snapshot()); $('#btn-voice').classList.toggle('on', v.state.joined); },
    onOverlay: (v) => overlay.toggle(v.snapshot()),
  });
  voice.mount($('#voice'));
  window.mssVoice = voice; // zum Nachsehen in der Browser-Konsole
  $('#btn-voice').addEventListener('click', () => {
    $('#voice-drawer').classList.toggle('hidden');
    voice.refresh();
  });

  // Automatisch in den Sprachkanal (Konferenz-Fenster der App, ?voice=1).
  let autoVoiceDone = false;
  function maybeAutoVoice() {
    if (autoVoiceDone || params.get('voice') !== '1') return;
    autoVoiceDone = true;
    if (params.get('muted') === '1') voice.setMuted(true);
    voice.join();
  }

  $('#btn-leave').addEventListener('click', () => {
    voice.leave();
    wantConnect = false;
    cleanup();
    audioSend({ reset: true });
    if (document.fullscreenElement) document.exitFullscreen();
    if (document.pictureInPictureElement) document.exitPictureInPicture();
    for (const id of [...sources.keys()]) dropSource(id);
    source(0).hasFrame = false;
    showJoin();
  });

  window.addEventListener('keydown', (ev) => {
    if (ev.target.matches('input') && ev.target.type !== 'range') return;
    if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
    if ($('#stage').classList.contains('hidden')) return;
    switch (ev.key.toLowerCase()) {
      case 'f': toggleFullscreen(); break;
      case 'm': toggleMute(); break;
      case 'p': togglePiP(); break;
      case 'i': toggleStats(); break;
      case 'o': toggleFit(); break;
      case 'arrowup': setVolume(audio.volume + 0.05); toast(`Lautstärke ${Math.round(audio.volume * 100)} %`); break;
      case 'arrowdown': setVolume(audio.volume - 0.05); toast(`Lautstärke ${Math.round(audio.volume * 100)} %`); break;
      default: return;
    }
    ev.preventDefault();
  });

  /* ---------- Beitreten ---------- */

  function showJoin() {
    $('#stage').classList.add('hidden');
    $('#join').classList.remove('hidden');
    const known = !!room;
    $('#join-title').textContent = known ? 'Zuschauen' : 'Bildschirm ansehen';
    $('#join-session').textContent = room;
    $('#join-session').classList.toggle('hidden', !known);
    $('#join-text').textContent = known
      ? 'Wie heißt du? Der andere sieht deinen Namen und lässt dich rein.'
      : 'Gib die Sitzungs-ID oder den Link ein, den du bekommen hast.';
    $('#room-input').classList.toggle('hidden', known);
    $('#room-input').value = room;
    $('#name-input').value = myName;
    $('#join-error').classList.add('hidden');
    (known ? $('#name-input') : $('#room-input')).focus();
    if (known && myName) $('#join-card button').focus();
  }

  // Eingabe kann eine ID oder ein ganzer Link sein. Zeigt der Link auf einen
  // anderen Server, wechseln wir einfach dorthin.
  function parseRoom(value) {
    const v = value.trim();
    if (/^https?:\/\//i.test(v) || v.includes('room=')) {
      try {
        const u = new URL(v, location.href);
        const r = (u.searchParams.get('room') || '').toLowerCase();
        if (u.origin !== location.origin && r) {
          location.href = u.href;
          return null;
        }
        return r;
      } catch (e) { return ''; }
    }
    return v.toLowerCase();
  }

  function enter() {
    $('#join').classList.add('hidden');
    $('#stage').classList.remove('hidden');
    focusedId = 0;
    reconnectDelay = 1000;
    state = 'connecting';
    layout();
    checkSelf();
    connect();
    wake();
  }

  $('#join-card').addEventListener('submit', (ev) => {
    ev.preventDefault();
    audioStart(); // der Klick auf "Zuschauen" schaltet den Ton frei
    const r = $('#room-input').classList.contains('hidden') ? room : parseRoom($('#room-input').value);
    if (r === null) return;
    if (!/^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/.test(r)) {
      const err = $('#join-error');
      err.textContent = 'Das sieht nicht nach einer Sitzungs-ID aus.';
      err.classList.remove('hidden');
      return;
    }
    room = r;
    myName = $('#name-input').value.trim();
    store.set('mss-name', myName);
    const keep = new URLSearchParams(params);
    keep.set('room', room);
    history.replaceState(null, '', `?${keep}`);
    enter();
  });

  /* ---------- Start ---------- */

  if (store.get('mss-native', '0') === '1') {
    $('#screen-wrap').classList.add('native');
    $('#btn-fit').classList.add('on');
  }
  if (store.get('mss-stats', '0') === '1') {
    $('#stats-panel').classList.remove('hidden');
    $('#btn-stats').classList.add('on');
  }
  source(0);
  if (params.get('name')) myName = params.get('name');
  updateAudioUI();
  // Gastgeber und Konferenz-Fenster der App steigen ohne Namensabfrage ein.
  if (room && (hostKey || params.get('auto') === '1')) {
    if (hostKey && !myName) myName = 'Gastgeber';
    enter();
  } else {
    showJoin();
  }
})();
