// Measures the remote desktop's click-to-picture latency with a real browser.
//
// It starts the flashing test window (test/winapp -flash) on the agent's
// desktop, opens /remote/ in headless Chrome, clicks through the page and
// times how long the picture takes to change. Needs Node 22 or newer.
//
//   node test/remotelatency/latency.mjs --agent http://HOST:7070 \
//        --winapp 'C:\path\winapp.exe' [--samples 40] [--quality 60,0]
//
// Times are milliseconds from sending the click:
//   received  the last packet of the changed frame arrived in the browser
//   shown     the browser expects that frame on screen
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const args = Object.fromEntries(process.argv.slice(2).join(' ').split('--').filter(Boolean).map((a) => { const [k, ...v] = a.trim().split(' '); return [k, v.join(' ') || true]; }));
const agent = args.agent || 'http://127.0.0.1:7070';
const samples = Number(args.samples || 40);
const quality = args.quality || '30,0';
const chrome = args.chrome || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function api(method, path, body) {
  const res = await fetch(agent + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  const reply = await res.json();
  if (!reply.ok) throw new Error(`${method} ${path}: ${JSON.stringify(reply.error)}`);
  return reply.result;
}

// The page, driven from inside: click the middle of the remote display and
// wait for a frame in which the middle of the picture changed brightness.
const measure = async (n) => {
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  for (let i = 0; i < 150 && !(session && video.videoWidth); i++) await wait(100);
  if (!session || !video.videoWidth) return { error: 'the page did not connect: ' + document.getElementById('state').textContent };
  const c = document.createElement('canvas'); c.width = c.height = 8;
  const g = c.getContext('2d', { willReadFrequently: true });
  const bright = () => {
    g.drawImage(video, video.videoWidth / 2 - 40, video.videoHeight / 2 - 40, 80, 80, 0, 0, 8, 8);
    const d = g.getImageData(0, 0, 8, 8).data; let s = 0;
    for (let i = 0; i < d.length; i += 4) s += d[i] + d[i + 1] + d[i + 2];
    return s / 192 > 128;
  };
  let onFrame = null;
  const loop = (now, meta) => { if (onFrame) onFrame(now, meta); video.requestVideoFrameCallback(loop); };
  video.requestVideoFrameCallback(loop);
  const d = session.display, x = d.x + (d.w >> 1), y = d.y + (d.h >> 1);
  await wait(3000);   // let the stream and the browser's buffer settle
  const out = [];
  for (let i = 0; i < n; i++) {
    await wait(350 + Math.random() * 300);
    const before = bright(), t0 = performance.now();
    sendInput({ t: 'md', b: 0, x, y }); sendInput({ t: 'mu', b: 0, x, y });
    out.push(await new Promise((done) => {
      const timer = setTimeout(() => { onFrame = null; done(null); }, 3000);
      onFrame = (now, meta) => {
        if (bright() === before) return;
        clearTimeout(timer); onFrame = null;
        done({ received: meta.receiveTime ? meta.receiveTime - t0 : null, shown: meta.expectedDisplayTime - t0, callback: now - t0 });
      };
    }));
  }
  const report = await pc.getStats(); let v = {};
  report.forEach((s) => { if (s.type === 'inbound-rtp' && s.kind === 'video') v = s; });
  return { samples: out, width: video.videoWidth, height: video.videoHeight, fps: v.framesPerSecond, decoder: v.decoderImplementation,
    powerEfficient: v.powerEfficientDecoder, dropped: v.framesDropped, lost: v.packetsLost, nack: v.nackCount, pli: v.pliCount,
    jitterBufferMs: v.jitterBufferEmittedCount ? Math.round(v.jitterBufferDelay / v.jitterBufferEmittedCount * 1000) : null,
    decodeMs: v.framesDecoded ? Math.round(v.totalDecodeTime / v.framesDecoded * 1000 * 10) / 10 : null, state: document.getElementById('state').textContent };
};

const pct = (a, p) => a.length ? Math.round(a[Math.min(a.length - 1, Math.floor(a.length * p))]) : null;
const summary = (a) => { a = a.filter((x) => x !== null).sort((x, y) => x - y); return `min ${pct(a, 0)}  p50 ${pct(a, 0.5)}  p90 ${pct(a, 0.9)}  max ${pct(a, 0.999)}`; };

let chromeProc, profile, appPid;
try {
  if (args.winapp) {
    const temp = (await api('GET', '/v1/file/known_dirs')).temp;
    appPid = (await api('POST', '/v1/process/launch', { argv: [args.winapp, '-flash', '-log', temp.replace(/\\$/, '') + '\\winapp-latency.log'] })).pid;
    await sleep(1500);
    // The clicks go to the middle of the screen: never send them unless the
    // test window is what will receive them (an open Start menu covers it).
    // Another application may hold the foreground; ask for it.
    await api('POST', '/v1/window/activate', { title: 'vm-agent-dpi-test', match: 'exact', pick: 'first' }).catch(() => null);
    await sleep(300);
    const active = (await api('GET', '/v1/window/active').catch(() => null))?.title;
    if (active !== 'vm-agent-dpi-test') throw new Error(`the test window is not in front (active window: ${active ?? 'none'}); nothing was clicked`);
  }
  profile = mkdtempSync(join(tmpdir(), 'vmagent-latency-'));
  const port = 9300 + Math.floor(Math.random() * 500);
  chromeProc = spawn(chrome, ['--headless=new', '--no-first-run', `--user-data-dir=${profile}`, '--autoplay-policy=no-user-gesture-required',
    '--window-size=1600,1040', `--remote-debugging-port=${port}`, 'about:blank'], { stdio: 'ignore' });
  let target;
  for (let i = 0; i < 50 && !target; i++) {
    await sleep(200);
    try { target = (await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()).find((t) => t.type === 'page'); } catch (e) { /* not up yet */ }
  }
  if (!target) throw new Error('Chrome did not start');
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((ok, fail) => { ws.onopen = ok; ws.onerror = fail; });
  let id = 0; const pending = new Map();
  ws.onmessage = (e) => { const m = JSON.parse(e.data); if (pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } };
  const cdp = (method, params) => new Promise((done) => { pending.set(++id, done); ws.send(JSON.stringify({ id, method, params })); });
  await cdp('Page.enable');
  await cdp('Page.addScriptToEvaluateOnNewDocument', { source: `try { localStorage.setItem('vmagent.quality', ${JSON.stringify(quality)}); } catch (e) {}` });
  await cdp('Page.navigate', { url: `${agent}/remote/?name=latency-test&auto` });
  await sleep(1500);
  const r = await cdp('Runtime.evaluate', { expression: `(${measure})(${samples})`, awaitPromise: true, returnByValue: true });
  const v = r.result?.result?.value;
  if (!v || v.error) throw new Error(v?.error || JSON.stringify(r.result?.exceptionDetails || r));
  const ok = v.samples.filter(Boolean);
  const info = (await api('GET', '/v1/remote/sessions')).sessions[0] || {};
  console.log(`quality ${quality}  pipeline ${info.capture}/${info.encoder}  picture ${v.width}x${v.height}  ${Math.round(v.fps)} fps  decoder ${v.decoder || '?'} (hardware: ${v.powerEfficient})`);
  console.log(`samples ${ok.length}/${v.samples.length}  dropped ${v.dropped}  lost ${v.lost}  nack ${v.nack}  pli ${v.pli}  browser buffer ${v.jitterBufferMs} ms  decode ${v.decodeMs} ms`);
  console.log(`received  ${summary(ok.map((s) => s.received))}`);
  console.log(`shown     ${summary(ok.map((s) => s.shown))}`);
  await cdp('Runtime.evaluate', { expression: 'release()' });
  await sleep(300);
} catch (e) {
  console.error('latency:', e.message); process.exitCode = 1;
} finally {
  if (chromeProc) chromeProc.kill();
  if (appPid) await api('POST', '/v1/process/kill', { pid: appPid, force: true }).catch((e) => console.error(e.message));
  if (profile) { await sleep(500); rmSync(profile, { recursive: true, force: true }); }
}
