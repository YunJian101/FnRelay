const fs = require('fs');

async function main() {
  const out = process.argv[2] || '.';
  const token = process.env.SESSION || '';
  const pages = (process.argv[3] || 'overview,rules,certs,settings,logs').split(',');
  const ver = await (await fetch('http://127.0.0.1:9222/json/version')).json();
  const ws = new WebSocket(ver.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.onopen = resolve;
    ws.onerror = reject;
  });
  let id = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    let m;
    try { m = JSON.parse(ev.data); } catch (e) { return; }
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m);
      pending.delete(m.id);
    }
  };
  const send = (method, params = {}, sessionId) => new Promise((resolve) => {
    const i = ++id;
    pending.set(i, resolve);
    const msg = { id: i, method, params };
    if (sessionId) msg.sessionId = sessionId;
    ws.send(JSON.stringify(msg));
  });

  const t = await send('Target.createTarget', { url: 'about:blank' });
  const targetId = t.result.targetId;
  const a = await send('Target.attachToTarget', { targetId, flatten: true });
  const sid = a.result.sessionId;

  const mobile = process.env.MOBILE === '1';
  const baseUrl = process.env.BASE_URL || 'http://127.0.0.1:18899/';
  const theme = process.env.THEME || '';
  await send('Page.enable', {}, sid);
  await send('Network.enable', {}, sid);
  await send('Network.setCacheDisabled', { cacheDisabled: true }, sid);
  await send('Emulation.setDeviceMetricsOverride', mobile
    ? { width: 390, height: 844, deviceScaleFactor: 2, mobile: true }
    : { width: 1440, height: 980, deviceScaleFactor: 1, mobile: false }, sid);
  if (theme) {
    await send('Page.addScriptToEvaluateOnNewDocument', {
      source: "try{localStorage.setItem('fnrelay_theme','" + theme + "')}catch(e){}",
    }, sid);
  }
  if (token) {
    await send('Network.setCookie', { name: 'fnrelay_session', value: token, domain: '127.0.0.1', path: '/' }, sid);
  }

  for (const page of pages) {
    if (page === 'login') {
      await send('Network.clearBrowserCookies', {}, sid);
      await send('Page.navigate', { url: baseUrl }, sid);
      await new Promise((r) => setTimeout(r, 1400));
    } else if (page === 'appearance') {
      await send('Page.navigate', { url: baseUrl + '#/settings' }, sid);
      await new Promise((r) => setTimeout(r, 1200));
      await send('Runtime.evaluate', { expression: 'window.scrollTo(0, 1150)' }, sid);
      await new Promise((r) => setTimeout(r, 600));
    } else if (page === 'rule-modal') {
      await send('Page.navigate', { url: baseUrl + '#/rules' }, sid);
      await new Promise((r) => setTimeout(r, 1200));
      await send('Runtime.evaluate', { expression: 'document.querySelector("[data-action=rule-new]").click()' }, sid);
      await new Promise((r) => setTimeout(r, 600));
    } else {
      await send('Page.navigate', { url: baseUrl + '#/' + page }, sid);
      await new Promise((r) => setTimeout(r, 1400));
    }
    const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }, sid);
    fs.writeFileSync(out + '/' + page + '.png', Buffer.from(shot.result.data, 'base64'));
    console.log('shot', page);
  }
  ws.close();
}

main().catch((e) => { console.error(e); process.exit(1); });
