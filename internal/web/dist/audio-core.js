/* Ton-Kern des Viewers: Ringpuffer mit durchgehender Umrechnung auf die
 * Rate des Ausgabegeräts. Läuft im AudioWorklet (der Quelltext wird dort per
 * toString() eingebettet) oder als Notfallweg im ScriptProcessor.
 * Prüfen ohne Browser: node internal/web/audio_core_check.cjs */
function audioCore(outRate, minTarget) {
  const CAP = 1 << 17; // ~2,7 s bei 48 kHz
  const MASK = CAP - 1;
  const L = new Float32Array(CAP), R = new Float32Array(CAP);
  const MIN = minTarget, MAX = 0.6;
  let w = 0, r = 0, srcRate = 48000;
  let playing = false, target = Math.max(MIN, 0.12), ratio = 1;
  let clock = 0, underrunAt = -1, calmSince = 0, underruns = 0;

  function avail() { return w - r; }

  function noteData() {
    // Kam nach einem Aussetzer binnen einer Sekunde wieder Ton, war es das
    // Netz: Puffer etwas größer machen. Längere Lücken (Pause) zählen nicht.
    if (underrunAt >= 0) {
      if ((clock - underrunAt) / outRate < 1) {
        target = Math.min(MAX, target + 0.04);
        underruns++;
        calmSince = clock;
      }
      underrunAt = -1;
    }
  }

  function room(n) {
    if (avail() + n >= CAP) r = w + n - CAP + 1;
  }

  return {
    setRate(rate) {
      if (rate > 0 && rate !== srcRate) { srcRate = rate; this.reset(); }
    },
    reset() { r = w; playing = false; underrunAt = -1; },
    push(l, rr) {
      const n = l.length;
      room(n);
      for (let i = 0; i < n; i++) {
        const k = (w + i) & MASK;
        L[k] = l[i];
        R[k] = rr[i];
      }
      w += n;
      noteData();
    },
    pushSilence(n) {
      room(n);
      for (let i = 0; i < n; i++) {
        const k = (w + i) & MASK;
        L[k] = 0;
        R[k] = 0;
      }
      w += n;
      noteData();
    },
    pull(outL, outR) {
      const n = outL.length;
      clock += n;
      const level = avail() / srcRate;
      if (!playing) {
        if (level < target) { outL.fill(0); outR.fill(0); return; }
        playing = true;
      }
      // Viel zu viel im Puffer (z. B. Tab war im Hintergrund): aufholen.
      if (level > target + 0.25) r = w - target * srcRate;
      // Lange ruhig gelaufen: Puffer langsam wieder verkleinern.
      if (clock - calmSince > outRate * 20 && target > MIN) {
        target = Math.max(MIN, target - 0.01);
        calmSince = clock;
      }
      const err = (level - target) / target;
      // Kleine Abweichung (Taktdrift): unhörbar fein nachregeln. Großer
      // Überschuss nach einem Netz-Schub: bis 2 % schneller, damit Ton und
      // Bild nach wenigen Sekunden wieder synchron sind.
      const want = 1 + Math.max(-0.02, Math.min(0.02, err * 0.02));
      ratio += (want - ratio) * 0.02;
      const step = (srcRate / outRate) * ratio;
      for (let i = 0; i < n; i++) {
        if (r + 1 >= w) {
          outL.fill(0, i);
          outR.fill(0, i);
          playing = false;
          underrunAt = clock;
          return;
        }
        const i0 = Math.floor(r);
        const f = r - i0;
        const a = i0 & MASK, b = (i0 + 1) & MASK;
        outL[i] = L[a] + (L[b] - L[a]) * f;
        outR[i] = R[a] + (R[b] - R[a]) * f;
        r += step;
      }
    },
    stats() { return { level: avail() / srcRate, target, underruns }; },
  };
}
