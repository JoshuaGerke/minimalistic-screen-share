/* Overlay für den Sprachkanal: schwebt über allen Fenstern und zeigt, wer
 * gerade spricht.
 *  - Windows: ein echtes, halbtransparentes Fenster der App (frei
 *    verschiebbar, Deckkraft, Größe, Klicks durchlassen).
 *  - Sonst: ein Bild-in-Bild-Fenster des Browsers (Chrome, Brave, Edge).
 *    Unter Hyprland lässt es sich per Fensterregel anpinnen und
 *    durchsichtig machen, z. B. "windowrule = opacity 0.8, title:^(Sprachkanal)$".
 *
 *   const o = MssOverlay.create({ base: 'http://127.0.0.1:7777', platform: () => 'windows' });
 *   o.toggle(snapshot); o.publish(snapshot); */
(() => {
  'use strict';

  function create(opts) {
    let nativeOn = false;
    let pip = null;
    let last = null;
    let sendTimer = null, pending = null;

    const base = () => (typeof opts.base === 'function' ? opts.base() : opts.base);
    // Das native Overlay gibt es nur unter Windows und nur, wenn die App erreichbar ist.
    const native = () => opts.platform() === 'windows' && base() !== null;

    function post(path, body) {
      return fetch(base() + path, { method: 'POST', body: JSON.stringify(body) }).catch(() => {});
    }

    // Höchstens zehnmal pro Sekunde an die App schicken.
    function sendState(snap) {
      pending = snap;
      if (sendTimer) return;
      sendTimer = setTimeout(() => {
        sendTimer = null;
        post('/api/overlay/state', pending);
      }, 100);
    }

    async function toggle(snap) {
      last = snap;
      if (native()) {
        nativeOn = !nativeOn;
        await post('/api/overlay', { on: nativeOn });
        if (nativeOn) sendState(snap);
        return;
      }
      if (pip) { pip.close(); pip = null; return; }
      if (!window.documentPictureInPicture) {
        alert('Das Overlay braucht hier Chrome, Brave oder Edge (Bild-in-Bild für Seiten).');
        return;
      }
      pip = await documentPictureInPicture.requestWindow({ width: 240, height: 260 });
      pip.document.title = 'Sprachkanal';
      const style = pip.document.createElement('style');
      style.textContent = `
        body { margin: 0; background: #111214; color: #f2f3f5; font: 14px system-ui, "Segoe UI", sans-serif; }
        .list { padding: 8px; display: flex; flex-direction: column; gap: 6px; }
        .m { display: flex; align-items: center; gap: 8px; padding: 4px 6px; border-radius: 8px; background: rgba(255,255,255,.04); }
        .a { width: 26px; height: 26px; border-radius: 50%; display: grid; place-items: center; font-weight: 700; font-size: 12px; background: #2a2b30; }
        .s .a { box-shadow: 0 0 0 3px #23a55a, 0 0 10px rgba(35,165,90,.8); }
        .n { flex: 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
        .f { font-size: 11px; color: #f23f43; }
        .e { color: #8b919a; padding: 12px; font-size: 13px; }`;
      pip.document.head.appendChild(style);
      pip.document.body.innerHTML = '<div class="list"></div>';
      pip.addEventListener('pagehide', () => { pip = null; });
      renderPip(snap);
    }

    function renderPip(snap) {
      if (!pip) return;
      const list = pip.document.querySelector('.list');
      const o = snap.overlay || {};
      list.style.opacity = String((o.opacity || 100) / 100);
      list.style.zoom = String((o.scale || 100) / 100);
      list.innerHTML = '';
      const members = snap.members.filter((m) => !o.onlySpeaking || m.speaking);
      if (!members.length) {
        list.innerHTML = '<div class="e">Niemand im Sprachkanal</div>';
        return;
      }
      for (const m of members) {
        const row = pip.document.createElement('div');
        row.className = 'm' + (m.speaking ? ' s' : '');
        const a = pip.document.createElement('span');
        a.className = 'a';
        a.textContent = (m.name || '?').slice(0, 1).toUpperCase();
        const n = pip.document.createElement('span');
        n.className = 'n';
        n.textContent = m.name;
        row.append(a, n);
        if (m.muted || m.deaf) {
          const f = pip.document.createElement('span');
          f.className = 'f';
          f.textContent = m.deaf ? 'taub' : 'stumm';
          row.append(f);
        }
        list.append(row);
      }
    }

    function publish(snap) {
      last = snap;
      if (nativeOn && native()) sendState(snap);
      renderPip(snap);
    }

    return { toggle, publish, get active() { return nativeOn || !!pip; } };
  }

  window.MssOverlay = { create };
})();
