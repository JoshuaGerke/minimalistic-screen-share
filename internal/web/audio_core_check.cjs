// Prüft den Ton-Kern ohne Browser: node internal/web/audio_core_check.cjs
// Simuliert 48-kHz-Ton auf einem 44,1-kHz-Gerät, Absender-Uhr 0,2 % zu
// schnell und ein Netz, das Pakete in Schüben liefert.
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const src = fs.readFileSync(path.join(__dirname, 'dist', 'audio-core.js'), 'utf8');
const audioCore = new Function(src + '\nreturn audioCore;')();

const OUT = 44100, IN = 48000, DRIFT = 1.002, BLOCK = 128;
const core = audioCore(OUT, 0.08);
core.setRate(IN);

let phase = 0;
function packet() {
  const n = IN / 50;
  const l = new Float32Array(n), r = new Float32Array(n);
  for (let i = 0; i < n; i++) {
    l[i] = r[i] = Math.sin(phase) * 0.5;
    phase += (2 * Math.PI * 440) / IN;
  }
  return [l, r];
}

const outL = new Float32Array(BLOCK), outR = new Float32Array(BLOCK);
let sent = 0, gaps = 0, maxJump = 0, prev = 0, playing = false;
const seconds = 120;
const blocks = Math.floor((seconds * OUT) / BLOCK);
for (let b = 0; b < blocks; b++) {
  const t = (b * BLOCK) / OUT;
  // Pakete kommen alle 60 ms im Dreierpack, die Absender-Uhr läuft schneller.
  const due = Math.floor((t * DRIFT * 50) / 3) * 3;
  // Nach 30 s hängt das Netz 300 ms, danach kommt alles im Schub.
  const stalled = t > 30 && t < 30.3;
  while (!stalled && sent < due) { const [l, r] = packet(); core.push(l, r); sent++; }
  core.pull(outL, outR);
  for (let i = 0; i < BLOCK; i++) {
    assert(Number.isFinite(outL[i]), 'NaN im Ausgang');
    if (t > 40) {
      if (outL[i] === 0 && prev !== 0 && playing) gaps++;
      maxJump = Math.max(maxJump, Math.abs(outL[i] - prev));
    }
    playing = outL[i] !== 0 || playing;
    prev = outL[i];
  }
}
const st = core.stats();
assert(Math.abs(st.level - st.target) < 0.06, 'Extra-Latenz nach dem Schub nicht abgebaut');
console.log('Puffer', Math.round(st.level * 1000), 'ms, Ziel', Math.round(st.target * 1000), 'ms, Aussetzer', st.underruns);
assert(st.level < st.target + 0.1, 'Puffer läuft trotz Drift voll');
assert(st.underruns >= 1, 'Netz-Aussetzer nicht erkannt');
assert(st.target > 0.08, 'Puffer hat sich nicht vergrößert');
// Ein 440-Hz-Sinus mit Amplitude 0,5 springt pro Sample höchstens ~0,032.
assert(maxJump < 0.05, 'Knacken im Ausgang: Sprung ' + maxJump.toFixed(3));
console.log('ok');
