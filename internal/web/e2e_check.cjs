// Gegenprobe Go ↔ Browser: node internal/web/e2e_check.cjs
// Öffnet ein in Go versiegeltes Paket (Testvektor aus internal/e2e) mit dem
// Browser-Code und prüft Manipulationsschutz und den Weg zurück.
'use strict';
const assert = require('assert');
const path = require('path');
if (!globalThis.crypto) globalThis.crypto = require('crypto').webcrypto;
const fs = require('fs');
const mod = { exports: {} };
new Function('module', fs.readFileSync(path.join(__dirname, 'dist', 'e2e.js'), 'utf8'))(mod);
const E2E = mod.exports;

const hex = (s) => Uint8Array.from(Buffer.from(s, 'hex'));
// Aus: MSS_E2E_VECTOR=… go test -run Vector ./internal/e2e
const ENC = 'd0f3bf9b756478b0e93a36955d4bfc1446d0d2bf35bf4cdd416185b765af27dd';
const MSG = '05000000090000000100000002000300be39150dee3ab89fe20b79d5d42ed5f691a5e6b940c0d3347bf637dbc937a4747efdf6ce267d41988740';

(async () => {
  const key = await E2E.importKey(hex(ENC));
  const { h, plain } = await E2E.open(key, hex(MSG).buffer);
  assert.strictEqual(new TextDecoder().decode(plain), 'Grüße aus Go');
  assert.deepStrictEqual([h.kind, h.sender, h.seq, h.idx, h.count], [5, 9, 1, 2, 3]);

  const bad = hex(MSG); bad[12] ^= 1; // Stücknummer verfälscht
  await assert.rejects(E2E.open(key, bad.buffer));

  const sealed = new Uint8Array(await E2E.seal(key, { kind: 3, flags: 1, seq: 77 }, new TextEncoder().encode('zurück')));
  sealed[4] = 5; // der Relay stempelt den Absender
  const back = await E2E.open(key, sealed.buffer);
  assert.strictEqual(new TextDecoder().decode(back.plain), 'zurück');
  assert.strictEqual(back.h.sender, 5);
  console.log('ok');
})().catch((e) => { console.error(e); process.exit(1); });
