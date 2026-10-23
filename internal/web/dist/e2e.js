/* Ende-zu-Ende im Fenster: dasselbe Paketformat wie internal/e2e (Go).
 *   Kopf (16 Byte, lesbar) + Nonce (12) + AES-256-GCM-Geheimtext.
 *   Zusatzdaten = Kopf mit genullter Absender-Kennung (Byte 4–7), denn die
 *   trägt der Relay selbst ein.
 * Den Schlüssel leitet die App aus Raum-ID und Passwort ab und gibt ihn nur
 * an dieses Fenster (lokal, verlässt den PC nie).
 * Gegenprobe ohne Browser: node internal/web/e2e_check.cjs */
(() => {
  'use strict';

  const HEADER = 16, NONCE = 12;
  const KIND = { video: 1, system: 2, voice: 3, file: 4, chat: 5, state: 6, event: 7 };

  function importKey(raw) {
    return crypto.subtle.importKey('raw', raw, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
  }

  function header(h) {
    const b = new Uint8Array(HEADER);
    const dv = new DataView(b.buffer);
    b[0] = h.kind; b[1] = h.flags || 0;
    dv.setUint32(8, h.seq >>> 0, true);
    dv.setUint16(12, h.idx || 0, true);
    dv.setUint16(14, h.count || 0, true);
    return b;
  }

  function aad(bytes) {
    const a = bytes.slice(0, HEADER);
    a[4] = a[5] = a[6] = a[7] = 0;
    return a;
  }

  // seal: Klartext (Uint8Array) → fertiges Paket (ArrayBuffer).
  async function seal(key, h, plain) {
    const head = header(h);
    const nonce = crypto.getRandomValues(new Uint8Array(NONCE));
    const ct = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv: nonce, additionalData: head }, key, plain));
    const out = new Uint8Array(HEADER + NONCE + ct.length);
    out.set(head, 0);
    out.set(nonce, HEADER);
    out.set(ct, HEADER + NONCE);
    return out.buffer;
  }

  function parse(buf) {
    const b = new Uint8Array(buf);
    const dv = new DataView(b.buffer, b.byteOffset, b.byteLength);
    return {
      kind: b[0], flags: b[1], sender: dv.getUint32(4, true), seq: dv.getUint32(8, true),
      idx: dv.getUint16(12, true), count: dv.getUint16(14, true),
    };
  }

  // open: Paket → { h, plain } – wirft bei Manipulation oder falschem Passwort.
  async function open(key, buf) {
    const b = new Uint8Array(buf);
    if (b.length < HEADER + NONCE + 16) throw new Error('zu kurz');
    const plain = await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: b.subarray(HEADER, HEADER + NONCE), additionalData: aad(b) },
      key, b.subarray(HEADER + NONCE));
    return { h: parse(buf), plain: new Uint8Array(plain) };
  }

  const api = { KIND, importKey, seal, open, parse };
  if (typeof window !== 'undefined') window.MssE2E = api;
  if (typeof module !== 'undefined') module.exports = api;
})();
