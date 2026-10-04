'use strict';
// RemoteDesk viewer: device list + remote session (WebCodecs H.264 → canvas).

const $ = (id) => document.getElementById(id);

const QUALITY = {
  eco: { fps: 24, maxw: 1280, kbps: 1500 },
  balanced: { fps: 30, maxw: 1920, kbps: 3500 },
  sharp: { fps: 60, maxw: 1920, kbps: 8000 },
  max: { fps: 60, maxw: 3840, kbps: 16000 },
};
const ICE_SERVERS = [{ urls: ['stun:stun.l.google.com:19302', 'stun:stun.cloudflare.com:3478'] }];
const P2P_SUPPORTED = 'RTCPeerConnection' in window && 'requestVideoFrameCallback' in HTMLVideoElement.prototype;
const IS_MAC_VIEWER = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

// ---------------------------------------------------------------- prefs (per browser)

const prefs = (() => {
  let data = {};
  try { data = JSON.parse(localStorage.getItem('rd_prefs') || '{}'); } catch { /* private mode */ }
  return {
    get: (k, d) => (k in data ? data[k] : d),
    set: (k, v) => { data[k] = v; try { localStorage.setItem('rd_prefs', JSON.stringify(data)); } catch { /* ignore */ } },
  };
})();

function show(name) {
  for (const s of ['login', 'devices', 'admin', 'session']) $(s).hidden = s !== name;
}

// ---------------------------------------------------------------- login

$('login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const r = await fetch('/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token: $('login-token').value }),
  });
  if (r.ok) { $('login-token').value = ''; route(); return; }
  $('login-error').hidden = false;
  $('login-error').textContent = r.status === 429 ? 'Sai quá nhiều lần, thử lại sau 15 phút.' : 'Mã không đúng.';
});

// ---------------------------------------------------------------- devices

let devicesTimer = null;

async function showDevices() {
  show('devices');
  loadMe();
  $('unsupported').hidden = 'VideoDecoder' in window;
  await refreshDevices();
  loadInstall();
  clearInterval(devicesTimer);
  devicesTimer = setInterval(refreshDevices, 3000);
}

async function refreshDevices() {
  let list;
  try {
    const r = await fetch('/api/agents', { cache: 'no-store' });
    if (r.status === 401) { clearInterval(devicesTimer); show('login'); return; }
    list = await r.json();
  } catch { return; }
  const ul = $('device-list');
  ul.replaceChildren(...list.map((a) => {
    const li = document.createElement('li');
    li.className = 'device';
    li.innerHTML = `<span class="os"></span><span class="meta"><div class="title"></div><div class="sub"></div></span><span class="dot"></span>`;
    li.querySelector('.os').textContent = a.os === 'darwin' ? '' : a.os === 'windows' ? '⊞' : '🖥';
    li.querySelector('.title').textContent = a.name;
    li.querySelector('.sub').textContent = `${a.os === 'darwin' ? 'macOS' : a.os === 'windows' ? 'Windows' : a.os} · agent ${a.ver}${a.watching ? ` · ${a.watching} người đang xem` : ''}`;
    li.querySelector('.dot').classList.toggle('busy', a.busy);
    li.onclick = () => { location.hash = `#/d/${encodeURIComponent(a.id)}`; };
    return li;
  }));
  $('device-empty').hidden = list.length > 0;
  if (!list.length) $('install').open = true;
}

async function loadInstall() {
  try {
    const r = await fetch('/api/install', { cache: 'no-store' });
    if (!r.ok) return;
    const i = await r.json();
    $('dl-win-exe').href = i.full.windowsExe;
    $('dl-win-exe-agent').href = i.agent.windowsExe;
    $('dl-mac-pkg').href = i.full.macPkg;
    $('dl-mac-pkg-agent').href = i.agent.macPkg;
    $('cmd-win-full').textContent = i.full.windowsPs;
    $('cmd-win-agent').textContent = i.agent.windowsPs;
    $('cmd-mac-full').textContent = i.full.mac;
    $('cmd-mac-agent').textContent = i.agent.mac;
  } catch { /* offline */ }
}

for (const b of document.querySelectorAll('[data-copy]')) {
  b.addEventListener('click', async () => {
    const text = $(b.dataset.copy).textContent;
    try { await navigator.clipboard.writeText(text); b.textContent = 'Đã copy'; } catch { b.textContent = 'Lỗi'; }
    setTimeout(() => { b.textContent = 'Copy'; }, 1500);
  });
}

// ---------------------------------------------------------------- session

let S = null; // active session state

function annexBCodec(bytes) {
  // Find the SPS (NAL type 7) and build an avc1.PPCCLL codec string from it.
  for (let i = 0; i + 4 < bytes.length; i++) {
    if (bytes[i] === 0 && bytes[i + 1] === 0 && bytes[i + 2] === 1) {
      const n = i + 3;
      if ((bytes[n] & 0x1f) === 7 && n + 3 < bytes.length) {
        const hex = (v) => v.toString(16).padStart(2, '0').toUpperCase();
        return `avc1.${hex(bytes[n + 1])}${hex(bytes[n + 2])}${hex(bytes[n + 3])}`;
      }
      i = n;
    }
  }
  return null;
}

function startSession(id) {
  stopSession();
  clearInterval(devicesTimer);
  show('session');
  const canvas = $('screen');
  S = {
    id,
    ws: null,
    closed: false,
    agent: null,
    decoder: null,
    codec: null,
    waitingKey: true,
    latest: null,
    rafPending: false,
    vw: 0,
    vh: 0,
    ctx: canvas.getContext('2d', { alpha: false, desynchronized: true }),
    bytes: 0,
    frames: 0,
    pressed: new Set(),
    gate: Promise.resolve(),
    lastClip: '',
    lastKfReq: 0,
    retry: null,
    statsTimer: setInterval(updateStats, 1000),
    control: false, // every session starts view-only
    // Direct path (WebRTC): video arrives as an RTP track, input leaves over a
    // data channel. Until the first P2P frame shows, the relay stream is used.
    pc: null,
    dc: null,
    video: null,
    p2pLive: false,
    p2pTimer: null,
    p2pRetry: null,
    p2pFails: 0,
    pendingCands: [],
    p2pStats: null, // { bytes, at } of the last getStats sample
  };
  $('chk-p2p').checked = prefs.get('p2p', true);
  $('chk-p2p').parentElement.hidden = !P2P_SUPPORTED;
  updateMode();
  $('sel-quality').value = prefs.get('quality', 'balanced');
  $('sess-name').textContent = id;
  setOverlay('Đang kết nối…');
  connect();
  $('stage').focus();
}

function stopSession() {
  if (!S) return;
  S.closed = true;
  clearTimeout(S.retry);
  clearInterval(S.statsTimer);
  stopP2P(null);
  try { S.ws && S.ws.close(); } catch { /* ignore */ }
  closeDecoder();
  if (S.latest) S.latest.close();
  S = null;
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
}

function setOverlay(text) {
  const o = $('overlay');
  o.hidden = !text;
  o.textContent = text || '';
}

function connect() {
  const q = QUALITY[prefs.get('quality', 'balanced')] || QUALITY.balanced;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/ws/view?id=${encodeURIComponent(S.id)}&fps=${q.fps}&maxw=${q.maxw}&kbps=${q.kbps}`);
  ws.binaryType = 'arraybuffer';
  S.ws = ws;
  S.waitingKey = true;
  ws.onmessage = (ev) => {
    if (!S || S.ws !== ws) return;
    if (typeof ev.data === 'string') onControl(JSON.parse(ev.data));
    else onVideo(ev.data);
  };
  ws.onclose = (ev) => {
    if (!S || S.ws !== ws || S.closed) return;
    stopP2P(null); // signaling is gone; renegotiate after reconnecting
    S.p2pFails = 0;
    closeDecoder();
    setOverlay(ev.code === 4404 ? 'Máy đang offline — đang chờ kết nối lại…' : 'Mất kết nối — đang thử lại…');
    S.retry = setTimeout(connect, 2000);
  };
}

function sendNow(obj) {
  if (S && S.ws && S.ws.readyState === WebSocket.OPEN) S.ws.send(JSON.stringify(obj));
}

// Input (and the clipboard text that must precede a paste) takes the P2P data
// channel when it is open, the relay otherwise.
function sendInput(obj) {
  if (S && S.dc && S.dc.readyState === 'open') S.dc.send(JSON.stringify(obj));
  else sendNow(obj);
}

// All input goes through one promise chain so a clipboard read can hold back
// the following keystrokes (paste must arrive after the clipboard text).
function send(obj) {
  if (!S || !S.control) return; // view-only: nothing reaches the remote machine
  S.gate = S.gate.then(() => sendInput(obj));
}

function onControl(m) {
  switch (m.t) {
    case 'hello': {
      S.agent = m.agent;
      $('sess-name').textContent = m.agent.name;
      document.title = `${m.agent.name} — RemoteDesk`;
      const key = `swap:${m.agent.id}`;
      const def = IS_MAC_VIEWER !== (m.agent.os === 'darwin'); // swap by default across OSes
      $('chk-swap').checked = prefs.get(key, def);
      S.zoom = prefs.get(`zoom:${m.agent.id}`, 'fit');
      $('sel-zoom').value = S.zoom;
      fitCanvas();
      setOverlay('Đang chờ hình ảnh…');
      startP2P();
      break;
    }
    case 'rtc':
      onRtc(m);
      break;
    case 'rtc-reset': // the agent reconnected: its peer connections are gone
      stopP2P(null);
      S.p2pFails = 0;
      S.p2pRetry = setTimeout(startP2P, 1000);
      break;
    case 'info':
      $('sess-stats').dataset.enc = m.enc || '';
      break;
    case 'clip':
      S.lastClip = m.text;
      if (navigator.clipboard && document.hasFocus()) navigator.clipboard.writeText(m.text).catch(() => {});
      break;
    case 'error':
      setOverlay(m.msg);
      break;
  }
}

function setupDecoder(codec) {
  closeDecoder();
  const dec = new VideoDecoder({
    output: onFrame,
    error: (e) => { console.warn('decoder error', e); if (S && S.decoder === dec) requestKeyframe(); },
  });
  dec.configure({ codec, optimizeForLatency: true });
  S.decoder = dec;
  S.codec = codec;
}

function closeDecoder() {
  if (S && S.decoder) {
    try { if (S.decoder.state !== 'closed') S.decoder.close(); } catch { /* ignore */ }
    S.decoder = null;
    S.codec = null;
  }
}

function requestKeyframe() {
  closeDecoder();
  S.waitingKey = true;
  const now = performance.now();
  if (now - S.lastKfReq > 1000) { S.lastKfReq = now; sendNow({ t: 'kf' }); }
}

function onVideo(buf) {
  if (S.p2pLive) return; // stray relay frames sent before it saw our switch
  S.bytes += buf.byteLength;
  const dv = new DataView(buf);
  if (dv.getUint8(0) !== 1) return;
  const key = (dv.getUint8(1) & 1) === 1;
  const ts = dv.getUint32(2);
  const data = new Uint8Array(buf, 6);
  if (key) {
    const codec = annexBCodec(data) || S.codec || 'avc1.640028';
    if (!S.decoder || S.decoder.state === 'closed' || codec !== S.codec) setupDecoder(codec);
    S.waitingKey = false;
  }
  if (S.waitingKey || !S.decoder) return;
  // Joining mid-stream replays up to one GOP (~90 frames) at once; only a much
  // bigger backlog means we truly can't keep up.
  if (S.decoder.decodeQueueSize > 240) { requestKeyframe(); return; }
  try {
    S.decoder.decode(new EncodedVideoChunk({ type: key ? 'key' : 'delta', timestamp: ts * 1000, data }));
  } catch (e) {
    console.warn('decode failed', e);
    requestKeyframe();
  }
}

function onFrame(frame) {
  if (!S) { frame.close(); return; }
  if (S.latest) S.latest.close(); // only ever paint the newest frame
  S.latest = frame;
  if (!S.rafPending) { S.rafPending = true; requestAnimationFrame(paint); }
}

function paint() {
  if (!S) return;
  S.rafPending = false;
  const f = S.latest;
  S.latest = null;
  if (!f) return;
  draw(f, f.displayWidth, f.displayHeight);
  f.close();
}

// draw puts one picture (a decoded VideoFrame, or the P2P <video>) on the canvas.
function draw(src, w, h) {
  const canvas = $('screen');
  if (w !== S.vw || h !== S.vh) {
    S.vw = canvas.width = w;
    S.vh = canvas.height = h;
    fitCanvas();
  }
  S.ctx.drawImage(src, 0, 0);
  if (S.frames === 0) setOverlay('');
  S.frames++;
}

// ---------------------------------------------------------------- P2P (WebRTC)

function p2pWanted() {
  return P2P_SUPPORTED && prefs.get('p2p', true);
}

async function startP2P() {
  if (!S || S.pc || !p2pWanted() || !S.ws || S.ws.readyState !== WebSocket.OPEN) return;
  clearTimeout(S.p2pRetry);
  const pc = new RTCPeerConnection({ iceServers: ICE_SERVERS, bundlePolicy: 'max-bundle' });
  S.pc = pc;
  S.pendingCands = [];
  const dc = pc.createDataChannel('input', { ordered: true });
  dc.onopen = () => { if (S && S.pc === pc) S.dc = dc; };
  dc.onclose = () => { if (S && S.dc === dc) S.dc = null; };
  pc.addTransceiver('video', { direction: 'recvonly' });

  // The <video> only decodes; frames are copied onto the canvas so zoom,
  // pointer mapping and touch handling stay the same as on the relay path.
  const video = document.createElement('video');
  video.className = 'p2p-video';
  video.muted = true;
  video.playsInline = true;
  video.autoplay = true;
  $('stage').appendChild(video);
  S.video = video;
  const onVideoFrame = () => {
    if (!S || S.video !== video) return;
    if (video.videoWidth) {
      if (!S.p2pLive) goLive();
      draw(video, video.videoWidth, video.videoHeight);
    }
    video.requestVideoFrameCallback(onVideoFrame);
  };
  video.requestVideoFrameCallback(onVideoFrame);

  pc.ontrack = (e) => {
    // Render as soon as a frame decodes; there is no audio to sync with.
    try { e.receiver.jitterBufferTarget = 0; } catch { /* older browsers */ }
    try { e.receiver.playoutDelayHint = 0; } catch { /* non-Chrome */ }
    video.srcObject = new MediaStream([e.track]);
    video.play().catch(() => {});
  };
  pc.onicecandidate = (e) => {
    if (e.candidate && S && S.pc === pc) sendNow({ t: 'rtc', cand: e.candidate.toJSON() });
  };
  let dropTimer = null;
  pc.onconnectionstatechange = () => {
    if (!S || S.pc !== pc) return;
    const st = pc.connectionState;
    clearTimeout(dropTimer);
    if (st === 'failed') stopP2P('kết nối thất bại');
    // A brief blip can recover, but don't leave a frozen picture for long.
    else if (st === 'disconnected') dropTimer = setTimeout(() => { if (S && S.pc === pc) stopP2P('mất kết nối'); }, 3000);
  };
  // No picture within this time (blocked UDP, symmetric NAT…): stay on the relay.
  S.p2pTimer = setTimeout(() => { if (S && S.pc === pc && !S.p2pLive) stopP2P('quá thời gian'); }, 15000);

  try {
    await pc.setLocalDescription(await pc.createOffer());
    if (S && S.pc === pc) sendNow({ t: 'rtc', sdp: pc.localDescription.toJSON() });
  } catch (e) {
    stopP2P(String(e));
  }
}

async function onRtc(m) {
  const pc = S.pc;
  if (!pc) return;
  if (m.err) { stopP2P(m.err); return; }
  try {
    if (m.sdp) {
      await pc.setRemoteDescription(m.sdp);
      for (const c of S.pendingCands) await pc.addIceCandidate(c);
      S.pendingCands = [];
    } else if (m.cand) {
      // The agent's first candidates can overtake its answer.
      if (pc.remoteDescription) await pc.addIceCandidate(m.cand);
      else S.pendingCands.push(m.cand);
    }
  } catch (e) {
    console.warn('p2p signaling', e);
  }
}

function goLive() {
  S.p2pLive = true;
  S.p2pFails = 0;
  clearTimeout(S.p2pTimer);
  sendNow({ t: 'p2p', on: true });
  closeDecoder();
  if (S.latest) { S.latest.close(); S.latest = null; }
  console.info('P2P: video now direct');
}

// stopP2P tears the direct path down. With a reason it is a failure: fall back
// to the relay stream and try again later (backing off).
function stopP2P(reason) {
  if (!S) return;
  clearTimeout(S.p2pTimer);
  clearTimeout(S.p2pRetry);
  const wasLive = S.p2pLive;
  S.p2pLive = false;
  S.dc = null;
  S.p2pStats = null;
  if (S.pc) { try { S.pc.close(); } catch { /* ignore */ } S.pc = null; }
  if (S.video) { S.video.srcObject = null; S.video.remove(); S.video = null; }
  if (wasLive) {
    S.waitingKey = true;
    sendNow({ t: 'p2p', on: false }); // relay resends a keyframe
  }
  if (reason == null) return;
  console.info(`P2P: ${reason} — using relay`);
  S.p2pFails++;
  if (S.p2pFails <= 5) S.p2pRetry = setTimeout(startP2P, Math.min(60000, 3000 * 2 ** S.p2pFails));
}

async function p2pStats() {
  let bytes = 0, rtt = null, kind = '';
  const report = await S.pc.getStats();
  const pairs = new Map();
  report.forEach((r) => {
    if (r.type === 'inbound-rtp' && r.kind === 'video') bytes = r.bytesReceived;
    if (r.type === 'candidate-pair' && r.nominated && r.state === 'succeeded') pairs.set(r.localCandidateId, r);
  });
  for (const [localId, pair] of pairs) {
    rtt = pair.currentRoundTripTime;
    const local = report.get(localId);
    const remote = report.get(pair.remoteCandidateId);
    kind = local && remote && local.candidateType === 'host' && remote.candidateType === 'host' ? 'LAN' : '';
  }
  const now = performance.now();
  const prev = S.p2pStats;
  S.p2pStats = { bytes, at: now };
  const kbps = prev ? Math.round(((bytes - prev.bytes) * 8) / (now - prev.at)) : 0;
  return { kbps, rtt, kind };
}

// Zoom: 'fit' (whole screen visible), 'width' (fill horizontally, scroll
// vertically) or a fixed factor where 1 = one remote pixel per CSS pixel.
function fitCanvas() {
  if (!S || !S.vw) return;
  const st = $('stage');
  const z = S.zoom || 'fit';
  let scale;
  if (z === 'fit') scale = Math.min(st.clientWidth / S.vw, st.clientHeight / S.vh);
  else if (z === 'width') scale = st.clientWidth / S.vw;
  else scale = Number(z) || 1;
  const c = $('screen');
  c.style.width = `${Math.floor(S.vw * scale)}px`;
  c.style.height = `${Math.floor(S.vh * scale)}px`;
  st.classList.toggle('zoomed', z !== 'fit');
}

function setZoom(z) {
  if (!S) return;
  // Keep the point at the centre of the view in place while zooming.
  const st = $('stage');
  const cx = (st.scrollLeft + st.clientWidth / 2) / Math.max(1, st.scrollWidth);
  const cy = (st.scrollTop + st.clientHeight / 2) / Math.max(1, st.scrollHeight);
  S.zoom = z;
  if (S.agent) prefs.set(`zoom:${S.agent.id}`, z);
  $('sel-zoom').value = z;
  fitCanvas();
  st.scrollLeft = cx * st.scrollWidth - st.clientWidth / 2;
  st.scrollTop = cy * st.scrollHeight - st.clientHeight / 2;
}
window.addEventListener('resize', fitCanvas);
document.addEventListener('fullscreenchange', () => {
  setTimeout(fitCanvas, 50);
  if (!document.fullscreenElement && navigator.keyboard && navigator.keyboard.unlock) navigator.keyboard.unlock();
});

async function updateStats() {
  if (!S) return;
  const s = S;
  let kbps = Math.round((s.bytes * 8) / 1000);
  const fps = s.frames;
  s.bytes = 0;
  s.frames = 0;
  let path = 'relay';
  if (s.p2pLive && s.pc) {
    try {
      const st = await p2pStats();
      kbps = st.kbps;
      path = `P2P${st.kind ? ' ' + st.kind : ''}${st.rtt != null ? ` ${Math.round(st.rtt * 1000)} ms` : ''}`;
    } catch { path = 'P2P'; }
    if (S !== s) return;
  }
  const enc = $('sess-stats').dataset.enc;
  $('sess-stats').textContent = s.vw ? `${s.vw}×${s.vh} · ${fps} fps · ${kbps} kbps · ${path}${enc ? ' · ' + enc : ''}` : '';
}

// ---------------------------------------------------------------- mouse

const canvas = $('screen');
const stage = $('stage');
let pendingMove = null;
let wheelAcc = { dx: 0, dy: 0, scheduled: false };

function norm(e) {
  const r = canvas.getBoundingClientRect();
  return {
    x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)),
    y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height)),
  };
}

function flushMove() {
  if (pendingMove) { send({ t: 'mm', ...pendingMove }); pendingMove = null; }
}

function edgePan(e) {
  if (!S || !S.zoom || S.zoom === 'fit') return;
  const st = $('stage');
  const r = st.getBoundingClientRect();
  const m = 40, step = 24;
  if (e.clientX > r.right - m) st.scrollLeft += step;
  else if (e.clientX < r.left + m) st.scrollLeft -= step;
  if (e.clientY > r.bottom - m) st.scrollTop += step;
  else if (e.clientY < r.top + m) st.scrollTop -= step;
}

canvas.addEventListener('pointermove', (e) => {
  if (!S || !S.control || e.pointerType === 'touch') return;
  edgePan(e);
  const first = !pendingMove;
  pendingMove = norm(e);
  if (first) requestAnimationFrame(flushMove); // coalesce to one move per display frame
});
canvas.addEventListener('pointerdown', (e) => {
  if (!S || e.pointerType === 'touch') return;
  if (!S.control) { nudgeMode(); return; }
  e.preventDefault();
  stage.focus();
  canvas.setPointerCapture(e.pointerId);
  pendingMove = norm(e);
  flushMove();
  send({ t: 'md', b: e.button });
});
canvas.addEventListener('pointerup', (e) => {
  if (!S || !S.control || e.pointerType === 'touch') return;
  e.preventDefault();
  pendingMove = norm(e);
  flushMove();
  send({ t: 'mu', b: e.button });
});
canvas.addEventListener('contextmenu', (e) => e.preventDefault());
// Back/forward mouse buttons would otherwise navigate the viewer page.
for (const ev of ['mouseup', 'auxclick']) canvas.addEventListener(ev, (e) => { if (e.button > 2) e.preventDefault(); });

stage.addEventListener('wheel', (e) => {
  if (!S || !S.control) return;
  e.preventDefault();
  const k = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? 800 : 1;
  wheelAcc.dx += e.deltaX * k;
  wheelAcc.dy += e.deltaY * k;
  if (!wheelAcc.scheduled) {
    wheelAcc.scheduled = true;
    requestAnimationFrame(() => {
      send({ t: 'wh', dx: Math.round(wheelAcc.dx), dy: Math.round(wheelAcc.dy) });
      wheelAcc = { dx: 0, dy: 0, scheduled: false };
    });
  }
}, { passive: false });


// ---------------------------------------------------------------- touch (phone / tablet)
// tap = click · double tap = double click · long press = right click
// one-finger drag = mouse drag · two-finger drag = scroll

let touch = null;

function sendMoveTo(t) {
  pendingMove = norm(t);
  flushMove();
}

function clickAt(t, b) {
  sendMoveTo(t);
  send({ t: 'md', b });
  send({ t: 'mu', b });
}

function touchCenter(list) {
  let x = 0, y = 0;
  for (const t of list) { x += t.clientX; y += t.clientY; }
  return { x: x / list.length, y: y / list.length };
}

canvas.addEventListener('touchstart', (e) => {
  if (!S) return;
  e.preventDefault();
  if (!S.control) { nudgeMode(); return; }
  if (touch && touch.timer) clearTimeout(touch.timer);
  if (e.touches.length >= 2) {
    if (touch && touch.mode === 'drag') send({ t: 'mu', b: 0 });
    touch = { mode: 'scroll', last: touchCenter(e.touches) };
    return;
  }
  const t = e.touches[0];
  const start = { clientX: t.clientX, clientY: t.clientY };
  touch = {
    mode: 'pending',
    start,
    timer: setTimeout(() => {
      if (touch && touch.mode === 'pending') {
        clickAt(start, 2);
        touch.mode = 'done';
        if (navigator.vibrate) navigator.vibrate(15);
      }
    }, 550),
  };
}, { passive: false });

canvas.addEventListener('touchmove', (e) => {
  if (!S || !S.control || !touch) return;
  e.preventDefault();
  if (touch.mode === 'scroll' && e.touches.length >= 2) {
    const c = touchCenter(e.touches);
    // Finger moves up → content scrolls down (positive dy), like a trackpad.
    send({ t: 'wh', dx: Math.round((touch.last.x - c.x) * 2), dy: Math.round((touch.last.y - c.y) * 2) });
    touch.last = c;
    return;
  }
  const t = e.touches[0];
  if (touch.mode === 'pending') {
    if (Math.hypot(t.clientX - touch.start.clientX, t.clientY - touch.start.clientY) < 10) return;
    clearTimeout(touch.timer);
    touch.mode = 'drag';
    sendMoveTo(touch.start);
    send({ t: 'md', b: 0 });
  }
  if (touch.mode === 'drag') {
    pendingMove = norm(t);
    requestAnimationFrame(flushMove);
  }
}, { passive: false });

canvas.addEventListener('touchend', (e) => {
  if (!S || !S.control || !touch) return;
  e.preventDefault();
  if (touch.timer) clearTimeout(touch.timer);
  if (touch.mode === 'pending') clickAt(touch.start, 0);
  else if (touch.mode === 'drag') { flushMove(); send({ t: 'mu', b: 0 }); }
  if (e.touches.length === 0) touch = null;
}, { passive: false });

canvas.addEventListener('touchcancel', () => {
  if (touch && touch.mode === 'drag') send({ t: 'mu', b: 0 });
  if (touch && touch.timer) clearTimeout(touch.timer);
  touch = null;
});

// On-screen keyboard: a hidden textarea brings up the phone keyboard. Typed
// characters become physical key presses (US layout), so the remote IME
// (Telex/Unikey) still works; anything not on a US keyboard (e.g. "ệ" from the
// phone's Vietnamese keyboard) is pasted through the clipboard instead.
const US_KEYS = (() => {
  const m = {};
  for (const c of 'abcdefghijklmnopqrstuvwxyz') { m[c] = ['Key' + c.toUpperCase()]; m[c.toUpperCase()] = ['Key' + c.toUpperCase(), 1]; }
  '1234567890'.split('').forEach((d, i) => { m[d] = ['Digit' + d]; m['!@#$%^&*()'[i]] = ['Digit' + d, 1]; });
  const pairs = { '-': 'Minus', '=': 'Equal', '[': 'BracketLeft', ']': 'BracketRight', '\\': 'Backslash',
    ';': 'Semicolon', "'": 'Quote', ',': 'Comma', '.': 'Period', '/': 'Slash', '`': 'Backquote' };
  const shifted = { '_': 'Minus', '+': 'Equal', '{': 'BracketLeft', '}': 'BracketRight', '|': 'Backslash',
    ':': 'Semicolon', '"': 'Quote', '<': 'Comma', '>': 'Period', '?': 'Slash', '~': 'Backquote' };
  for (const [c, k] of Object.entries(pairs)) m[c] = [k];
  for (const [c, k] of Object.entries(shifted)) m[c] = [k, 1];
  Object.assign(m, { ' ': ['Space'], '\n': ['Enter'], '’': ['Quote'], '‘': ['Quote'], '“': ['Quote', 1], '”': ['Quote', 1] });
  return m;
})();

function tapKey(code, shift) {
  if (shift) send({ t: 'kd', c: 'ShiftLeft' });
  send({ t: 'kd', c: code });
  send({ t: 'ku', c: code });
  if (shift) send({ t: 'ku', c: 'ShiftLeft' });
}

function pasteText(text) {
  if (!S || !text) return;
  const mod = S.agent && S.agent.os === 'darwin' ? 'MetaLeft' : 'ControlLeft';
  S.lastClip = text;
  send({ t: 'clip', text });
  send({ t: 'kd', c: mod }); send({ t: 'kd', c: 'KeyV' }); send({ t: 'ku', c: 'KeyV' }); send({ t: 'ku', c: mod });
}

function typeText(text) {
  let pending = '';
  for (const ch of text) {
    const k = US_KEYS[ch];
    if (!k) { pending += ch; continue; }
    if (pending) { pasteText(pending); pending = ''; }
    tapKey(k[0], !!k[1]);
  }
  if (pending) pasteText(pending);
}

const vkb = $('vkb');
vkb.addEventListener('beforeinput', (e) => {
  if (!S) return;
  e.preventDefault();
  switch (e.inputType) {
    case 'insertText': case 'insertReplacementText': case 'insertFromPaste':
      typeText(e.data || (e.dataTransfer && e.dataTransfer.getData('text/plain')) || '');
      break;
    case 'insertLineBreak': case 'insertParagraph': tapKey('Enter'); break;
    case 'deleteContentBackward': case 'deleteWordBackward': tapKey('Backspace'); break;
    case 'deleteContentForward': tapKey('Delete'); break;
  }
});
// Desktop keys typed while the textarea has focus are handled by the window
// key listeners; stop them from also producing beforeinput text.
vkb.addEventListener('keydown', (e) => { if (e.key !== 'Unidentified' && e.key !== 'Process' && e.code) e.stopPropagation(); });

$('btn-kb').onclick = () => {
  if (!S || !S.control) { nudgeMode(); return; }
  if (document.activeElement === vkb) { vkb.blur(); stage.focus(); } else vkb.focus();
};

// ---------------------------------------------------------------- keyboard

const SWAP = { ControlLeft: 'MetaLeft', MetaLeft: 'ControlLeft', ControlRight: 'MetaRight', MetaRight: 'ControlRight' };

function mapCode(code) {
  return $('chk-swap').checked && SWAP[code] ? SWAP[code] : code;
}

function keyTargetIsUi(e) {
  const t = e.target;
  return t && (t.tagName === 'SELECT' || t.tagName === 'INPUT' || t.tagName === 'BUTTON') && $('toolbar').contains(t);
}

function releaseAll() {
  if (!S) return;
  S.pressed.clear();
  send({ t: 'ra' });
}

window.addEventListener('keydown', (e) => {
  if (!S || !S.control || keyTargetIsUi(e) || !e.code) return;
  e.preventDefault();
  const code = mapCode(e.code);
  if (e.code === 'CapsLock') { send({ t: 'kd', c: code }); send({ t: 'ku', c: code }); return; }
  if (e.code === 'KeyV' && (e.ctrlKey || e.metaKey) && !e.repeat && navigator.clipboard && navigator.clipboard.readText) {
    // Push our clipboard to the remote side before the paste keystroke lands.
    S.gate = S.gate.then(async () => {
      try {
        const text = await Promise.race([navigator.clipboard.readText(), new Promise((_, rej) => setTimeout(rej, 1500))]);
        if (text && text !== S.lastClip) { S.lastClip = text; sendInput({ t: 'clip', text }); }
      } catch { /* permission denied or timeout: paste whatever the remote has */ }
    });
  }
  S.pressed.add(code);
  send({ t: 'kd', c: code });
});

window.addEventListener('keyup', (e) => {
  if (!S || !S.control || keyTargetIsUi(e) || !e.code) return;
  e.preventDefault();
  const code = mapCode(e.code);
  if (e.code === 'CapsLock') { send({ t: 'kd', c: code }); send({ t: 'ku', c: code }); return; }
  S.pressed.delete(code);
  send({ t: 'ku', c: code });
  // macOS browsers swallow keyup for keys released while ⌘ is held.
  if (IS_MAC_VIEWER && (e.code === 'MetaLeft' || e.code === 'MetaRight')) {
    for (const c of [...S.pressed]) { S.pressed.delete(c); send({ t: 'ku', c }); }
  }
});

window.addEventListener('blur', releaseAll);
document.addEventListener('visibilitychange', () => { if (document.hidden) releaseAll(); });

// ---------------------------------------------------------------- toolbar

$('btn-back').onclick = () => { location.hash = '#/'; };

// View-only ⇄ control. Leaving control releases any held keys/buttons first.
function updateMode() {
  const on = !!(S && S.control);
  const b = $('btn-mode');
  b.textContent = on ? '🖱 Đang điều khiển' : '👁 Chỉ xem';
  b.title = on ? 'Bấm để chuyển về chỉ xem' : 'Bấm để điều khiển chuột / bàn phím';
  b.classList.toggle('on', on);
  canvas.classList.toggle('view-only', !on);
}

function setControl(on) {
  if (!S) return;
  if (!on && S.control) {
    if (touch && touch.mode === 'drag') send({ t: 'mu', b: 0 });
    releaseAll();
    touch = null;
  }
  S.control = on;
  updateMode();
  stage.focus();
}

function nudgeMode() {
  const b = $('btn-mode');
  b.classList.remove('pulse');
  void b.offsetWidth; // restart the animation
  b.classList.add('pulse');
}

$('btn-mode').onclick = () => setControl(!(S && S.control));

$('sel-zoom').onchange = (e) => { setZoom(e.target.value); stage.focus(); };

$('sel-quality').onchange = (e) => {
  prefs.set('quality', e.target.value);
  sendNow({ t: 'cfg', ...QUALITY[e.target.value] });
  stage.focus();
};

$('chk-p2p').onchange = (e) => {
  prefs.set('p2p', e.target.checked);
  if (S) {
    S.p2pFails = 0;
    if (e.target.checked) startP2P(); else stopP2P(null);
  }
  stage.focus();
};

$('chk-swap').onchange = (e) => {
  if (S && S.agent) prefs.set(`swap:${S.agent.id}`, e.target.checked);
  stage.focus();
};

const COMBOS = {
  meta: ['MetaLeft'],
  'alt-tab': ['AltLeft', 'Tab'],
  'cmd-tab': ['MetaLeft', 'Tab'],
  'cmd-space': ['MetaLeft', 'Space'],
  'win-d': ['MetaLeft', 'KeyD'],
  'ctrl-shift-esc': ['ControlLeft', 'ShiftLeft', 'Escape'],
  esc: ['Escape'],
  tab: ['Tab'],
  enter: ['Enter'],
  del: ['Delete'],
  left: ['ArrowLeft'],
  right: ['ArrowRight'],
  up: ['ArrowUp'],
  down: ['ArrowDown'],
  'ctrl-c': ['ControlLeft', 'KeyC'],
  'ctrl-v': ['ControlLeft', 'KeyV'],
  'cmd-c': ['MetaLeft', 'KeyC'],
  'cmd-v': ['MetaLeft', 'KeyV'],
};

$('sel-keys').onchange = (e) => {
  const v = e.target.value;
  e.target.value = '';
  stage.focus();
  if (!S || !S.control) { nudgeMode(); return; }
  if (v === 'restart-agent') {
    if (confirm('Khởi động lại agent trên máy kia?')) sendNow({ t: 'restart' });
    return;
  }
  const keys = COMBOS[v];
  if (!keys) return;
  for (const c of keys) send({ t: 'kd', c });
  for (const c of [...keys].reverse()) send({ t: 'ku', c });
};

$('btn-fs').onclick = async () => {
  if (window.rdNative) { window.rdNative.fullscreen(); stage.focus(); return; } // desktop app window
  if (document.fullscreenElement) { await document.exitFullscreen(); return; }
  if (!$('session').requestFullscreen) return; // iPhone Safari: no Fullscreen API
  await $('session').requestFullscreen();
  // Chrome/Edge: capture ⌘/Win/Alt+Tab etc. while fullscreen.
  if (navigator.keyboard && navigator.keyboard.lock) navigator.keyboard.lock().catch(() => {});
  stage.focus();
};

window.addEventListener('beforeunload', (e) => { if (S && !window.rdNative) e.preventDefault(); });

// ---------------------------------------------------------------- install as app (PWA)

let installPrompt = null;
const IS_APP = matchMedia('(display-mode: standalone)').matches || navigator.standalone === true || !!window.rdNative;

if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});

window.addEventListener('beforeinstallprompt', (e) => {
  e.preventDefault();
  if (IS_APP) return; // show our own button instead of the browser mini-bar
  installPrompt = e;
  $('btn-install').hidden = false;
  $('install-hint').hidden = true;
});
window.addEventListener('appinstalled', () => { $('btn-install').hidden = true; installPrompt = null; });

$('btn-install').onclick = async () => {
  if (!installPrompt) return;
  installPrompt.prompt();
  await installPrompt.userChoice;
  installPrompt = null;
  $('btn-install').hidden = true;
};

// Safari has no install prompt; tell the user where the menu item is.
if (!IS_APP && /Safari/.test(navigator.userAgent) && !/Chrome|Chromium|Edg\//.test(navigator.userAgent)) {
  $('install-hint').hidden = false;
  $('install-hint').textContent = IS_MAC_VIEWER
    ? 'Muốn dùng như app: Safari › File › Add to Dock (hoặc mở bằng Chrome/Edge để có nút “Cài app”).'
    : '';
}

// ---------------------------------------------------------------- who am I / admin

async function loadMe() {
  try {
    const r = await fetch('/api/me', { cache: 'no-store' });
    if (!r.ok) return null;
    const me = await r.json();
    $('me-name').textContent = me.admin ? '' : me.name;
    $('lnk-admin').hidden = !me.admin;
    return me;
  } catch { return null; }
}

async function adminCall(method, path, body) {
  const r = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (r.status === 401) { show('login'); throw new Error('login'); }
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return r.json();
}

function adminError(e) {
  $('admin-error').hidden = !e;
  $('admin-error').textContent = e ? `Lỗi: ${e.message || e}` : '';
}

async function showAdmin() {
  clearInterval(devicesTimer);
  show('admin');
  const me = await loadMe();
  if (!me) { show('login'); return; }
  if (!me.admin) { location.hash = '#/'; return; }
  await refreshUsers();
}

async function refreshUsers() {
  let list;
  try { list = await adminCall('GET', '/api/admin/users'); adminError(null); } catch (e) { adminError(e); return; }
  $('user-empty').hidden = list.length > 0;
  $('user-list').replaceChildren(...list.map(userItem));
}

// What to send the person: where to install, where to watch, their code.
function userMessage(u) {
  return [
    `RemoteDesk – hướng dẫn cho ${u.name}`,
    '',
    '1) Cài lên máy cần được điều khiển (tải bằng chính link này):',
    `   • Windows: ${u.install.agent.windowsExe}`,
    `   • Mac: ${u.install.agent.macPkg}`,
    '',
    `2) Xem / điều khiển từ trình duyệt hoặc điện thoại: ${location.origin}`,
    `   Mã truy cập: ${u.token}`,
    '',
    'Giữ kín link và mã này: ai có chúng là điều khiển được máy của bạn.',
  ].join('\n');
}

function copyButton(label, text) {
  const b = document.createElement('button');
  b.type = 'button';
  b.textContent = label;
  b.onclick = async () => {
    try { await navigator.clipboard.writeText(text); b.textContent = 'Đã copy'; } catch { b.textContent = 'Lỗi'; }
    setTimeout(() => { b.textContent = label; }, 1500);
  };
  return b;
}

function userItem(u) {
  const li = document.createElement('li');
  li.className = 'user' + (u.disabled ? ' disabled' : '');
  li.innerHTML = `<div class="user-head"><span class="meta"><div class="title"></div><div class="sub"></div></span><span class="user-actions"></span></div>
    <details class="user-links"><summary class="small">Link cài đặt &amp; mã truy cập</summary><div class="user-links-body"></div></details>`;
  li.querySelector('.title').textContent = u.name;
  const created = u.created ? new Date(u.created).toLocaleDateString('vi-VN') : '';
  li.querySelector('.sub').textContent = `${u.disabled ? 'Đã khoá · ' : ''}${u.online} máy online · tạo ngày ${created}`;

  const act = li.querySelector('.user-actions');
  const button = (label, fn, cls) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.textContent = label;
    if (cls) b.className = cls;
    b.onclick = async () => { try { await fn(); adminError(null); } catch (e) { if (e.message !== 'cancel') adminError(e); } refreshUsers(); };
    act.append(b);
  };
  const ask = (q) => { if (!confirm(q)) throw new Error('cancel'); };
  act.append(copyButton('Copy tin nhắn gửi họ', userMessage(u)));
  button('Đổi tên', async () => {
    const name = prompt('Tên mới:', u.name);
    if (!name || name.trim() === u.name) throw new Error('cancel');
    await adminCall('POST', `/api/admin/users/${u.id}/rename`, { name });
  });
  button('Đổi mã', async () => {
    ask(`Đổi mã của "${u.name}"? Máy và trình duyệt đang dùng mã cũ sẽ bị ngắt; phải cài lại máy bằng link mới.`);
    await adminCall('POST', `/api/admin/users/${u.id}/rotate`);
  });
  if (u.disabled) button('Mở khoá', () => adminCall('POST', `/api/admin/users/${u.id}/enable`));
  else button('Khoá', async () => { ask(`Khoá "${u.name}"? Mọi máy của họ sẽ offline cho tới khi mở khoá.`); await adminCall('POST', `/api/admin/users/${u.id}/disable`); });
  button('Xoá', async () => { ask(`Xoá hẳn "${u.name}"? Không hoàn tác được.`); await adminCall('DELETE', `/api/admin/users/${u.id}`); }, 'danger');

  const body = li.querySelector('.user-links-body');
  const rows = [
    ['Mã truy cập (đăng nhập trang web)', u.token],
    ['Windows · chỉ agent', u.install.agent.windowsExe],
    ['Windows · agent + app', u.install.full.windowsExe],
    ['Mac · chỉ agent', u.install.agent.macPkg],
    ['Mac · agent + app', u.install.full.macPkg],
    ['Windows · PowerShell', u.install.agent.windowsPs],
    ['Mac · Terminal', u.install.agent.mac],
  ];
  for (const [label, value] of rows) {
    const p = document.createElement('p');
    p.className = 'muted small';
    p.textContent = label;
    const row = document.createElement('div');
    row.className = 'cmd';
    const code = document.createElement('code');
    code.textContent = value;
    row.append(code, copyButton('Copy', value));
    body.append(p, row);
  }
  return li;
}

$('user-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const name = $('user-name').value.trim();
  if (!name) return;
  try {
    await adminCall('POST', '/api/admin/users', { name });
    $('user-name').value = '';
    adminError(null);
  } catch (err) { adminError(err); }
  refreshUsers();
});

// ---------------------------------------------------------------- routing

async function route() {
  if (location.hash === '#/admin') { stopSession(); showAdmin(); return; }
  const m = location.hash.match(/^#\/d\/(.+)$/);
  if (!m) { stopSession(); showDevices(); return; }
  const r = await fetch('/api/agents', { cache: 'no-store' }).catch(() => null);
  if (r && r.status === 401) { stopSession(); show('login'); return; }
  startSession(decodeURIComponent(m[1]));
}
window.addEventListener('hashchange', route);
route();
