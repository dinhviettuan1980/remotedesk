'use strict';
// RemoteDesk viewer: device list + remote session (WebCodecs H.264 → canvas).

const $ = (id) => document.getElementById(id);

const QUALITY = {
  eco: { fps: 24, maxw: 1280, kbps: 1500 },
  balanced: { fps: 30, maxw: 1920, kbps: 3500 },
  sharp: { fps: 60, maxw: 1920, kbps: 8000 },
  max: { fps: 60, maxw: 3840, kbps: 16000 },
};
// Fallback; the relay's hello carries STUN + TURN (coturn on the VPS).
const ICE_SERVERS = [{ urls: ['stun:stun.l.google.com:19302', 'stun:stun.cloudflare.com:3478'] }];
const P2P_SUPPORTED = 'RTCPeerConnection' in window && 'requestVideoFrameCallback' in HTMLVideoElement.prototype;
const IS_MAC_VIEWER = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
// Phone-sized window: the less used toolbar items move into the ⋯ menu (style.css).
const COMPACT = matchMedia('(max-width: 900px), (max-height: 500px)');

// Remote sound needs WebCodecs Opus decoding (relay path) and Web Audio.
const AudioCtx = window.AudioContext || window.webkitAudioContext;
let audioOk = false;
if ('AudioDecoder' in window && AudioCtx) {
  AudioDecoder.isConfigSupported({ codec: 'opus', sampleRate: 48000, numberOfChannels: 2 })
    .then((r) => { audioOk = !!r.supported; updateAudioButton(); })
    .catch(() => {});
}

// "0.2.10" ≥ "0.2.6"
function verAtLeast(v, min) {
  const a = String(v || '').split('.').map(Number), b = min.split('.').map(Number);
  for (let i = 0; i < b.length; i++) {
    const x = a[i] || 0;
    if (x !== b[i]) return x > b[i];
  }
  return true;
}

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

$('login-eye').onclick = () => {
  const inp = $('login-token'), show = inp.type === 'password';
  inp.type = show ? 'text' : 'password';
  $('login-eye').textContent = show ? '🙈' : '👁';
  $('login-eye').title = show ? 'Ẩn mã' : 'Hiện mã';
  inp.focus();
};

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
    pressed: new Map(), // physical e.code → code sent to the remote
    gate: Promise.resolve(),
    lastClip: '',
    lastKfReq: 0,
    retry: null,
    statsTimer: setInterval(updateStats, 1000),
    control: false, // every session starts view-only
    tp: { x: 0.5, y: 0.5 }, // touchpad pointer, 0..1 of the remote screen
    tpUsed: false,
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
    p2pAudio: null, // <audio> playing the P2P Opus track
    // Displays of the remote machine (agent 0.2.6+, from "info").
    screens: null,
    screen: 0,
    screenSynced: false, // the remembered choice was applied for this connection
    // Remote sound: off until 🔇 is clicked; relay packets are decoded into
    // Web Audio (S.audio), the P2P track plays in S.p2pAudio.
    agentAudio: false,
    audioOn: false,
    audio: null,
  };
  $('chk-p2p').checked = prefs.get('p2p', true);
  $('chk-p2p').parentElement.hidden = !P2P_SUPPORTED;
  renderScreens();
  updateAudioButton();
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
  $('kb-offer').hidden = true;
  clearInterval(S.statsTimer);
  stopP2P(null);
  filesReset();
  closeAudio();
  try { S.ws && S.ws.close(); } catch { /* ignore */ }
  closeDecoder();
  if (S.latest) S.latest.close();
  S = null;
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  if (nativeFs) window.rdNative.fullscreen();
  nativeKeys();
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
    if (typeof ev.data === 'string') { onControl(JSON.parse(ev.data)); return; }
    const type = new Uint8Array(ev.data, 0, 1)[0];
    if (type === 2) onFileData(ev.data); // files.js
    else if (type === 3) onAudio(ev.data);
    else onVideo(ev.data);
  };
  ws.onclose = (ev) => {
    if (!S || S.ws !== ws || S.closed) return;
    stopP2P(null); // signaling is gone; renegotiate after reconnecting
    S.p2pFails = 0;
    filesReset();
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
      S.ice = m.ice || ICE_SERVERS;
      $('sess-name').textContent = m.agent.name;
      document.title = `${m.agent.name} — RemoteDesk`;
      const key = `swap:${m.agent.id}`;
      const def = IS_MAC_VIEWER !== (m.agent.os === 'darwin'); // swap by default across OSes
      $('chk-swap').checked = prefs.get(key, def);
      S.zoom = prefs.get(`zoom:${m.agent.id}`, 'fit');
      syncZoomSelect();
      fitCanvas();
      $('key-meta').textContent = m.agent.os === 'darwin' ? '⌘' : 'Win';
      setOverlay('Đang chờ hình ảnh…');
      S.screenSynced = false; // the agent may have restarted on its default display
      if (S.audioOn) sendNow({ t: 'audio', on: true }); // a new relay connection starts silent
      startP2P();
      break;
    }
    case 'rtc':
      onRtc(m);
      break;
    case 'edit': // agent 0.2.4+: the focus is (not) a text field after a click / while typing
      onEdit(m);
      break;
    case 'fls': case 'fmeta': case 'fack': case 'fdone': case 'ferr': case 'fclip': case 'clipfiles':
      onFileMsg(m); // files.js
      break;
    case 'rtc-reset': // the agent reconnected: its peer connections are gone
      stopP2P(null);
      S.p2pFails = 0;
      S.p2pRetry = setTimeout(startP2P, 1000);
      break;
    case 'info':
      $('sess-stats').dataset.enc = m.enc || '';
      onInfo(m);
      break;
    case 'clip':
      S.lastClip = m.text;
      if (navigator.clipboard && document.hasFocus()) navigator.clipboard.writeText(m.text).catch(() => {});
      break;
    case 'error':
      setOverlay(m.msg);
      break;
    case 'notice': { // short status from the agent (e.g. update progress)
      setOverlay(m.msg);
      const msg = m.msg;
      setTimeout(() => { if ($('overlay').textContent === msg) setOverlay(''); }, 6000);
      break;
    }
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
  const pc = new RTCPeerConnection({ iceServers: S.ice || ICE_SERVERS, bundlePolicy: 'max-bundle' });
  S.pc = pc;
  S.pendingCands = [];
  const dc = pc.createDataChannel('input', { ordered: true });
  dc.onopen = () => { if (S && S.pc === pc) S.dc = dc; };
  dc.onclose = () => { if (S && S.dc === dc) S.dc = null; };
  pc.addTransceiver('video', { direction: 'recvonly' });
  // Agents before 0.2.6 have no audio; an offer without it keeps them simple.
  if (S.agent && verAtLeast(S.agent.ver, '0.2.6')) pc.addTransceiver('audio', { direction: 'recvonly' });

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
    if (e.track.kind === 'audio') {
      const el = new Audio();
      el.autoplay = false;
      el.srcObject = new MediaStream([e.track]);
      S.p2pAudio = el;
      syncP2PAudio();
      return;
    }
    // Render as soon as a frame decodes; sound is not lip-synced to it.
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
  syncP2PAudio();
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
  if (S.p2pAudio) { S.p2pAudio.pause(); S.p2pAudio.srcObject = null; S.p2pAudio = null; }
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
    if (local && remote && local.candidateType === 'host' && remote.candidateType === 'host') kind = 'LAN';
    else if (local && remote && (local.candidateType === 'relay' || remote.candidateType === 'relay')) kind = 'TURN';
    else kind = '';
  }
  const now = performance.now();
  const prev = S.p2pStats;
  S.p2pStats = { bytes, at: now };
  const kbps = prev ? Math.round(((bytes - prev.bytes) * 8) / (now - prev.at)) : 0;
  return { kbps, rtt, kind };
}

// ---------------------------------------------------------------- displays (agent 0.2.6+)

function onInfo(m) {
  S.agentAudio = !!m.audio;
  updateAudioButton();
  if (!Array.isArray(m.screens)) return; // older agent: one display, no choice
  const switched = S.switching != null && m.screen === S.switching;
  S.screens = m.screens;
  S.screen = m.screen | 0;
  renderScreens();
  if (switched) { S.switching = null; setOverlay(''); }
  if (S.screenSynced || !S.agent) return;
  S.screenSynced = true;
  // Back to the display chosen last time on this machine (matched by name:
  // the order can change when monitors are plugged in or out).
  const want = prefs.get(`screen:${S.agent.id}`, null);
  if (!want || S.screens.length < 2) return;
  let i = S.screens[want.i] && S.screens[want.i].name === want.name ? want.i : S.screens.findIndex((d) => d.name === want.name);
  if (i < 0 && want.i < S.screens.length) i = want.i;
  if (i >= 0 && i !== S.screen) chooseScreen(i, false);
}

function renderScreens() {
  const sel = $('sel-screen');
  const list = S && S.screens;
  sel.hidden = !list || list.length < 2;
  if (sel.hidden) return;
  sel.replaceChildren(...list.map((d, i) => {
    const o = document.createElement('option');
    o.value = String(i);
    o.textContent = `🖥 ${i + 1}. ${d.name} · ${d.w}×${d.h}${d.primary ? ' (chính)' : ''}`;
    return o;
  }));
  sel.value = String(S.screen);
}

function chooseScreen(i, remember) {
  if (!S || !S.screens || !S.screens[i]) return;
  if (remember && S.agent) prefs.set(`screen:${S.agent.id}`, { i, name: S.screens[i].name });
  if (i === S.screen) return;
  S.switching = i;
  setOverlay(`Đang chuyển sang màn hình ${i + 1}…`);
  sendNow({ t: 'screen', i });
  // An agent that cannot switch would leave the overlay up: give up quietly.
  setTimeout(() => { if (S && S.switching === i) { S.switching = null; setOverlay(''); renderScreens(); } }, 8000);
}

$('sel-screen').onchange = (e) => {
  chooseScreen(Number(e.target.value), true);
  stage.focus();
};

// ---------------------------------------------------------------- remote sound (agent 0.2.6+)
// Relay path: Opus packets (binary type 3) → WebCodecs AudioDecoder → short
// AudioBuffers scheduled back to back on a Web Audio clock. P2P path: the
// Opus track of the peer connection plays in an <audio> element.

function updateAudioButton() {
  const b = $('btn-audio');
  b.hidden = !(S && audioOk && S.agentAudio);
  const on = !!(S && S.audioOn);
  b.querySelector('.ico').textContent = on ? '🔊' : '🔇';
  b.querySelector('.menu-label').textContent = on ? ' Âm thanh: bật' : ' Âm thanh: tắt';
  b.title = on ? 'Tắt âm thanh của máy kia' : 'Nghe âm thanh của máy kia';
  b.classList.toggle('on', on);
}

function setAudio(on) {
  if (!S) return;
  if (on && !S.audio) {
    let ctx;
    try { ctx = new AudioCtx({ latencyHint: 'interactive', sampleRate: 48000 }); } catch { ctx = new AudioCtx(); }
    ctx.resume().catch(() => {}); // inside the click: allowed to start
    const a = { ctx, dec: null, playhead: 0, trim: 0, minAhead: undefined, winEnd: 0 };
    a.dec = new AudioDecoder({
      output: (d) => playAudio(a, d),
      error: (e) => console.warn('audio decoder', e),
    });
    a.dec.configure({ codec: 'opus', sampleRate: 48000, numberOfChannels: 2 });
    S.audio = a;
  } else if (!on) {
    closeAudio();
  }
  S.audioOn = on;
  sendNow({ t: 'audio', on });
  syncP2PAudio();
  updateAudioButton();
}

function closeAudio() {
  const a = S && S.audio;
  if (!a) return;
  S.audio = null;
  try { if (a.dec.state !== 'closed') a.dec.close(); } catch { /* ignore */ }
  a.ctx.close().catch(() => {});
}

function onAudio(buf) {
  const a = S.audio;
  if (!a || !S.audioOn || a.dec.state !== 'configured' || buf.byteLength <= 6) return;
  if (S.p2pLive && S.p2pAudio) return; // stray relay packets after the switch to P2P
  const ts = new DataView(buf).getUint32(2);
  try {
    a.dec.decode(new EncodedAudioChunk({ type: 'key', timestamp: ts * 1000, data: new Uint8Array(buf, 6) }));
  } catch (e) {
    console.warn('audio decode', e);
  }
}

// Each 20 ms plays right after the previous one, ~60 ms behind real time
// (room for network jitter). Running dry restarts that margin. A queue that
// stayed above 100 ms for a whole second (the burst at start, a connection
// catching up after a stall) is trimmed back by dropping packets.
function playAudio(a, d) {
  if (!S || S.audio !== a) { d.close(); return; }
  const ctx = a.ctx;
  const now = ctx.currentTime;
  const ahead = a.playhead - now;
  if (ahead > 0) a.minAhead = Math.min(a.minAhead ?? ahead, ahead);
  if (!a.winEnd || now >= a.winEnd) {
    if (a.minAhead > 0.1) a.trim = Math.round((a.minAhead - 0.06) / 0.02);
    a.minAhead = undefined;
    a.winEnd = now + 1;
  }
  if ((a.trim > 0 && ahead > 0.04) || ahead > 0.3) { a.trim--; d.close(); return; }
  const n = d.numberOfFrames, ch = d.numberOfChannels;
  const buf = ctx.createBuffer(ch, n, d.sampleRate);
  try {
    for (let c = 0; c < ch; c++) {
      const plane = new Float32Array(n);
      d.copyTo(plane, { planeIndex: c, format: 'f32-planar' });
      buf.copyToChannel(plane, c);
    }
  } catch (e) {
    console.warn('audio copy', e);
    d.close();
    return;
  }
  d.close();
  if (ahead < 0.01) a.playhead = now + 0.06;
  const src = ctx.createBufferSource();
  src.buffer = buf;
  src.connect(ctx.destination);
  src.start(a.playhead);
  a.playhead += buf.duration;
}

// The P2P track is audible only while video also comes P2P (the relay stops
// sending sound to this viewer then) and sound is on.
function syncP2PAudio() {
  const el = S && S.p2pAudio;
  if (!el) return;
  const on = S.audioOn && S.p2pLive;
  el.muted = !on;
  if (on) el.play().catch((e) => console.warn('p2p audio', e));
  else el.pause();
}

$('btn-audio').onclick = () => {
  setAudio(!(S && S.audioOn));
  stage.focus();
};

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
  placeCursor();
}

// Pinch zoom gives factors the menu does not list: show them as an extra entry.
function syncZoomSelect() {
  const sel = $('sel-zoom'), custom = $('zoom-custom'), z = String(S.zoom || 'fit');
  const known = [...sel.options].some((o) => o !== custom && o.value === z);
  custom.hidden = known;
  if (!known) { custom.value = z; custom.textContent = `🔍 ${Math.round(Number(z) * 100)}%`; }
  sel.value = z;
}

function setZoom(z) {
  if (!S) return;
  // Keep the point at the centre of the view in place while zooming.
  const st = $('stage');
  const cx = (st.scrollLeft + st.clientWidth / 2) / Math.max(1, st.scrollWidth);
  const cy = (st.scrollTop + st.clientHeight / 2) / Math.max(1, st.scrollHeight);
  S.zoom = z;
  if (S.agent) prefs.set(`zoom:${S.agent.id}`, z);
  syncZoomSelect();
  fitCanvas();
  st.scrollLeft = cx * st.scrollWidth - st.clientWidth / 2;
  st.scrollTop = cy * st.scrollHeight - st.clientHeight / 2;
}
window.addEventListener('resize', fitCanvas);
// The stage also changes size when the toolbar hides or the phone keyboard opens.
if (window.ResizeObserver) new ResizeObserver(() => fitCanvas()).observe($('stage'));
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
  $('menu-stats').textContent = $('sess-stats').textContent;
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
  syncModifiers(e);
  const first = !pendingMove;
  pendingMove = norm(e);
  if (first) requestAnimationFrame(flushMove); // coalesce to one move per display frame
});
canvas.addEventListener('pointerdown', (e) => {
  if (!S || e.pointerType === 'touch') return;
  if (!S.control) { nudgeMode(); return; }
  syncModifiers(e);
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
  syncModifiers(e);
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
// Two ways to use touch (⋯ → ☝):
//  touchpad (default): the screen works like a laptop touchpad. Drag = move the
//    pointer (faster swipes go further), tap = click, double tap = double click,
//    two-finger tap or long press = right click, long press then drag = drag.
//  direct: tap where you want to click, drag = mouse drag, long press = right click.
// Both: two-finger drag = scroll, pinch = zoom and pan the view. View-only:
// drag pans the view, pinch zooms.

const IS_TOUCH = navigator.maxTouchPoints > 0 || matchMedia('(pointer: coarse)').matches;
const tpCursor = $('tp-cursor');
let touch = null;

const isTrackpad = () => prefs.get('touch', 'trackpad') === 'trackpad';
const pt = (t) => ({ clientX: t.clientX, clientY: t.clientY });

function sendMoveTo(t) {
  pendingMove = norm(t);
  flushMove();
}

function clickAt(t, b) {
  sendMoveTo(t);
  send({ t: 'md', b });
  send({ t: 'mu', b });
}

function clickCursor(b) {
  pendingMove = { x: S.tp.x, y: S.tp.y };
  flushMove();
  send({ t: 'md', b });
  send({ t: 'mu', b });
}

function touchCenter(list) {
  let x = 0, y = 0;
  for (const t of list) { x += t.clientX; y += t.clientY; }
  return { clientX: x / list.length, clientY: y / list.length };
}

function touchDist(list) {
  return Math.hypot(list[0].clientX - list[1].clientX, list[0].clientY - list[1].clientY);
}

// The local pointer drawn in touchpad mode: the one in the video lags behind.
// It sits outside the scrolling stage so it never makes the stage scroll further.
function placeCursor() {
  const on = !!(S && S.control && S.tpUsed && S.vw && isTrackpad());
  tpCursor.hidden = !on;
  if (!on) return;
  const r = canvas.getBoundingClientRect(), sr = $('session').getBoundingClientRect();
  tpCursor.style.transform = `translate(${r.left - sr.left + S.tp.x * r.width}px, ${r.top - sr.top + S.tp.y * r.height}px)`;
}
stage.addEventListener('scroll', placeCursor);

// Zoomed in: scroll the view so the pointer stays away from the edges.
function followCursor() {
  if (!S.zoom || S.zoom === 'fit') return;
  const px = canvas.offsetLeft + S.tp.x * canvas.offsetWidth;
  const py = canvas.offsetTop + S.tp.y * canvas.offsetHeight;
  const w = stage.clientWidth, h = stage.clientHeight, m = 0.18;
  if (px < stage.scrollLeft + w * m) stage.scrollLeft = px - w * m;
  else if (px > stage.scrollLeft + w * (1 - m)) stage.scrollLeft = px - w * (1 - m);
  if (py < stage.scrollTop + h * m) stage.scrollTop = py - h * m;
  else if (py > stage.scrollTop + h * (1 - m)) stage.scrollTop = py - h * (1 - m);
}

function moveTrackpad(dx, dy, dt) {
  const v = Math.hypot(dx, dy) / Math.max(dt, 8); // CSS px per ms
  const gain = 1 + 1.6 * Math.min(v, 1.5);
  S.tp.x = Math.min(1, Math.max(0, S.tp.x + (dx * gain) / canvas.offsetWidth));
  S.tp.y = Math.min(1, Math.max(0, S.tp.y + (dy * gain) / canvas.offsetHeight));
  const first = !pendingMove;
  pendingMove = { x: S.tp.x, y: S.tp.y };
  if (first) requestAnimationFrame(flushMove);
  followCursor();
  placeCursor();
}

// Pinch keeps the remote point first touched under the fingers' centre, so
// moving both fingers also pans.
function startPinch(list) {
  const c = touchCenter(list);
  const r = canvas.getBoundingClientRect();
  touch = {
    mode: 'two', t0: performance.now(), c0: c, last: c, d0: Math.max(1, touchDist(list)),
    scale0: canvas.offsetWidth / S.vw, ax: (c.clientX - r.left) / r.width, ay: (c.clientY - r.top) / r.height,
  };
}

function doPinch(list) {
  const c = touchCenter(list);
  const fit = Math.min(stage.clientWidth / S.vw, stage.clientHeight / S.vh);
  const s = Math.min(Math.max(touch.scale0 * touchDist(list) / touch.d0, fit), Math.max(3, fit));
  S.zoom = s <= fit * 1.03 ? 'fit' : String(+s.toFixed(3));
  fitCanvas();
  const sr = stage.getBoundingClientRect();
  stage.scrollLeft = canvas.offsetLeft + touch.ax * canvas.offsetWidth - (c.clientX - sr.left);
  stage.scrollTop = canvas.offsetTop + touch.ay * canvas.offsetHeight - (c.clientY - sr.top);
  placeCursor();
}

function endPinch() {
  if (S.agent) prefs.set(`zoom:${S.agent.id}`, S.zoom);
  syncZoomSelect();
}

const onStage = (e) => e.target === canvas || e.target === stage;

stage.addEventListener('touchstart', (e) => {
  if (!S || !onStage(e)) return;
  e.preventDefault();
  if ($('session').classList.contains('tb-show')) hideToolbar();
  if (touch && touch.timer) clearTimeout(touch.timer);
  if (e.touches.length >= 2) {
    if (touch && touch.mode === 'drag') { flushMove(); send({ t: 'mu', b: 0 }); }
    if (S.vw) startPinch(e.touches);
    return;
  }
  if (S.control && isTrackpad() && !S.tpUsed) { S.tpUsed = true; placeCursor(); }
  const t = e.touches[0];
  touch = {
    mode: 'pending', start: pt(t), last: pt(t), lastT: e.timeStamp,
    timer: setTimeout(() => {
      if (!touch || touch.mode !== 'pending' || !S || !S.control) return;
      if (navigator.vibrate) navigator.vibrate(15);
      if (isTrackpad()) { touch.mode = 'hold'; return; } // right click on release, drag on move
      clickAt(touch.start, 2);
      touch.mode = 'done';
    }, 500),
  };
}, { passive: false });

stage.addEventListener('touchmove', (e) => {
  if (!S || !touch) return;
  e.preventDefault();
  if (e.touches.length >= 2) {
    if (touch.mode === 'two') {
      const c = touchCenter(e.touches);
      const moved = Math.hypot(c.clientX - touch.c0.clientX, c.clientY - touch.c0.clientY);
      const spread = Math.abs(touchDist(e.touches) - touch.d0);
      if (spread > 30 || (!S.control && (spread > 4 || moved > 4))) touch.mode = 'pinch';
      else if (moved > 12) touch.mode = 'scroll';
    }
    if (touch.mode === 'pinch') doPinch(e.touches);
    else if (touch.mode === 'scroll') {
      const c = touchCenter(e.touches);
      // Fingers move up → content scrolls down (positive dy), like a trackpad.
      send({ t: 'wh', dx: Math.round((touch.last.clientX - c.clientX) * 2), dy: Math.round((touch.last.clientY - c.clientY) * 2) });
      touch.last = c;
    }
    return;
  }
  const t = pt(e.touches[0]);
  const far = Math.hypot(t.clientX - touch.start.clientX, t.clientY - touch.start.clientY) >= 8;
  const dx = t.clientX - touch.last.clientX, dy = t.clientY - touch.last.clientY, dt = e.timeStamp - touch.lastT;
  touch.last = t;
  touch.lastT = e.timeStamp;
  if (!S.control) { // view-only: pan
    if (touch.mode === 'pending' && far) { clearTimeout(touch.timer); touch.mode = 'pan'; }
    if (touch.mode === 'pan') { stage.scrollLeft -= dx; stage.scrollTop -= dy; }
    return;
  }
  if (isTrackpad()) {
    if (touch.mode === 'pending' && far) { clearTimeout(touch.timer); touch.mode = 'move'; }
    else if (touch.mode === 'hold' && far) { touch.mode = 'drag'; clickCursorDown(); }
    if (touch.mode === 'move' || touch.mode === 'drag') moveTrackpad(dx, dy, dt);
    return;
  }
  if (touch.mode === 'pending' && far) {
    clearTimeout(touch.timer);
    touch.mode = 'drag';
    sendMoveTo(touch.start);
    send({ t: 'md', b: 0 });
  }
  if (touch.mode === 'drag') {
    const first = !pendingMove;
    pendingMove = norm(t);
    if (first) requestAnimationFrame(flushMove);
  }
}, { passive: false });

function clickCursorDown() {
  pendingMove = { x: S.tp.x, y: S.tp.y };
  flushMove();
  send({ t: 'md', b: 0 });
}

stage.addEventListener('touchend', (e) => {
  if (!S || !touch) return;
  e.preventDefault();
  if (touch.timer) clearTimeout(touch.timer);
  if (touch.mode === 'two') { // two-finger tap = right click
    if (S.control && performance.now() - touch.t0 < 350) { if (isTrackpad()) clickCursor(2); else clickAt(touch.c0, 2); }
    touch.mode = 'done';
  }
  if (e.touches.length > 0) return;
  if (touch.mode === 'pending') {
    if (!S.control) nudgeMode();
    else if (isTrackpad()) { clickCursor(0); S.editAt = { ...S.tp }; }
    else { clickAt(touch.start, 0); S.editAt = norm(touch.start); }
  } else if (touch.mode === 'hold') clickCursor(2);
  else if (touch.mode === 'drag') { flushMove(); send({ t: 'mu', b: 0 }); }
  else if (touch.mode === 'pinch') endPinch();
  touch = null;
}, { passive: false });

stage.addEventListener('touchcancel', () => {
  if (!touch) return;
  if (touch.mode === 'drag') send({ t: 'mu', b: 0 });
  if (touch.mode === 'pinch') endPinch();
  if (touch.timer) clearTimeout(touch.timer);
  touch = null;
});

// One-time help for the touch gestures (also ⋯ → ❔).
const HINTS = {
  trackpad: 'Kéo 1 ngón: di chuột\nChạm: click · Chạm 2 lần: double-click\nChạm 2 ngón hoặc giữ: click phải\nGiữ rồi kéo: kéo thả\n2 ngón kéo: cuộn\nChụm / mở 2 ngón: zoom',
  direct: 'Chạm: click tại chỗ chạm\nGiữ: click phải · Kéo: kéo thả\n2 ngón kéo: cuộn\nChụm / mở 2 ngón: zoom\nBấm nút nhỏ dễ hơn với Touchpad (⋯ → ☝).',
};

function showTouchHint() {
  const mode = isTrackpad() ? 'trackpad' : 'direct';
  $('touch-hint-body').textContent = HINTS[mode];
  $('touch-hint').hidden = false;
  prefs.set(`hint:${mode}`, true);
}

function maybeTouchHint() {
  if (IS_TOUCH && S && S.control && !prefs.get(`hint:${isTrackpad() ? 'trackpad' : 'direct'}`, false)) showTouchHint();
}

$('touch-hint-ok').onclick = () => { $('touch-hint').hidden = true; stage.focus(); };
$('btn-help').onclick = showTouchHint;

$('sel-touch').hidden = !IS_TOUCH;
$('btn-help').hidden = !IS_TOUCH;
$('sel-touch').value = prefs.get('touch', 'trackpad');
$('sel-touch').onchange = (e) => {
  prefs.set('touch', e.target.value);
  placeCursor();
  maybeTouchHint();
  stage.focus();
};

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

function pressCombo(mods, code) {
  for (const c of mods) send({ t: 'kd', c });
  send({ t: 'kd', c: code });
  send({ t: 'ku', c: code });
  for (const c of [...mods].reverse()) send({ t: 'ku', c });
}

// Typed keys pick up the Ctrl/Alt/⌘/⇧ armed on the key row.
function tapKey(code, shift) {
  const mods = takeMods();
  if (shift && !mods.includes('ShiftLeft')) mods.push('ShiftLeft');
  pressCombo(mods, code);
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
  if (keyboardShown()) { vkb.blur(); stage.focus(); } else { vkb.blur(); vkb.focus(); }
};

// Key row above the phone keyboard. A modifier tap arms it for the next key;
// a double tap locks it until tapped again. Arrows etc. repeat while held.
const keyrow = $('keyrow');
const armed = new Map(); // code → { lock, at }

function renderMods() {
  for (const b of keyrow.querySelectorAll('[data-mod]')) {
    const a = armed.get(b.dataset.mod);
    b.classList.toggle('armed', !!a && !a.lock);
    b.classList.toggle('locked', !!a && a.lock);
  }
}

function toggleMod(code) {
  const a = armed.get(code);
  if (!a) armed.set(code, { lock: false, at: performance.now() });
  else if (!a.lock && performance.now() - a.at < 400) a.lock = true;
  else armed.delete(code);
  renderMods();
}

function takeMods() {
  const list = [...armed.keys()];
  for (const [c, a] of armed) if (!a.lock) armed.delete(c);
  if (list.length) renderMods();
  return list;
}

let keyRepeat = null;
function stopKeyRepeat() {
  if (!keyRepeat) return;
  clearTimeout(keyRepeat.wait);
  clearInterval(keyRepeat.every);
  keyRepeat = null;
}

keyrow.addEventListener('pointerdown', (e) => {
  const b = e.target.closest('button');
  e.preventDefault(); // keep focus (and the phone keyboard) on the textarea
  if (!b) return;
  if (b.dataset.act === 'hide') { vkb.blur(); stage.focus(); return; }
  if (!S || !S.control) { nudgeMode(); return; }
  if (b.dataset.mod) { toggleMod(b.dataset.mod); return; }
  const code = b.dataset.k, mods = takeMods();
  pressCombo(mods, code);
  stopKeyRepeat();
  const r = { every: null };
  r.wait = setTimeout(() => { r.every = setInterval(() => pressCombo(mods, code), 70); }, 400);
  keyRepeat = r;
});
for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) keyrow.addEventListener(ev, stopKeyRepeat);
// No focus change and no ghost click: the key already fired on pointerdown.
keyrow.addEventListener('touchstart', (e) => e.preventDefault(), { passive: false });
keyrow.addEventListener('mousedown', (e) => e.preventDefault());

// While the phone keyboard is up, fit the session into what is left of the
// screen (visualViewport) so the keyboard does not cover the remote screen.
function fitToKeyboard() {
  const ses = $('session'), vv = window.visualViewport;
  const open = document.activeElement === vkb;
  keyrow.hidden = !open;
  if (!open || !vv) { ses.style.top = ses.style.height = ses.style.bottom = ''; return; }
  ses.style.top = `${vv.offsetTop}px`;
  ses.style.height = `${vv.height}px`;
  ses.style.bottom = 'auto';
  setTimeout(revealEdit, 60); // once the stage has its new size
}
vkb.addEventListener('focus', () => { $('kb-offer').hidden = true; fitToKeyboard(); });
vkb.addEventListener('blur', () => { stopKeyRepeat(); armed.clear(); renderMods(); fitToKeyboard(); restoreKbZoom(); });

// Typing on a phone: zoom to readable text and keep the field being typed in
// (the agent's caret, else the last tap) just above the keyboard. The zoom
// from before comes back when the keyboard closes.
const KB_SCALE = 0.75; // CSS px per remote px: small UI text stays legible

function revealEdit() {
  if (!S || !S.vw || !S.editAt || document.activeElement !== vkb) return;
  if (!S.kbSaved) S.kbSaved = { zoom: S.zoom, left: stage.scrollLeft, top: stage.scrollTop };
  const fit = Math.min(stage.clientWidth / S.vw, stage.clientHeight / S.vh);
  const want = Math.max(fit, KB_SCALE);
  if (canvas.offsetWidth / S.vw < want * 0.98) {
    S.zoom = String(+want.toFixed(3));
    fitCanvas();
    syncZoomSelect();
  }
  stage.scrollLeft = canvas.offsetLeft + S.editAt.x * canvas.offsetWidth - stage.clientWidth * 0.3;
  stage.scrollTop = canvas.offsetTop + S.editAt.y * canvas.offsetHeight - stage.clientHeight * 0.45;
  placeCursor();
}

function restoreKbZoom() {
  const k = S && S.kbSaved;
  if (!k) return;
  S.kbSaved = null;
  S.zoom = k.zoom;
  fitCanvas();
  syncZoomSelect();
  stage.scrollLeft = k.left;
  stage.scrollTop = k.top;
  placeCursor();
}

// A click landed in a text field: open the keyboard. iOS only opens it from
// a tap, so the web page offers a button; the iOS app can open it directly.
let kbOfferTimer = null;
function onEdit(m) {
  if (!IS_TOUCH || !S || !S.control) return;
  if (m.x != null) S.editAt = { x: m.x, y: m.y };
  const offer = $('kb-offer');
  if (document.activeElement === vkb) { if (m.on) revealEdit(); return; }
  if (!m.click) return;
  clearTimeout(kbOfferTimer);
  if (!m.on) { offer.hidden = true; return; }
  if (window.rdNative && window.rdNative.autoKeyboard) {
    vkb.focus();
    // iOS may still refuse a keyboard that no tap asked for: then offer the button.
    setTimeout(() => { if (!keyboardShown()) showKbOffer(); }, 700);
    return;
  }
  showKbOffer();
}

// The on-screen keyboard is up when the visual viewport lost a keyboard's height.
function keyboardShown() {
  const vv = window.visualViewport;
  return document.activeElement === vkb && !!vv && vv.height < window.innerHeight - 120;
}

function showKbOffer() {
  clearTimeout(kbOfferTimer);
  $('kb-offer').hidden = false;
  kbOfferTimer = setTimeout(() => { $('kb-offer').hidden = true; }, 6000);
}

// Blur first: focus() on an already focused field does not bring the keyboard back.
$('kb-offer').onclick = () => { $('kb-offer').hidden = true; vkb.blur(); vkb.focus(); };
if (window.visualViewport) {
  visualViewport.addEventListener('resize', fitToKeyboard);
  visualViewport.addEventListener('scroll', fitToKeyboard);
}

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

// ⌘V / Ctrl+V: the keydown is not cancelled, so the browser fires "paste" with
// our clipboard. Files are uploaded and pasted on the remote side (files.js);
// text goes to the remote clipboard before the V lands. Browsers that fire no
// paste event here fall back to reading the clipboard.
function pasteViaClipboardApi() {
  const code = S.pasteKey;
  S.pasteKey = null;
  clearTimeout(S.pasteTimer);
  if (navigator.clipboard && navigator.clipboard.readText) {
    S.gate = S.gate.then(async () => {
      try {
        const text = await Promise.race([navigator.clipboard.readText(), new Promise((_, rej) => setTimeout(rej, 1500))]);
        if (text && text !== S.lastClip) { S.lastClip = text; sendInput({ t: 'clip', text }); }
      } catch { /* permission denied or timeout: paste whatever the remote has */ }
    });
  }
  S.pressed.set('KeyV', code);
  send({ t: 'kd', c: code });
}

window.addEventListener('paste', (e) => {
  if (!S || !S.control || !S.pasteKey) return;
  e.preventDefault();
  clearTimeout(S.pasteTimer);
  const code = S.pasteKey;
  S.pasteKey = null;
  if (handlePasteEvent(e)) { S.swallowKeyUp = code; return; } // files.js sends the keystroke when done
  const text = e.clipboardData && e.clipboardData.getData('text/plain');
  if (text && text !== S.lastClip) { S.lastClip = text; send({ t: 'clip', text }); }
  S.pressed.set('KeyV', code);
  send({ t: 'kd', c: code });
});

// A keyup the page never sees (focus moved, OS shortcut, swallowed by the
// browser) leaves the key held on the remote: a stuck Ctrl turns every scroll
// into Ctrl+wheel there, i.e. zoom. Every input event carries the real modifier
// state, so release whatever the browser says is no longer down.
const MOD_FLAG = {
  ControlLeft: 'ctrlKey', ControlRight: 'ctrlKey', MetaLeft: 'metaKey', MetaRight: 'metaKey',
  AltLeft: 'altKey', AltRight: 'altKey', ShiftLeft: 'shiftKey', ShiftRight: 'shiftKey',
};
function syncModifiers(e) {
  if (!S || !S.control || !S.pressed.size) return;
  for (const [phys, c] of S.pressed) {
    if (MOD_FLAG[phys] && !e[MOD_FLAG[phys]]) { S.pressed.delete(phys); send({ t: 'ku', c }); }
  }
}

window.addEventListener('keydown', (e) => {
  if (!S || !S.control || keyTargetIsUi(e) || !e.code) return;
  syncModifiers(e);
  const code = mapCode(e.code);
  if (e.code === 'KeyV' && (e.ctrlKey || e.metaKey) && !e.repeat) {
    S.pasteKey = code;
    S.pasteTimer = setTimeout(() => { if (S && S.pasteKey) pasteViaClipboardApi(); }, 50);
    return;
  }
  e.preventDefault();
  if (e.code === 'CapsLock') { send({ t: 'kd', c: code }); send({ t: 'ku', c: code }); return; }
  S.pressed.set(e.code, code);
  send({ t: 'kd', c: code });
});

window.addEventListener('keyup', (e) => {
  // A key pressed on the screen and released over the toolbar still goes up.
  if (!S || !S.control || !e.code || (keyTargetIsUi(e) && !S.pressed.has(e.code))) return;
  e.preventDefault();
  const code = S.pressed.get(e.code) || mapCode(e.code); // as sent, even if ⌘/Ctrl swap changed since
  if (S.pasteKey === code) pasteViaClipboardApi(); // released before any paste event
  if (S.swallowKeyUp === code) { S.swallowKeyUp = null; return; }
  if (e.code === 'CapsLock') { send({ t: 'kd', c: code }); send({ t: 'ku', c: code }); return; }
  S.pressed.delete(e.code);
  send({ t: 'ku', c: code });
  // macOS browsers swallow keyup for keys released while ⌘ is held.
  if (IS_MAC_VIEWER && (e.code === 'MetaLeft' || e.code === 'MetaRight')) {
    for (const [phys, c] of [...S.pressed]) { S.pressed.delete(phys); send({ t: 'ku', c }); }
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
  if (COMPACT.matches) b.textContent = on ? '🖱 Điều khiển' : '👁 Xem';
  else b.textContent = on ? '🖱 Đang điều khiển' : '👁 Chỉ xem';
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
  nativeKeys();
  placeCursor();
  if (!on && document.activeElement === vkb) vkb.blur();
  stage.focus();
  if (on) maybeTouchHint();
}

function nudgeMode() {
  const b = $('btn-mode');
  b.classList.remove('pulse');
  void b.offsetWidth; // restart the animation
  b.classList.add('pulse');
}

$('btn-mode').onclick = () => setControl(!(S && S.control));
COMPACT.addEventListener('change', updateMode);

// ⋯ menu (phone-sized windows): closes after a choice or a tap elsewhere.
const tbExtra = $('tb-extra');
const closeMenu = () => tbExtra.classList.remove('open');
$('btn-more').onclick = () => { tbExtra.classList.toggle('open'); keepToolbar(); };
document.addEventListener('pointerdown', (e) => {
  if (tbExtra.classList.contains('open') && !tbExtra.contains(e.target) && e.target !== $('btn-more')) closeMenu();
}, true);
tbExtra.addEventListener('change', (e) => { if (e.target.tagName === 'SELECT') closeMenu(); });
tbExtra.addEventListener('click', (e) => { if (e.target.closest('button')) closeMenu(); });

// Phone in landscape (or fullscreen on a touch screen): the toolbar slides
// away to leave the whole height to the remote screen; ▾ brings it back.
const SHORT = matchMedia('(max-height: 500px)');
let tbTimer = null;
function updateTbAuto() {
  const auto = SHORT.matches || (!!document.fullscreenElement && IS_TOUCH);
  $('session').classList.toggle('tb-auto', auto);
  $('tb-handle').hidden = !auto;
  closeMenu();
  if (!auto) $('session').classList.remove('tb-show');
}
function keepToolbar() {
  clearTimeout(tbTimer);
  tbTimer = setTimeout(hideToolbar, 5000);
}
function hideToolbar() {
  const el = document.activeElement; // a native <select> picker may still be open
  if (tbExtra.classList.contains('open') || (el && el.tagName === 'SELECT' && $('toolbar').contains(el))) { keepToolbar(); return; }
  clearTimeout(tbTimer);
  $('session').classList.remove('tb-show');
}
$('tb-handle').onclick = () => { $('session').classList.add('tb-show'); keepToolbar(); };
$('toolbar').addEventListener('pointerdown', keepToolbar);
SHORT.addEventListener('change', updateTbAuto);
document.addEventListener('fullscreenchange', updateTbAuto);
updateTbAuto();

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
  if (v === 'update-agent') {
    // The agent downloads the current installer and runs it silently (no UAC: it is already elevated).
    if (confirm('Cập nhật agent trên máy kia lên bản mới nhất? Phiên sẽ ngắt khoảng 10–20 giây rồi tự nối lại.')) sendNow({ t: 'update' });
    return;
  }
  const keys = COMBOS[v];
  if (!keys) return;
  for (const c of keys) send({ t: 'kd', c });
  for (const c of [...keys].reverse()) send({ t: 'ku', c });
};

// iPhone Safari has no Fullscreen API; the iOS app is always fullscreen.
if (window.rdNative ? !window.rdNative.fullscreen : !$('session').requestFullscreen) $('btn-fs').hidden = true;

$('btn-fs').onclick = async () => {
  if (window.rdNative) { // desktop app window
    window.rdNative.fullscreen();
    stage.focus();
    if (window.rdNative.platform === 'windows' && !window.rdNative.keys) { // app from before 2026-10-15
      const hint = $('fs-hint');
      hint.textContent = 'App RemoteDesk này là bản cũ: phím Windows / Alt+Tab chưa gửi sang máy kia.\nCài lại bản "Agent + App" từ trang chủ để cập nhật.';
      hint.hidden = false;
      clearTimeout(fsHintTimer);
      fsHintTimer = setTimeout(() => { hint.hidden = true; }, 8000);
    }
    return;
  }
  if (document.fullscreenElement) { await document.exitFullscreen(); return; }
  if (!$('session').requestFullscreen) return; // iPhone Safari: no Fullscreen API
  await $('session').requestFullscreen();
  // Chrome/Edge: capture ⌘/Win/Alt+Tab etc. while fullscreen.
  if (navigator.keyboard && navigator.keyboard.lock) navigator.keyboard.lock().catch(() => {});
  stage.focus();
};

// Windows app: fullscreen is the app's own borderless window. While it is up
// and we are controlling, the app catches Win / Alt+Tab / Ctrl+Esc etc. and
// hands them over here, so the remote machine feels like the local one.
let nativeFs = false;
let fsHintTimer = null;
function nativeKeys() {
  if (window.rdNative && window.rdNative.keys) window.rdNative.keys(!!(S && S.control));
}
window.rdNativeFsChanged = (on) => {
  nativeFs = on;
  $('session').classList.toggle('native-fs', on);
  clearTimeout(fsHintTimer);
  const hint = $('fs-hint');
  hint.hidden = !on || !S;
  if (on && S) {
    hint.textContent = (S.control ? 'Phím Windows, Alt+Tab… đang gửi sang máy kia.\n' : '')
      + 'Thoát toàn màn hình: Ctrl+Alt+Enter, hoặc đưa chuột lên mép trên → ⛶';
    fsHintTimer = setTimeout(() => { hint.hidden = true; }, 5000);
  }
  setTimeout(fitCanvas, 50);
  stage.focus();
};
window.rdNativeKey = (code, down, ctrlKey, altKey, shiftKey, metaKey) => {
  window.dispatchEvent(new KeyboardEvent(down ? 'keydown' : 'keyup', { code, ctrlKey, altKey, shiftKey, metaKey, cancelable: true }));
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
  refreshPwStatus();
}

async function refreshPwStatus() {
  try {
    const r = await adminCall('GET', '/api/admin/password');
    $('pw-status').textContent = r.set ? '· đã đặt' : '· chưa đặt';
  } catch { /* shown by refreshUsers */ }
}

$('pw-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const msg = $('pw-msg');
  msg.hidden = false;
  if ($('pw-new').value !== $('pw-again').value) { msg.className = 'small error'; msg.textContent = 'Hai lần nhập không khớp.'; return; }
  try {
    const r = await fetch('/api/admin/password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: $('pw-new').value }) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
    msg.className = 'small muted';
    msg.textContent = 'Đã lưu. Lần sau đăng nhập bằng mật khẩu này.';
    $('pw-new').value = $('pw-again').value = '';
    refreshPwStatus();
  } catch (err) { msg.className = 'small error'; msg.textContent = `Lỗi: ${err.message}`; }
});

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
