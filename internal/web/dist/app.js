/* Viewer: WebSocket rein, Canvas und Lautsprecher raus.
 * Bild kommt als JPEG (in Stücken), Ton als PCM in 20-ms-Paketen. */
(() => {
  'use strict';

  const $ = (s) => document.querySelector(s);
  const canvas = $('#screen');
  const ctx2d = canvas.getContext('2d', { alpha: false, desynchronized: true });

  const store = {
    get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : v; } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* egal */ } },
  };

  let room = (new URLSearchParams(location.search).get('room') || '').trim().toLowerCase();
  let myName = store.get('mss-name', '');
  let ws = null;
  let wantConnect = false;
  let state = 'idle';
  let hasFrame = false;
  let hostPaused = false;
  let hostName = '', targetTitle = '';
  let viewers = 0;
  let frameW = 0, frameH = 0;
  let framesDrawn = 0, bytesIn = 0, fps = 0, kbps = 0, rtt = 0;
  let lastPing = 0, lastPong = 0;
  let reconnectTimer = null, reconnectDelay = 1000;
  let lastError = '';
  let videoKind = '–';

  /* ======================================================================
   * Ton
   *
   * Ein Ringpuffer mit durchgehender Umrechnung auf die Rate des Geräts.
   * Früher wurde jedes 20-ms-Paket einzeln umgerechnet und eingeplant – an
   * den Kanten knackte es, und Taktunterschiede zwischen den Soundkarten
   * führten zu Sprüngen. Jetzt:
   *  - der Puffer wächst bei Aussetzern im Netz und schrumpft langsam wieder,
   *  - Taktdrift wird über eine winzige Tempoanpassung (max. ±2 %)
   *    ausgeglichen, statt hörbar zu springen.
   * Läuft im AudioWorklet (eigener Thread), sonst im ScriptProcessor.
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

  /* Rückkopplungsschutz: Schaut man auf dem PC zu, der gerade streamt,
   * nimmt dessen Systemton die eigene Wiedergabe wieder auf – das schaukelt
   * sich in Sekunden ohrenbetäubend laut hoch. Darum bleibt der Ton aus, bis
   * geklärt ist, dass hier nicht gestreamt wird. */
  const selfGuard = { pending: true, self: new URLSearchParams(location.search).get('self') === '1' };

  function silenced() { return audio.muted || selfGuard.pending || selfGuard.self; }

  async function checkSelf() {
    selfGuard.pending = true;
    applyVolume();
    const ask = (port) => fetch(`http://127.0.0.1:${port}/api/hello?room=${encodeURIComponent(room)}`,
      { cache: 'no-store', signal: AbortSignal.timeout(1500) })
      .then((r) => r.json()).then((d) => !!d.self).catch(() => false);
    const ports = [];
    for (let p = 7777; p < 7782; p++) ports.push(ask(p));
    const self = (await Promise.all(ports)).some(Boolean);
    selfGuard.self = selfGuard.self || self;
    selfGuard.pending = false;
    applyVolume();
    updateAudioUI();
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
   * Verbindung
   * ==================================================================== */

  // Ein Token pro Sitzung und Browser: Wer einmal freigegeben wurde, kommt
  // nach Abbrüchen und Stream-Neustarts ohne neues Anklopfen wieder rein.
  function tokenFor(roomName) {
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

  function connect() {
    cleanup();
    wantConnect = true;
    lastPong = 0;
    setState(state === 'offline' ? 'offline' : 'connecting');
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    try {
      ws = new WebSocket(`${proto}//${location.host}/ws`);
    } catch (e) {
      scheduleReconnect();
      return;
    }
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => {
      ws.send(JSON.stringify({ t: 'join', room, role: 'viewer', name: myName || 'Gast', token: tokenFor(room) }));
    };
    ws.onmessage = (ev) => {
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
      ws.send(JSON.stringify({ t: 'ping', ts: Date.now() }));
    }
  }, 3000);

  function onControl(m) {
    switch (m.t) {
      case 'joined':
        reconnectDelay = 1000;
        setState('approval');
        break;
      case 'allowed':
        setState(hostPaused ? 'paused' : (hasFrame ? 'live' : 'waiting'));
        break;
      case 'denied':
        wantConnect = false;
        setState('denied');
        break;
      case 'meta':
        if (m.rate && m.rate !== audio.rate) {
          audio.rate = m.rate;
          audioSend({ rate: m.rate });
        }
        audio.enabled = !!m.audio;
        if (!audio.enabled) audioSend({ reset: true });
        hostName = m.name || hostName;
        vdec.meta = m.codec || '';
        videoKind = m.video === 'h264' ? 'H.264' : 'JPEG';
        targetTitle = m.title || targetTitle;
        renderTitle();
        updateAudioUI();
        break;
      case 'pause':
        hostPaused = !!m.on;
        audioSend({ reset: true });
        if (state === 'live' || state === 'waiting' || state === 'paused') {
          setState(hostPaused ? 'paused' : (hasFrame ? 'live' : 'waiting'));
        }
        break;
      case 'viewers':
        viewers = m.n || 0;
        renderTitle();
        break;
      case 'pong':
        rtt = Math.max(0, performance.now() - lastPing);
        lastPong = performance.now();
        break;
      case 'reconnecting':
        setState('reconnecting');
        break;
      case 'waiting':
        setState('approval');
        break;
      case 'live':
        setState(hostPaused ? 'paused' : (hasFrame ? 'live' : 'waiting'));
        break;
      case 'ended':
        hasFrame = false;
        setState('offline');
        break;
      case 'error': {
        const msg = m.msg || 'Unbekannter Fehler';
        if (/nicht gefunden/i.test(msg)) {
          setState('offline');
        } else {
          lastError = msg;
          wantConnect = /voll|Anfragen/i.test(msg);
          setState('error');
        }
        break;
      }
    }
  }

  /* ======================================================================
   * Bild
   * ==================================================================== */

  let asm = { seq: -1, parts: [], got: 0 };
  let vasm = { seq: -1, parts: [], got: 0, key: false };
  let decoding = false, pendingBlob = null;

  /* H.264 über WebCodecs: Die Grafikkarte des Zuschauers dekodiert, das Bild
   * landet ohne Umweg im Canvas. Geht nur in sicheren Kontexten (https,
   * localhost) – darum ist der Internet-Link der Normalfall. */
  const vdec = { dec: null, codec: '', waitKey: true, ts: 0, meta: '', unsupported: false };

  function spsCodec(au) {
    for (let i = 0; i + 7 < au.length; i++) {
      if (au[i] === 0 && au[i + 1] === 0 && au[i + 2] === 1 && (au[i + 3] & 0x1f) === 7) {
        const h = (b) => b.toString(16).padStart(2, '0');
        return `avc1.${h(au[i + 4])}${h(au[i + 5])}${h(au[i + 6])}`;
      }
    }
    return '';
  }

  function configureVideo(codec) {
    if (vdec.dec && vdec.dec.state !== 'closed') vdec.dec.close();
    vdec.dec = new VideoDecoder({
      output: onVideoFrame,
      error: () => { vdec.waitKey = true; vdec.codec = ''; },
    });
    vdec.dec.configure({ codec, optimizeForLatency: true });
    vdec.codec = codec;
    vdec.waitKey = true;
  }

  function onVideoAU(au, key) {
    if (!('VideoDecoder' in window)) {
      if (!vdec.unsupported) {
        vdec.unsupported = true;
        lastError = 'Dieser Browser kann das Video hier nicht abspielen. Öffne den Link über https (Streamer: Verbindung „Überall“) oder bitte den Streamer um den Kompatibilitätsmodus.';
        setState('error');
      }
      return;
    }
    if (key) {
      const codec = spsCodec(au) || vdec.meta;
      if (codec && (codec !== vdec.codec || !vdec.dec || vdec.dec.state === 'closed')) {
        try { configureVideo(codec); } catch (e) { return; }
      }
    }
    if (!vdec.dec || vdec.dec.state !== 'configured') return;
    if (vdec.waitKey) {
      if (!key) return;
      vdec.waitKey = false;
    }
    // Kommt der Decoder nicht hinterher, lieber zum nächsten Keyframe springen.
    if (!key && vdec.dec.decodeQueueSize > 8) {
      vdec.waitKey = true;
      return;
    }
    vdec.ts += 16667;
    try {
      vdec.dec.decode(new EncodedVideoChunk({ type: key ? 'key' : 'delta', timestamp: vdec.ts, data: au }));
    } catch (e) {
      vdec.waitKey = true;
    }
  }

  function onVideoFrame(frame) {
    if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
      canvas.width = frame.displayWidth;
      canvas.height = frame.displayHeight;
    }
    frameW = frame.displayWidth;
    frameH = frame.displayHeight;
    ctx2d.drawImage(frame, 0, 0);
    frame.close();
    framesDrawn++;
    if (!hasFrame) {
      hasFrame = true;
      if (state === 'waiting') setState('live');
    }
  }

  function onBinary(buf) {
    bytesIn += buf.byteLength;
    const v = new DataView(buf);
    const type = v.getUint8(0);
    if (type === 4) {
      if (buf.byteLength < 17) return;
      const seq = v.getUint32(1, true);
      const idx = v.getUint16(13, true);
      const count = v.getUint16(15, true);
      if (seq !== asm.seq) asm = { seq, parts: new Array(count), got: 0 };
      if (!asm.parts[idx]) {
        asm.parts[idx] = new Uint8Array(buf, 17);
        asm.got++;
      }
      if (asm.got === count) {
        frameW = v.getUint16(9, true);
        frameH = v.getUint16(11, true);
        showFrame(new Blob(asm.parts, { type: 'image/jpeg' }));
        asm = { seq: -1, parts: [], got: 0 };
      }
    } else if (type === 5) {
      if (buf.byteLength < 17) return;
      const seq = v.getUint32(1, true);
      const idx = v.getUint16(13, true);
      const count = v.getUint16(15, true);
      if (seq !== vasm.seq) vasm = { seq, parts: new Array(count), got: 0, key: v.getUint16(9, true) === 1 };
      if (!vasm.parts[idx]) {
        vasm.parts[idx] = new Uint8Array(buf, 17);
        vasm.got++;
      }
      if (vasm.got === count) {
        const total = vasm.parts.reduce((n, p) => n + p.length, 0);
        const au = new Uint8Array(total);
        let off = 0;
        for (const p of vasm.parts) { au.set(p, off); off += p.length; }
        onVideoAU(au, vasm.key);
        vasm = { seq: -1, parts: [], got: 0, key: false };
      }
    } else if (type === 1) {
      if (buf.byteLength <= 13) return;
      frameW = v.getUint16(9, true);
      frameH = v.getUint16(11, true);
      showFrame(new Blob([new Uint8Array(buf, 13)], { type: 'image/jpeg' }));
    } else if (type === 2) {
      onPCM(new Uint8Array(buf, 9));
    } else if (type === 3) {
      onSilence();
    }
  }

  // Immer nur das neueste Bild dekodieren – liegt eins in der Warteschlange,
  // wird es durch das neue ersetzt.
  function showFrame(blob) {
    if (decoding) { pendingBlob = blob; return; }
    decoding = true;
    createImageBitmap(blob).then((bmp) => {
      if (canvas.width !== bmp.width || canvas.height !== bmp.height) {
        canvas.width = bmp.width;
        canvas.height = bmp.height;
      }
      ctx2d.drawImage(bmp, 0, 0);
      bmp.close();
      framesDrawn++;
      if (!hasFrame) {
        hasFrame = true;
        if (state === 'waiting') setState('live');
      }
    }).catch(() => { /* kaputtes Bild, egal */ }).finally(() => {
      decoding = false;
      if (pendingBlob) {
        const next = pendingBlob;
        pendingBlob = null;
        showFrame(next);
      }
    });
  }

  /* ======================================================================
   * Oberfläche
   * ==================================================================== */

  const STATES = {
    connecting: { badge: ['wait', 'Verbinde'], text: 'Verbinde…', spin: true },
    approval: { badge: ['wait', 'Wartet'], text: 'Warte auf Freigabe…', fine: 'Der andere bekommt eine Meldung und lässt dich mit einem Klick rein.', spin: true },
    waiting: { badge: ['wait', 'Gleich'], text: 'Gleich geht es los…', spin: true },
    live: { badge: ['', 'Live'] },
    paused: { badge: ['off', 'Pause'], text: 'Pausiert', fine: 'Der andere hat das Teilen kurz angehalten. Es geht automatisch weiter.' },
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
    ov.style.background = next === 'paused' && hasFrame ? 'rgba(17,18,20,.6)' : '';
    if (next !== 'live') document.body.classList.remove('idle');
    renderTitle();
    updateAudioUI();
  }

  function renderTitle() {
    const who = hostName || room;
    $('#title').textContent = who;
    const bits = [];
    if (targetTitle) bits.push(targetTitle);
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
      $('#stat-res').textContent = frameW ? `${frameW}×${frameH} · ${videoKind}` : '–';
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

    // Antwortet der Server nicht mehr, ist die Leitung tot – neu verbinden.
    if (ws && ws.readyState === WebSocket.OPEN && lastPong && performance.now() - lastPong > 12000) {
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

  let pipVideo = null;
  async function togglePiP() {
    try {
      if (document.pictureInPictureElement) {
        await document.exitPictureInPicture();
        return;
      }
      if (!pipVideo) {
        pipVideo = document.createElement('video');
        pipVideo.muted = true;
        pipVideo.playsInline = true;
        pipVideo.srcObject = canvas.captureStream();
        pipVideo.addEventListener('leavepictureinpicture', () => $('#btn-pip').classList.remove('on'));
        pipVideo.addEventListener('enterpictureinpicture', () => $('#btn-pip').classList.add('on'));
      }
      // Einmal neu malen, damit der Videostrom sofort ein Bild hat.
      if (canvas.width) ctx2d.drawImage(canvas, 0, 0);
      await pipVideo.play();
      await pipVideo.requestPictureInPicture();
    } catch (e) {
      toast('Bild-in-Bild geht in diesem Browser nicht');
    }
  }
  if (!document.pictureInPictureEnabled || !canvas.captureStream) $('#btn-pip').classList.add('hidden');

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
  canvas.addEventListener('dblclick', toggleFullscreen);

  $('#overlay-btn').addEventListener('click', () => {
    reconnectDelay = 1000;
    connect();
  });

  $('#btn-leave').addEventListener('click', () => {
    wantConnect = false;
    cleanup();
    audioSend({ reset: true });
    if (document.fullscreenElement) document.exitFullscreen();
    if (document.pictureInPictureElement) document.exitPictureInPicture();
    hasFrame = false;
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
    history.replaceState(null, '', `?room=${encodeURIComponent(room)}`);
    $('#join').classList.add('hidden');
    $('#stage').classList.remove('hidden');
    hostName = '';
    targetTitle = '';
    hostPaused = false;
    hasFrame = false;
    vdec.waitKey = true;
    vdec.unsupported = false;
    reconnectDelay = 1000;
    state = 'connecting';
    checkSelf();
    connect();
    wake();
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
  updateAudioUI();
  showJoin();
})();
