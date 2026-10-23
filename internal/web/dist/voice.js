/* Sprachkanal wie bei Discord – läuft im Browser, weil nur dort Echo-
 * Unterdrückung, Rauschfilter und automatische Lautstärke (WebRTC) in guter
 * Qualität zur Verfügung stehen. Kodiert wird mit Opus über WebCodecs;
 * Verschlüsseln und Verschicken übernimmt das Raum-Fenster (room.js).
 *
 *   const v = MssVoice.create({ send(opus, stereo), onState(), onChange, canJoin, joinHint });
 *   v.mount(element, settingsElement); v.setMembers(list, meId);
 *   v.onPacket(id, seq, stereo, opus); v.join(); v.leave();
 * Braucht audio-core.js (Ringpuffer für die Wiedergabe). */
(() => {
  'use strict';

  const RATE = 48000;
  const FRAME = 960; // 20 ms
  const HANG_MS = 400; // so lange bleibt die Sprachaktivierung nach dem letzten lauten Stück offen

  const DEFAULTS = {
    mic: '', out: '',
    echo: true, noise: true, agc: true,
    mode: 'vad', // vad | ptt
    pttKey: 'KeyV',
    threshold: -50, // dBFS
    inputGain: 100, outputVolume: 100,
    bitrate: 128000, stereo: true,
    volumes: {}, // Name -> Prozent
    overlay: { enabled: false, opacity: 85, scale: 100, onlySpeaking: false, clickThrough: false },
  };

  function loadSettings() {
    try {
      const s = JSON.parse(localStorage.getItem('mss-voice') || '{}');
      return { ...DEFAULTS, ...s, overlay: { ...DEFAULTS.overlay, ...(s.overlay || {}) }, volumes: s.volumes || {} };
    } catch (e) { return { ...DEFAULTS, overlay: { ...DEFAULTS.overlay } }; }
  }
  function saveSettings(s) { try { localStorage.setItem('mss-voice', JSON.stringify(s)); } catch (e) { /* egal */ } }

  const CAPTURE_WORKLET = `class MssCapture extends AudioWorkletProcessor {
  constructor(o) { super(); this.ch = o.processorOptions.channels; this.buf = []; this.n = 0;
    for (let c = 0; c < this.ch; c++) this.buf.push(new Float32Array(${FRAME})); }
  process(inputs) {
    const inp = inputs[0];
    if (!inp || !inp.length) return true;
    const len = inp[0].length;
    let i = 0;
    while (i < len) {
      const take = Math.min(len - i, ${FRAME} - this.n);
      for (let c = 0; c < this.ch; c++) this.buf[c].set((inp[c] || inp[0]).subarray(i, i + take), this.n);
      this.n += take; i += take;
      if (this.n === ${FRAME}) {
        this.port.postMessage(this.buf, this.buf.map((b) => b.buffer));
        this.buf = []; for (let c = 0; c < this.ch; c++) this.buf.push(new Float32Array(${FRAME}));
        this.n = 0;
      }
    }
    return true;
  }
}
registerProcessor('mss-capture', MssCapture);`;

  function playerWorklet() {
    return `const audioCore = ${audioCore.toString()};
class MssVoicePlayer extends AudioWorkletProcessor {
  constructor() { super(); this.core = audioCore(sampleRate, 0.06); this.core.setRate(${RATE}); this.spare = new Float32Array(128);
    this.port.onmessage = (e) => { const m = e.data; if (m.l) this.core.push(m.l, m.r); else if (m.reset) this.core.reset(); }; }
  process(i, outputs) { const o = outputs[0]; if (this.spare.length !== o[0].length) this.spare = new Float32Array(o[0].length);
    this.core.pull(o[0], o[1] || this.spare); return true; }
}
registerProcessor('mss-voice-player', MssVoicePlayer);`;
  }

  function dbfs(chs) {
    let sum = 0, n = 0;
    for (const c of chs) { for (let i = 0; i < c.length; i++) sum += c[i] * c[i]; n += c.length; }
    return n ? 10 * Math.log10(sum / n + 1e-12) : -120;
  }

  const supported = () => !!(window.isSecureContext && window.AudioEncoder && window.AudioDecoder &&
    navigator.mediaDevices && navigator.mediaDevices.getUserMedia && window.AudioWorkletNode);

  function create(opts) {
    const settings = loadSettings();
    const st = {
      joined: false, joining: false, me: 0, members: [],
      muted: false, deaf: false,
      inputDb: -120, transmitting: false, pttDown: false,
      error: '',
    };
    let ctx = null, master = null, micStream = null, micGain = null, capNode = null, encoder = null;
    let encChannels = 1, hangUntil = 0, encTs = 0;
    const peers = new Map(); // id -> { dec, node, gain, ch, lastPkt, level }
    let root = null, changeTimer = null;
    let silent = false; // lautloser Test: Wiedergabe bleibt hart auf 0

    function changed() {
      if (changeTimer) return;
      changeTimer = setTimeout(() => { changeTimer = null; render(); opts.onChange && opts.onChange(api); }, 50);
    }

    /* ---------- Audio aufsetzen ---------- */

    async function ensureContext() {
      if (ctx) return;
      ctx = new AudioContext({ sampleRate: RATE, latencyHint: 'interactive' });
      master = ctx.createGain();
      master.connect(ctx.destination);
      applyOutput();
      const url = (src) => URL.createObjectURL(new Blob([src], { type: 'application/javascript' }));
      await ctx.audioWorklet.addModule(url(CAPTURE_WORKLET));
      await ctx.audioWorklet.addModule(url(playerWorklet()));
      if (settings.out && ctx.setSinkId) ctx.setSinkId(settings.out).catch(() => {});
    }

    function applyOutput() {
      if (!master) return;
      master.gain.setTargetAtTime(st.deaf || silent ? 0 : settings.outputVolume / 100, ctx.currentTime, 0.02);
      for (const [, p] of peers) applyPeerVolume(p);
    }

    async function startMic() {
      stopMic();
      encChannels = settings.stereo ? 2 : 1;
      micStream = await navigator.mediaDevices.getUserMedia({
        audio: {
          deviceId: settings.mic ? { exact: settings.mic } : undefined,
          echoCancellation: settings.echo, noiseSuppression: settings.noise, autoGainControl: settings.agc,
          channelCount: encChannels, sampleRate: RATE,
        },
      });
      const src = ctx.createMediaStreamSource(micStream);
      micGain = ctx.createGain();
      micGain.gain.value = settings.inputGain / 100;
      capNode = new AudioWorkletNode(ctx, 'mss-capture', { numberOfInputs: 1, numberOfOutputs: 0, processorOptions: { channels: encChannels } });
      capNode.port.onmessage = (e) => onMicFrame(e.data);
      src.connect(micGain).connect(capNode);
      startEncoder();
    }

    function stopMic() {
      if (capNode) { capNode.port.onmessage = null; capNode.disconnect(); capNode = null; }
      if (micStream) { micStream.getTracks().forEach((t) => t.stop()); micStream = null; }
      if (encoder && encoder.state !== 'closed') encoder.close();
      encoder = null;
    }

    function startEncoder() {
      encoder = new AudioEncoder({
        output: (chunk) => {
          const data = new Uint8Array(chunk.byteLength);
          chunk.copyTo(data);
          opts.send(data, encChannels === 2);
        },
        error: (e) => { st.error = 'Opus: ' + e.message; changed(); },
      });
      encoder.configure({
        codec: 'opus', sampleRate: RATE, numberOfChannels: encChannels, bitrate: settings.bitrate,
        opus: { frameDuration: 20000, complexity: 10, useinbandfec: true },
      });
    }

    function onMicFrame(chs) {
      const db = dbfs(chs);
      st.inputDb = db;
      const now = performance.now();
      let open;
      if (settings.mode === 'ptt') {
        open = st.pttDown;
      } else {
        if (db > settings.threshold) hangUntil = now + HANG_MS;
        open = now < hangUntil;
      }
      open = open && !st.muted && st.joined;
      if (open !== st.transmitting) { st.transmitting = open; changed(); }
      if (!open || !encoder || encoder.state !== 'configured') return;
      const data = new Float32Array(FRAME * chs.length);
      chs.forEach((c, i) => data.set(c, i * FRAME));
      const ad = new AudioData({ format: 'f32-planar', sampleRate: RATE, numberOfFrames: FRAME, numberOfChannels: chs.length, timestamp: encTs, data });
      encTs += 20000;
      encoder.encode(ad);
      ad.close();
    }

    /* ---------- Empfangen ---------- */

    function peer(id, ch) {
      let p = peers.get(id);
      if (p && p.ch !== ch) { dropPeer(id); p = null; }
      if (p) return p;
      const node = new AudioWorkletNode(ctx, 'mss-voice-player', { numberOfInputs: 0, outputChannelCount: [2] });
      const gain = ctx.createGain();
      node.connect(gain).connect(master);
      p = { id, ch, node, gain, lastPkt: 0, level: 0, dec: null, bytes: 0, kbps: 0 };
      p.dec = new AudioDecoder({
        output: (ad) => {
          const n = ad.numberOfFrames;
          const l = new Float32Array(n), r = new Float32Array(n);
          ad.copyTo(l, { planeIndex: 0, format: 'f32-planar' });
          if (ad.numberOfChannels > 1) ad.copyTo(r, { planeIndex: 1, format: 'f32-planar' }); else r.set(l);
          ad.close();
          p.level = Math.max(p.level * 0.6, Math.pow(10, dbfs([l]) / 20));
          node.port.postMessage({ l, r }, [l.buffer, r.buffer]);
        },
        error: () => { /* nächstes Paket versucht es wieder */ },
      });
      p.dec.configure({ codec: 'opus', sampleRate: RATE, numberOfChannels: ch });
      peers.set(id, p);
      applyPeerVolume(p);
      return p;
    }

    function dropPeer(id) {
      const p = peers.get(id);
      if (!p) return;
      try { p.dec.close(); } catch (e) { /* egal */ }
      p.node.disconnect();
      p.gain.disconnect();
      peers.delete(id);
    }

    function memberName(id) { const m = st.members.find((x) => x.id === id); return m ? m.name : ''; }

    function applyPeerVolume(p) {
      const v = settings.volumes[memberName(p.id)];
      p.gain.gain.value = (v === undefined ? 100 : v) / 100;
    }

    function onPacket(id, seq, stereo, data) {
      if (!st.joined || !ctx || st.deaf) return;
      const p = peer(id, stereo ? 2 : 1);
      p.lastPkt = performance.now();
      p.bytes += data.byteLength;
      if (p.dec.state !== 'configured') return;
      p.dec.decode(new EncodedAudioChunk({ type: 'key', timestamp: seq * 20000, data }));
    }

    /* ---------- Steuerung ---------- */

    // setMembers: wer ist im Sprachkanal ({ id, name, muted, deaf }), wer bin ich?
    function setMembers(list, me) {
      const old = new Map(st.members.map((m) => [m.id, m]));
      st.members = list.map((x) => ({ ...x, speaking: old.has(x.id) ? old.get(x.id).speaking : false }));
      st.me = me;
      for (const id of [...peers.keys()]) if (!st.members.some((x) => x.id === id)) dropPeer(id);
      for (const [, p] of peers) applyPeerVolume(p);
      changed();
    }
    const announce = () => opts.onState && opts.onState();

    async function join() {
      if (st.joined || st.joining) return;
      if (!supported()) { st.error = 'Sprachkanal braucht einen aktuellen Browser über https (oder das Panel auf diesem PC).'; changed(); return; }
      st.joining = true; st.error = ''; changed();
      try {
        await ensureContext();
        await ctx.resume();
        await startMic();
        st.joined = true;
        announce();
        listDevices();
      } catch (e) {
        st.error = e.name === 'NotAllowedError' ? 'Mikrofon wurde nicht erlaubt – im Browser freigeben.' : 'Mikrofon: ' + e.message;
        stopMic();
      }
      st.joining = false;
      changed();
    }

    function leave() {
      if (!st.joined) return;
      st.joined = false; st.transmitting = false;
      announce();
      stopMic();
      for (const id of [...peers.keys()]) dropPeer(id);
      changed();
    }

    function setMuted(v) { st.muted = v; announce(); changed(); }
    function setDeaf(v) {
      st.deaf = v;
      if (v) st.muted = true;
      applyOutput();
      announce();
      changed();
    }

    // Push-to-Talk greift, solange diese Seite den Fokus hat.
    window.addEventListener('keydown', (e) => {
      if (settings.mode !== 'ptt' || e.code !== settings.pttKey || e.repeat) return;
      if (e.target.matches && e.target.matches('input, select, textarea')) return;
      st.pttDown = true;
    });
    window.addEventListener('keyup', (e) => { if (e.code === settings.pttKey) st.pttDown = false; });
    window.addEventListener('blur', () => { st.pttDown = false; });

    // Datenrate je Teilnehmer – zeigt, ob überhaupt Ton ankommt.
    setInterval(() => {
      for (const [, p] of peers) { p.kbps = Math.round(p.bytes * 8 / 1000); p.bytes = 0; }
      if (st.joined) changed();
    }, 1000);

    // Sprech-Anzeige: frische Pakete mit hörbarem Pegel.
    setInterval(() => {
      const now = performance.now();
      let dirty = false;
      for (const m of st.members) {
        let speaking;
        if (m.id === st.me) speaking = st.transmitting;
        else { const p = peers.get(m.id); speaking = !!p && now - p.lastPkt < 250 && p.level > 0.01; if (p) p.level *= 0.7; }
        if (m.speaking !== speaking) { m.speaking = speaking; dirty = true; }
      }
      if (dirty) changed();
      if (root && st.joined) renderMeter();
    }, 100);

    // Spielt einen leisen, kurzen Ton über denselben Weg wie die Stimmen –
    // so lässt sich prüfen, ob Ausgabegerät und Lautstärke stimmen.
    async function testOutput() {
      await ensureContext();
      await ctx.resume();
      const osc = ctx.createOscillator();
      const g = ctx.createGain();
      const t = ctx.currentTime;
      osc.frequency.value = 523;
      g.gain.setValueAtTime(0, t);
      g.gain.linearRampToValueAtTime(0.03, t + 0.05); // etwa −30 dB
      g.gain.linearRampToValueAtTime(0, t + 0.4);
      osc.connect(g).connect(master);
      osc.start(t);
      osc.stop(t + 0.45);
    }

    /* ---------- Geräte ---------- */

    let devices = { mics: [], outs: [] };
    async function listDevices() {
      try {
        const all = await navigator.mediaDevices.enumerateDevices();
        devices = {
          mics: all.filter((d) => d.kind === 'audioinput'),
          outs: all.filter((d) => d.kind === 'audiooutput'),
        };
        changed();
      } catch (e) { /* egal */ }
    }

    function set(key, value) {
      settings[key] = value;
      saveSettings(settings);
      if (['mic', 'echo', 'noise', 'agc', 'stereo'].includes(key) && st.joined) startMic().catch((e) => { st.error = e.message; changed(); });
      if (key === 'bitrate' && st.joined) { if (encoder && encoder.state !== 'closed') encoder.close(); startEncoder(); }
      if (key === 'inputGain' && micGain) micGain.gain.value = value / 100;
      if (key === 'outputVolume') applyOutput();
      if (key === 'out' && ctx && ctx.setSinkId) ctx.setSinkId(value).catch(() => {});
      changed();
    }

    function setVolume(name, pct) {
      settings.volumes[name] = pct;
      saveSettings(settings);
      for (const [, p] of peers) applyPeerVolume(p);
    }

    /* ---------- Oberfläche ---------- */

    const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; };

    let setRoot = null;
    function mount(container, settingsEl) {
      root = container;
      root.classList.add('voice');
      root.innerHTML = `
        <div class="voice-head">
          <button class="btn primary" data-act="join" type="button">Sprachkanal beitreten</button>
          <button class="btn icon" data-act="mute" type="button" title="Mikrofon stumm (Strg+Umschalt+M)" aria-label="Mikrofon stumm">
            <svg viewBox="0 0 24 24" width="20" height="20"><rect x="9" y="3" width="6" height="11" rx="3" fill="currentColor"/><path d="M5.5 11a6.5 6.5 0 0013 0M12 17.5V21" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><path class="slash" d="M4 4l16 16" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
          </button>
          <button class="btn icon" data-act="deaf" type="button" title="Taub schalten (Strg+Umschalt+D)" aria-label="Taub schalten">
            <svg viewBox="0 0 24 24" width="20" height="20"><path d="M4 14v-2a8 8 0 0116 0v2" fill="none" stroke="currentColor" stroke-width="1.8"/><rect x="3" y="13" width="4" height="7" rx="1.5" fill="currentColor"/><rect x="17" y="13" width="4" height="7" rx="1.5" fill="currentColor"/><path class="slash" d="M4 4l16 16" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
          </button>
          <button class="btn icon" data-act="settings" type="button" title="Spracheinstellungen" aria-label="Spracheinstellungen">
            <svg viewBox="0 0 24 24" width="20" height="20"><path d="M4 7h10M18 7h2M4 17h4M12 17h8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><circle cx="16" cy="7" r="2.2" fill="none" stroke="currentColor" stroke-width="1.8"/><circle cx="10" cy="17" r="2.2" fill="none" stroke="currentColor" stroke-width="1.8"/></svg>
          </button>
          <button class="btn ghost" data-act="overlay" type="button" title="Schwebende Teilnehmerliste über allen Fenstern">Overlay</button>
        </div>
        <p class="voice-error error hidden"></p>
        <div class="voice-members"></div>
        <div class="voice-settings hidden">
          <label class="field"><span>Mikrofon</span><select data-set="mic"></select></label>
          <label class="field"><span>Ausgabe</span><select data-set="out"></select>
            <button class="btn ghost" data-act="testout" type="button">Ausgabe testen (leiser Ton)</button></label>
          <label class="field"><span>Eingabemodus</span><select data-set="mode"><option value="vad">Sprachaktivierung</option><option value="ptt">Push-to-Talk (solange dieses Fenster aktiv ist)</option></select></label>
          <label class="field ptt-only"><span>Push-to-Talk-Taste</span><button class="btn ghost" data-act="pttkey" type="button"></button></label>
          <label class="field full vad-only"><span>Empfindlichkeit <b class="thr-label"></b></span>
            <div class="meter"><div class="meter-level"></div><div class="meter-thr"></div></div>
            <input type="range" min="-80" max="-10" step="1" data-set="threshold"></label>
          <label class="field"><span>Mikrofon-Verstärkung <b class="ig-label"></b></span><input type="range" min="0" max="300" step="5" data-set="inputGain"></label>
          <label class="field"><span>Lautstärke der anderen <b class="ov-label"></b></span><input type="range" min="0" max="200" step="5" data-set="outputVolume"></label>
          <label class="switch"><input type="checkbox" data-set="echo"><span class="label"><b>Echo-Unterdrückung</b><span>Damit die anderen sich nicht selbst hören (ohne Kopfhörer wichtig)</span></span></label>
          <label class="switch"><input type="checkbox" data-set="noise"><span class="label"><b>Rauschunterdrückung</b><span>Filtert Lüfter, Tastatur und Hintergrundgeräusche</span></span></label>
          <label class="switch"><input type="checkbox" data-set="agc"><span class="label"><b>Automatische Lautstärke</b><span>Gleicht leise und laute Stimme aus</span></span></label>
          <label class="switch"><input type="checkbox" data-set="stereo"><span class="label"><b>Stereo</b><span>Für Stereo-Mikrofone und Musik – sonst Mono</span></span></label>
          <label class="field"><span>Bitrate</span><select data-set="bitrate">
            <option value="64000">64 kbit/s</option><option value="96000">96 kbit/s</option><option value="128000">128 kbit/s – Standard</option>
            <option value="192000">192 kbit/s</option><option value="256000">256 kbit/s</option><option value="510000">510 kbit/s – maximal</option></select></label>
          <div class="overlay-settings field full">
            <span>Overlay</span>
            <label class="field"><span>Deckkraft <b class="op-label"></b></span><input type="range" min="20" max="100" step="5" data-ov="opacity"></label>
            <label class="field"><span>Größe <b class="sc-label"></b></span><input type="range" min="60" max="200" step="10" data-ov="scale"></label>
            <label class="switch"><input type="checkbox" data-ov="onlySpeaking"><span class="label"><b>Nur wer gerade spricht</b><span>Sonst alle Teilnehmer</span></span></label>
            <label class="switch"><input type="checkbox" data-ov="clickThrough"><span class="label"><b>Klicks durchlassen</b><span>Overlay lässt sich dann nicht mehr verschieben</span></span></label>
          </div>
        </div>`;
      setRoot = settingsEl || root;
      if (settingsEl) { const vs = root.querySelector('.voice-settings'); vs.classList.remove('hidden'); settingsEl.append(vs); }
      for (const r of new Set([root, setRoot])) {
        r.addEventListener('click', onClick);
        r.addEventListener('input', onInput);
        r.addEventListener('change', onInput);
      }
      render();
    }

    let waitingKey = false;
    function onClick(e) {
      const b = e.target.closest('[data-act]');
      if (!b) return;
      switch (b.dataset.act) {
        case 'join': st.joined ? leave() : join(); break;
        case 'mute': setMuted(!st.muted); break;
        case 'deaf': setDeaf(!st.deaf); break;
        case 'settings': opts.onSettings ? opts.onSettings() : setRoot.querySelector('.voice-settings').classList.toggle('hidden'); listDevices(); break;
        case 'overlay': opts.onOverlay && opts.onOverlay(api); break;
        case 'testout': testOutput(); break;
        case 'pttkey':
          waitingKey = true;
          b.textContent = 'Taste drücken…';
          window.addEventListener('keydown', function grab(ev) {
            ev.preventDefault();
            settings.pttKey = ev.code;
            saveSettings(settings);
            waitingKey = false;
            window.removeEventListener('keydown', grab, true);
            render();
          }, true);
          break;
        case 'vol': break;
      }
    }

    function onInput(e) {
      const t = e.target;
      if (t.dataset.set) {
        const k = t.dataset.set;
        let v = t.type === 'checkbox' ? t.checked : t.value;
        if (['threshold', 'inputGain', 'outputVolume', 'bitrate'].includes(k)) v = Number(v);
        if (e.type === 'input' && t.tagName === 'SELECT') return;
        set(k, v);
      } else if (t.dataset.ov) {
        const k = t.dataset.ov;
        settings.overlay[k] = t.type === 'checkbox' ? t.checked : Number(t.value);
        saveSettings(settings);
        changed();
      } else if (t.dataset.vol) {
        setVolume(t.dataset.vol, Number(t.value));
      }
    }

    // Tastenkürzel wie bei Discord.
    window.addEventListener('keydown', (e) => {
      if (!st.joined || !e.ctrlKey || !e.shiftKey) return;
      if (e.code === 'KeyM') { setMuted(!st.muted); e.preventDefault(); }
      if (e.code === 'KeyD') { setDeaf(!st.deaf); e.preventDefault(); }
    });

    function renderMeter() {
      const pct = (db) => Math.max(0, Math.min(100, (db + 80) / 70 * 100));
      const lvl = setRoot.querySelector('.meter-level');
      if (!lvl) return;
      lvl.style.width = pct(st.inputDb) + '%';
      lvl.classList.toggle('open', st.transmitting);
      setRoot.querySelector('.meter-thr').style.left = pct(settings.threshold) + '%';
    }

    function fillSelect(sel, list, value, fallback) {
      const key = list.map((d) => d.deviceId).join('|');
      if (sel.dataset.key !== key) {
        sel.dataset.key = key;
        sel.innerHTML = '';
        sel.appendChild(new Option(fallback, ''));
        for (const d of list) if (d.deviceId !== 'default') sel.appendChild(new Option(d.label || 'Gerät', d.deviceId));
      }
      sel.value = value;
    }

    function render() {
      if (!root) return;
      const q = (s) => root.querySelector(s) || setRoot.querySelector(s);
      const join = q('[data-act=join]');
      join.textContent = st.joining ? 'Verbinde…' : (st.joined ? 'Verlassen' : 'Sprachkanal beitreten');
      join.className = 'btn ' + (st.joined ? 'danger' : 'primary');
      join.disabled = st.joining || !opts.canJoin();
      q('[data-act=mute]').classList.toggle('off', st.muted);
      q('[data-act=deaf]').classList.toggle('off', st.deaf);
      q('[data-act=mute]').classList.toggle('hidden', !st.joined);
      q('[data-act=deaf]').classList.toggle('hidden', !st.joined);
      const err = q('.voice-error');
      err.textContent = st.error || (!opts.canJoin() ? opts.joinHint() : '');
      err.classList.toggle('hidden', !err.textContent);

      const box = q('.voice-members');
      box.innerHTML = '';
      for (const m of st.members) {
        const row = el('div', 'voice-member' + (m.speaking ? ' speaking' : '') + (m.id === st.me ? ' me' : ''));
        const av = el('span', 'avatar', (m.name || '?').slice(0, 1).toUpperCase());
        const name = el('span', 'name', m.name + (m.id === st.me ? ' (du)' : ''));
        row.append(av, name);
        if (m.id !== st.me && st.joined) {
          const p = peers.get(m.id);
          row.append(el('span', 'rate', p && p.kbps ? `${p.kbps} kbit/s` : 'kein Ton'));
        }
        if (m.muted) row.append(el('span', 'flag', 'stumm'));
        if (m.deaf) row.append(el('span', 'flag', 'taub'));
        if (m.id !== st.me) {
          const vol = el('input');
          vol.type = 'range'; vol.min = 0; vol.max = 200; vol.step = 5;
          vol.value = settings.volumes[m.name] ?? 100;
          vol.dataset.vol = m.name;
          vol.title = 'Lautstärke für ' + m.name;
          row.append(vol);
        }
        box.append(row);
      }
      if (!st.members.length && st.joined) box.append(el('p', 'link-note', 'Noch niemand sonst da.'));

      fillSelect(q('[data-set=mic]'), devices.mics, settings.mic, 'Standard-Mikrofon');
      fillSelect(q('[data-set=out]'), devices.outs, settings.out, 'Standard-Ausgabe');
      q('[data-set=out]').disabled = !(window.AudioContext && AudioContext.prototype.setSinkId);
      for (const k of ['mode', 'bitrate']) q(`[data-set=${k}]`).value = String(settings[k]);
      for (const k of ['echo', 'noise', 'agc', 'stereo']) q(`[data-set=${k}]`).checked = !!settings[k];
      for (const k of ['threshold', 'inputGain', 'outputVolume']) q(`[data-set=${k}]`).value = String(settings[k]);
      q('.thr-label').textContent = settings.threshold + ' dB';
      q('.ig-label').textContent = settings.inputGain + ' %';
      q('.ov-label').textContent = settings.outputVolume + ' %';
      q('.ptt-only').classList.toggle('hidden', settings.mode !== 'ptt');
      q('.vad-only').classList.toggle('hidden', settings.mode !== 'vad');
      if (!waitingKey) q('[data-act=pttkey]').textContent = settings.pttKey.replace(/^Key|^Digit/, '');
      for (const k of ['opacity', 'scale']) q(`[data-ov=${k}]`).value = String(settings.overlay[k]);
      for (const k of ['onlySpeaking', 'clickThrough']) q(`[data-ov=${k}]`).checked = !!settings.overlay[k];
      q('.op-label').textContent = settings.overlay.opacity + ' %';
      q('.sc-label').textContent = settings.overlay.scale + ' %';
      renderMeter();
    }

    const api = {
      mount, join, leave, setMembers, onPacket, listDevices, setVolume, setMuted, setDeaf,
      get state() { return st; },
      get settings() { return settings; },
      // Für das Overlay: wer ist da, wer spricht gerade?
      snapshot() {
        return {
          members: st.members.map((m) => ({ name: m.name, speaking: !!m.speaking, muted: !!m.muted, deaf: !!m.deaf, me: m.id === st.me })),
          overlay: settings.overlay,
        };
      },
      refresh: changed,
      // Nur für den lautlosen Test: Mikrofon durch einen beliebigen Strom ersetzen.
      async _testInput(stream) {
        silent = true;
        await ensureContext();
        master.gain.value = 0;
        micStream = stream;
        encChannels = settings.stereo ? 2 : 1;
        const src = ctx.createMediaStreamSource(stream);
        micGain = ctx.createGain();
        capNode = new AudioWorkletNode(ctx, 'mss-capture', { numberOfInputs: 1, numberOfOutputs: 0, processorOptions: { channels: encChannels } });
        capNode.port.onmessage = (e) => onMicFrame(e.data);
        src.connect(micGain).connect(capNode);
        startEncoder();
        st.joined = true;
        announce();
        return ctx;
      },
      _peers: peers,
    };
    return api;
  }

  window.MssVoice = { create, supported };
})();
