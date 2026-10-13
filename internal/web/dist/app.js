/* Viewer: WebSocket rein, Canvas raus. Bild kommt als JPEG, Ton als PCM. */
(() => {
  'use strict';

  const $ = (s) => document.querySelector(s);
  const canvas = $('#screen');
  const ctx = canvas.getContext('2d', { alpha: false, desynchronized: true });
  const params = new URLSearchParams(location.search);

  let room = (params.get('room') || '').trim().toLowerCase();
  let playerName = 'Gast';
  try { playerName = localStorage.getItem('mss-name') || 'Gast'; } catch (e) { /* egal */ }
  let ws = null;
  let wantConnect = false;
  let state = 'idle';           // idle | connecting | waiting | live | reconnecting | ended | error
  let viewers = 0;
  let frameW = 0, frameH = 0;
  let pending = null, decoding = false;
  let frameCount = 0, byteCount = 0, lastFrameAt = 0;
  let fps = 0, kbps = 0, rtt = 0;
  let pingTimer = null, statTimer = null, lastPing = 0;
  let lastPong = 0, lastGoodState = 'connecting', serverReconnecting = false;
  let joinToken = '';
  let reconnectTimer = null, reconnectDelay = 1000;

  // Audio
  let actx = null, gain = null, nextTime = 0, muted = false, audioRate = 48000;
  let audioEnabled = false, audioPeak = 0, audioUnlocked = false;

  /* ---------- Verbindung ---------- */

  // Ein Token pro Raum und Browser: Damit darf man nach einem
  // Verbindungsabbruch ohne neue Freigabe wieder rein.
  function tokenFor(roomName) {
    const key = 'mss-token-' + roomName;
    let token = '';
    try { token = localStorage.getItem(key) || ''; } catch (e) { /* egal */ }
    if (!token) {
      const bytes = new Uint8Array(16);
      crypto.getRandomValues(bytes);
      token = Array.from(bytes).map((b) => b.toString(16).padStart(2, '0')).join('');
      try { localStorage.setItem(key, token); } catch (e) { /* egal */ }
    }
    return token;
  }

  function connect() {
    cleanup();
    wantConnect = true;
    lastPong = 0;
    serverReconnecting = false;
    lastGoodState = 'connecting';
    joinToken = tokenFor(room);
    setState('connecting');
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    try {
      ws = new WebSocket(`${proto}//${location.host}/ws`);
    } catch (e) {
      return fail('Verbindung fehlgeschlagen');
    }
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => {
      ws.send(JSON.stringify({ t: 'join', room, role: 'viewer', name: playerName, token: joinToken }));
      startPing();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        try { onControl(JSON.parse(ev.data)); } catch (e) { /* egal */ }
      } else {
        onBinary(ev.data);
      }
    };
    ws.onclose = () => {
      stopPing();
      if (state !== 'ended' && state !== 'error') setState('reconnecting');
      if (wantConnect) scheduleReconnect();
    };
    ws.onerror = () => { /* onclose übernimmt */ };
  }

  function cleanup() {
    clearTimeout(reconnectTimer);
    if (ws) {
      ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
      try { ws.close(); } catch (e) { /* egal */ }
      ws = null;
    }
  }

  function scheduleReconnect() {
    clearTimeout(reconnectTimer);
    reconnectTimer = setTimeout(() => {
      if (wantConnect) connect();
    }, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 10000);
  }

  function startPing() {
    stopPing();
    pingTimer = setInterval(() => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        lastPing = performance.now();
        ws.send(JSON.stringify({ t: 'ping', ts: Date.now() }));
      }
    }, 3000);
  }

  function stopPing() {
    clearInterval(pingTimer);
    pingTimer = null;
  }

  /* ---------- Steuernachrichten ---------- */

  function onControl(m) {
    switch (m.t) {
      case 'joined':
        reconnectDelay = 1000;
        if (state === 'connecting') setState('approval');
        break;
      case 'allowed':
        serverReconnecting = false;
        if (state === 'approval') setState('waiting');
        break;
      case 'denied':
        wantConnect = false;
        setState('denied');
        break;
      case 'meta':
        if (m.w && m.h) {
          frameW = m.w; frameH = m.h;
          applyCanvasSize();
        }
        if (m.rate) audioRate = m.rate;
        if (typeof m.audio === 'boolean') audioEnabled = m.audio;
        updateAudioButton();
        setState('live');
        break;
      case 'viewers':
        viewers = m.n || 0;
        break;
      case 'pong':
        rtt = Math.max(0, performance.now() - lastPing);
        lastPong = performance.now();
        if (state === 'reconnecting' && !serverReconnecting) setState(lastGoodState);
        break;
      case 'reconnecting':
        serverReconnecting = true;
        setState('reconnecting');
        break;
      case 'waiting':
        serverReconnecting = false;
        if (state === 'reconnecting') setState('approval');
        break;
      case 'live':
        serverReconnecting = false;
        setState('live');
        break;
      case 'ended':
        setState('ended');
        break;
      case 'error':
        fail(m.msg || 'Unbekannter Fehler');
        break;
    }
  }

  /* ---------- Bild ---------- */

  function onBinary(buf) {
    const v = new DataView(buf);
    const type = v.getUint8(0);
    byteCount += buf.byteLength;
    if (type === 1) {
      if (buf.byteLength <= 13) return;
      frameW = v.getUint16(9, true);
      frameH = v.getUint16(11, true);
      const jpeg = new Uint8Array(buf, 13);
      if (decoding) { pending = jpeg; return; }
      decodeFrame(jpeg);
    } else if (type === 2) {
      playPCM(new Uint8Array(buf, 9));
    } else if (type === 3) {
      advanceSilence(0.02);
    }
  }

  async function decodeFrame(jpeg) {
    decoding = true;
    try {
      const bmp = await createImageBitmap(new Blob([jpeg], { type: 'image/jpeg' }));
      if (canvas.width !== frameW || canvas.height !== frameH) {
        canvas.width = frameW;
        canvas.height = frameH;
      }
      ctx.drawImage(bmp, 0, 0);
      bmp.close();
      frameCount++;
      lastFrameAt = performance.now();
      if (state === 'waiting') setState('live');
    } catch (e) { /* kaputtes Frame, ignorieren */ }
    decoding = false;
    if (pending) {
      const next = pending;
      pending = null;
      decodeFrame(next);
    }
  }

  function applyCanvasSize() {
    if (!frameW || !frameH) return;
    const wrap = $('#screen-wrap');
    if (!wrap.clientWidth || !wrap.clientHeight) return;
    canvas.width = frameW;
    canvas.height = frameH;
    const scale = Math.min(wrap.clientWidth / frameW, wrap.clientHeight / frameH);
    canvas.style.width = Math.floor(frameW * scale) + 'px';
    canvas.style.height = Math.floor(frameH * scale) + 'px';
  }

  /* ---------- Ton ---------- */

  function ensureAudio() {
    if (actx) return;
    try {
      // Bewusst ohne sampleRate-Option: Der Browser nimmt die Rate des
      // Geräts und rechnet unsere Puffer selbst um. Erzwungene Raten haben
      // auf manchen Systemen (Chromium unter PipeWire) geknistert.
      actx = new AudioContext();
      gain = actx.createGain();
      gain.connect(actx.destination);
    } catch (e) {
      actx = null;
    }
    updateAudioButton();
  }

  function playPCM(bytes) {
    if (muted) return;
    ensureAudio();
    if (!actx) return;
    if (actx.state !== 'running') { updateAudioButton(); return; }
    const samples = bytes.byteLength >> 1;
    const frames = samples >> 1;
    if (frames <= 0) return;
    const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    const buf = actx.createBuffer(2, frames, audioRate);
    const l = buf.getChannelData(0);
    const r = buf.getChannelData(1);
    let peak = 0;
    for (let i = 0; i < frames; i++) {
      const lv = dv.getInt16(i * 4, true) / 32768;
      const rv = dv.getInt16(i * 4 + 2, true) / 32768;
      l[i] = lv;
      r[i] = rv;
      const a = lv < 0 ? -lv : lv;
      const b = rv < 0 ? -rv : rv;
      if (a > peak) peak = a;
      if (b > peak) peak = b;
    }
    if (peak > audioPeak) audioPeak = peak;
    const src = actx.createBufferSource();
    src.buffer = buf;
    src.connect(gain);
    schedule(src, buf.duration);
  }

  function advanceSilence(seconds) {
    if (!actx || actx.state !== 'running') return;
    const now = actx.currentTime;
    if (nextTime < now + 0.03) nextTime = now + 0.2;
    if (nextTime > now + 0.8) nextTime = now + 0.25;
    nextTime += seconds;
  }

  function schedule(src, duration) {
    const now = actx.currentTime;
    if (nextTime < now + 0.03) nextTime = now + 0.2;   // Unterlauf: neu puffern
    if (nextTime > now + 0.8) nextTime = now + 0.25;   // zu weit voraus: aufholen
    src.start(nextTime);
    nextTime += duration;
  }

  function updateAudioButton() {
    const btn = $('#btn-audio');
    const needsClick = !actx || actx.state !== 'running';
    btn.classList.toggle('attention', needsClick && !muted);
    btn.querySelector('.ico-on').classList.toggle('hidden', muted);
    btn.querySelector('.ico-off').classList.toggle('hidden', !muted);
    btn.title = needsClick ? 'Ton aktivieren' : (muted ? 'Ton an' : 'Ton aus');
    const hint = $('#audio-hint');
    if (hint) hint.classList.toggle('hidden', !(needsClick && !muted && audioEnabled));
  }

  // Browser erlauben Ton erst nach einer Interaktion: Der erste Klick oder
  // Tastendruck irgendwo auf der Seite schaltet ihn frei.
  function unlockAudio() {
    audioUnlocked = true;
    ensureAudio();
    if (actx && actx.state !== 'running') {
      actx.resume().then(updateAudioButton).catch(() => {});
    }
  }

  document.addEventListener('click', unlockAudio, true);
  document.addEventListener('keydown', unlockAudio, true);

  /* ---------- Oberfläche ---------- */

  function setState(next) {
    if (state === next && next !== 'live') return;
    state = next;
    if (next === 'live' || next === 'waiting' || next === 'approval') lastGoodState = next;
    const dot = $('#status-dot');
    dot.className = 'dot';
    const ov = $('#overlay-msg');
    const ovText = $('#overlay-text');
    const ovBtn = $('#overlay-btn');
    const spinner = ov.querySelector('.spinner');
    let text = '';
    let showOverlay = true;
    let showSpinner = false;
    let btn = null;

    switch (next) {
      case 'connecting':
        dot.classList.add('yellow');
        text = 'Verbinde…';
        showSpinner = true;
        break;
      case 'waiting':
        dot.classList.add('yellow');
        text = 'Warte auf Stream…';
        showSpinner = true;
        break;
      case 'approval':
        dot.classList.add('yellow');
        text = 'Warte auf Freigabe durch den Streamer…';
        showSpinner = true;
        break;
      case 'denied':
        dot.classList.add('red');
        text = 'Der Streamer hat die Anfrage abgelehnt.';
        btn = 'Erneut anfragen';
        break;
      case 'live':
        dot.classList.add('green');
        showOverlay = false;
        break;
      case 'reconnecting':
        dot.classList.add('yellow');
        text = 'Verbindung unterbrochen…';
        showSpinner = true;
        break;
      case 'ended':
        dot.classList.add('red');
        text = 'Stream beendet';
        btn = 'Neu verbinden';
        wantConnect = false;
        break;
      case 'error':
        dot.classList.add('red');
        text = lastError || 'Fehler';
        btn = 'Neu versuchen';
        break;
    }

    $('#status-text').textContent = next === 'live' ? 'Live' : (text || '');
    ov.classList.toggle('hidden', !showOverlay);
    ovText.textContent = text;
    spinner.classList.toggle('hidden', !showSpinner);
    ovBtn.classList.toggle('hidden', !btn);
    if (btn) ovBtn.textContent = btn;
  }

  let lastError = '';

  function fail(msg) {
    lastError = msg;
    wantConnect = false;
    cleanup();
    setState('error');
  }

  function updateStats() {
    const now = performance.now();
    fps = frameCount * 2;
    kbps = Math.round((byteCount * 8 / 1000) / 2);
    frameCount = 0;
    byteCount = 0;

    $('#stat-res').textContent = frameW ? `${frameW}×${frameH}` : '–';
    $('#stat-fps').textContent = `${fps} fps`;
    $('#stat-rate').textContent = kbps >= 1000
      ? `${(kbps / 1000).toFixed(1).replace('.', ',')} Mbit/s`
      : `${kbps} kbit/s`;
    $('#stat-ping').textContent = rtt ? `Ping ${Math.round(rtt)} ms` : 'Ping –';
    const level = Math.round(audioPeak * 100);
    audioPeak = 0;
    $('#stat-audio').textContent = audioEnabled ? `Ton ${level}%` : 'Ton –';
    $('#stat-viewers').textContent = `${viewers} Zuschauer`;

    // Kontext kann vom Browser schlafen gelegt werden – nach einer
    // Interaktion wecken wir ihn selbst wieder auf.
    if (actx && audioUnlocked && actx.state === 'suspended') {
      actx.resume().then(updateAudioButton).catch(() => {});
    }

    // Nur wenn wirklich keine Antworten mehr kommen, ist die Verbindung
    // unterbrochen. Ein stilles Bild ist kein Fehler: Unveränderte Frames
    // werden bewusst nicht verschickt.
    if (ws && ws.readyState === WebSocket.OPEN && lastPong &&
        (state === 'live' || state === 'waiting' || state === 'approval') &&
        now - lastPong > 15000) {
      setState('reconnecting');
    }
  }

  function toggleFullscreen() {
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else {
      document.documentElement.requestFullscreen();
    }
  }

  /* ---------- Events ---------- */

  $('#join-card').addEventListener('submit', (ev) => {
    ev.preventDefault();
    const value = $('#room-input').value.trim().toLowerCase();
    if (!value) return;
    room = value;
    const name = $('#name-input').value.trim();
    playerName = name || 'Gast';
    try { localStorage.setItem('mss-name', playerName); } catch (e) { /* egal */ }
    history.replaceState(null, '', `?room=${encodeURIComponent(room)}`);
    $('#join').classList.add('hidden');
    $('#stage').classList.remove('hidden');
    $('#room-name').textContent = room;
    connect();
  });

  $('#overlay-btn').addEventListener('click', () => {
    if (state === 'ended') {
      // Stream ist vorbei: von vorn versuchen, sobald der Host wieder da ist.
      reconnectDelay = 1500;
      connect();
    } else {
      connect();
    }
  });

  $('#btn-leave').addEventListener('click', () => {
    wantConnect = false;
    cleanup();
    if (document.fullscreenElement) document.exitFullscreen();
    $('#stage').classList.add('hidden');
    $('#join').classList.remove('hidden');
    $('#room-input').value = room;
    $('#join-error').classList.add('hidden');
  });

  $('#btn-audio').addEventListener('click', () => {
    ensureAudio();
    if (actx && actx.state !== 'running') {
      actx.resume();
      muted = false;
    } else {
      muted = !muted;
    }
    updateAudioButton();
  });

  $('#btn-fullscreen').addEventListener('click', toggleFullscreen);
  canvas.addEventListener('dblclick', toggleFullscreen);

  document.addEventListener('fullscreenchange', () => {
    document.body.classList.toggle('fs', !!document.fullscreenElement);
    if (document.fullscreenElement) {
      showUI();
      applyCanvasSize();
    }
  });

  let uiTimer = null;
  function showUI() {
    document.body.classList.add('show-ui');
    clearTimeout(uiTimer);
    uiTimer = setTimeout(() => document.body.classList.remove('show-ui'), 2500);
  }

  document.addEventListener('mousemove', () => {
    if (document.fullscreenElement) showUI();
  });

  window.addEventListener('resize', applyCanvasSize);
  window.addEventListener('keydown', (ev) => {
    if (ev.key === 'f' && !ev.ctrlKey && !ev.metaKey) toggleFullscreen();
  });

  /* ---------- Start ---------- */

  if (playerName !== 'Gast') $('#name-input').value = playerName;

  if (room) {
    $('#join').classList.add('hidden');
    $('#stage').classList.remove('hidden');
    $('#room-name').textContent = room;
    connect();
  } else {
    $('#room-input').focus();
  }

  statTimer = setInterval(updateStats, 500);
  updateAudioButton();
})();
