'use strict';
// RemoteDesk relay: connects agents (controlled machines) with viewers (browser/app)
// and forwards bytes between them. It never decodes video, so it stays cheap.
//
//   agent  ──wss /ws/agent──►  relay  ◄──wss /ws/view──  viewer (browser)
//
// Any number of viewers can watch one agent; the agent encodes once and the
// relay fans the stream out.
//
// Agent → viewer binary frames: [type u8][flags u8][ts u32 BE][payload]
//   type 1 = H.264 access unit (Annex B), flags bit0 = keyframe.
// Everything else is JSON text and is forwarded verbatim, except:
//   {"t":"rtc",…}  WebRTC signaling for the direct (P2P) path. The relay stamps
//                  viewer→agent messages with "vid" and delivers agent→viewer
//                  ones only to viewer "to".
//   {"t":"p2p","on":bool} from a viewer: it now gets video P2P (or fell back).
//                  The relay stops/resumes its copy and tells the agent
//                  {"t":"wsvideo","on":bool} so the agent's uplink only carries
//                  the relay stream while someone still needs it.

const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { WebSocketServer } = require('ws');

const PORT = Number(process.env.PORT) || 8790;
const HOST = process.env.HOST || '127.0.0.1';
const TOKEN = process.env.REMOTE_TOKEN || '';
// PUBLIC_URL may list several origins (comma-separated); the first is the default.
const PUBLIC_URLS = (process.env.PUBLIC_URL || `http://localhost:${PORT}`).split(',').map((u) => u.trim().replace(/\/$/, '')).filter(Boolean);
const PUBLIC_URL = PUBLIC_URLS[0];
if (TOKEN.length < 24) {
  console.error('REMOTE_TOKEN is missing or shorter than 24 chars');
  process.exit(1);
}

const PUBLIC_DIR = path.join(__dirname, 'public');
const DIST_DIR = path.join(__dirname, 'dist');
const INSTALL_DIR = path.join(__dirname, 'install');
const COOKIE = 'rd_auth';
const COOKIE_VALUE = crypto.createHmac('sha256', TOKEN).update('viewer-cookie-v1').digest('base64url');

// Video backpressure: once a viewer's socket buffers this much, drop frames
// until the next keyframe (dropping deltas would corrupt the picture anyway).
const CONGESTED_BYTES = 3 * 1024 * 1024;
const RESUME_BYTES = 512 * 1024;
// Frames since the last keyframe are kept so a new viewer gets a picture at once.
const GOP_CACHE_BYTES = 12 * 1024 * 1024;

// ---------------------------------------------------------------- auth

function safeEqual(a, b) {
  const ha = crypto.createHash('sha256').update(String(a)).digest();
  const hb = crypto.createHash('sha256').update(String(b)).digest();
  return crypto.timingSafeEqual(ha, hb);
}

const failures = new Map(); // ip -> {count, first, blockedUntil}

function clientIp(req) {
  return req.headers['x-real-ip'] || req.socket.remoteAddress || '?';
}

function isBlocked(ip) {
  const f = failures.get(ip);
  return !!(f && f.blockedUntil > Date.now());
}

function noteFailure(ip) {
  const now = Date.now();
  let f = failures.get(ip);
  if (!f || now - f.first > 10 * 60_000) f = { count: 0, first: now, blockedUntil: 0 };
  f.count++;
  if (f.count >= 10) f.blockedUntil = now + 15 * 60_000;
  failures.set(ip, f);
  console.warn(`auth failure from ${ip} (${f.count})`);
}

function parseCookies(req) {
  const out = {};
  for (const part of (req.headers.cookie || '').split(';')) {
    const i = part.indexOf('=');
    if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim();
  }
  return out;
}

function bearer(req, url) {
  const h = req.headers.authorization || '';
  if (h.startsWith('Bearer ')) return h.slice(7);
  return url.searchParams.get('t') || '';
}

function viewerAuthed(req) {
  const c = parseCookies(req)[COOKIE];
  return !!c && safeEqual(c, COOKIE_VALUE);
}

function tokenAuthed(req, url) {
  const t = bearer(req, url);
  return !!t && safeEqual(t, TOKEN);
}

// ---------------------------------------------------------------- state

/** @type {Map<string, {ws, id, name, os, ver, since, viewers, params, gop, lastInfo}>} */
const agents = new Map();
let viewerSeq = 0;

function agentSummary(a) {
  return { id: a.id, name: a.name, os: a.os, ver: a.ver, since: a.since, busy: a.viewers.size > 0, watching: a.viewers.size };
}

// ---------------------------------------------------------------- http

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.json': 'application/json',
  '.webmanifest': 'application/manifest+json',
};

function send(res, status, body, headers = {}) {
  res.writeHead(status, { 'Cache-Control': 'no-store', ...headers });
  res.end(body);
}

function serveStatic(res, file) {
  fs.readFile(file, (err, data) => {
    if (err) return send(res, 404, 'not found');
    send(res, 200, data, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' });
  });
}

function readBody(req, limit = 4096) {
  return new Promise((resolve, reject) => {
    let size = 0;
    const chunks = [];
    req.on('data', (c) => {
      size += c.length;
      if (size > limit) { reject(new Error('too large')); req.destroy(); return; }
      chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

// The origin the request came in on, if it is one of ours (so installers point back at it).
function requestOrigin(req) {
  const o = `https://${req.headers.host}`;
  return PUBLIC_URLS.includes(o) ? o : PUBLIC_URL;
}

// Two install flavours: agent only (?app=0), or agent + RemoteDesk viewer app (default).
function renderInstaller(name, req, withApp) {
  const tmpl = fs.readFileSync(path.join(INSTALL_DIR, name), 'utf8');
  return tmpl.replaceAll('{{SERVER}}', requestOrigin(req)).replaceAll('{{TOKEN}}', TOKEN)
    .replaceAll('{{WITH_APP}}', withApp ? '1' : '0');
}

// Windows Setup.exe: one base binary; the config is appended at download time
// as [json][magic "RDCFG1"][uint32 LE length] (see setup-win/main.go).
const SETUP_EXES = {
  '/dl/RemoteDesk-Setup.exe': true, // agent + app
  '/dl/RemoteDesk-Agent-Setup.exe': false, // agent only
};

function setupExe(req, app) {
  const base = fs.readFileSync(path.join(DIST_DIR, 'RemoteDesk-Setup-base.exe'));
  const cfg = Buffer.from(JSON.stringify({ server: requestOrigin(req), token: TOKEN, app }));
  const len = Buffer.alloc(4);
  len.writeUInt32LE(cfg.length);
  return Buffer.concat([base, cfg, Buffer.from('RDCFG1'), len]);
}

const SETUP_CMDS = {
  '/dl/RemoteDesk-Setup.cmd': true, // agent + app
  '/dl/RemoteDesk-Agent-Setup.cmd': false, // agent only
};

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://x');
  const p = url.pathname;
  const ip = clientIp(req);

  if (p === '/healthz') return send(res, 200, 'ok');

  if (p === '/login' && req.method === 'POST') {
    if (isBlocked(ip)) return send(res, 429, 'Too many attempts, try later');
    let token = '';
    try { token = (JSON.parse(await readBody(req)).token || '').trim(); } catch { /* bad body */ }
    if (!token || !safeEqual(token, TOKEN)) { noteFailure(ip); return send(res, 401, 'invalid'); }
    failures.delete(ip);
    const secure = PUBLIC_URL.startsWith('https:') ? '; Secure' : '';
    return send(res, 204, '', {
      'Set-Cookie': `${COOKIE}=${COOKIE_VALUE}; Path=/; HttpOnly; SameSite=Strict; Max-Age=31536000${secure}`,
    });
  }

  if (p === '/logout') {
    return send(res, 302, '', { 'Set-Cookie': `${COOKIE}=; Path=/; Max-Age=0`, Location: '/' });
  }

  if (p === '/api/agents') {
    if (!viewerAuthed(req)) return send(res, 401, '{}', { 'Content-Type': 'application/json' });
    const list = [...agents.values()].map(agentSummary).sort((a, b) => a.name.localeCompare(b.name));
    return send(res, 200, JSON.stringify(list), { 'Content-Type': 'application/json' });
  }

  // Install commands for the logged-in viewer (they embed the token).
  if (p === '/api/install') {
    if (!viewerAuthed(req)) return send(res, 401, '{}', { 'Content-Type': 'application/json' });
    const base = requestOrigin(req);
    const ps = (app) => `irm "${base}/install.ps1?t=${TOKEN}${app ? '' : '&app=0'}" | iex`;
    const sh = (app) => `curl -fsSL "${base}/install.sh?t=${TOKEN}${app ? '' : '&app=0'}" | bash`;
    return send(res, 200, JSON.stringify({
      full: {
        windowsExe: `${base}/dl/RemoteDesk-Setup.exe?t=${TOKEN}`,
        macPkg: `${base}/dl/RemoteDesk-Installer.pkg?t=${TOKEN}`,
        windowsCmd: `${base}/dl/RemoteDesk-Setup.cmd?t=${TOKEN}`, windowsPs: ps(true), mac: sh(true),
      },
      agent: {
        windowsExe: `${base}/dl/RemoteDesk-Agent-Setup.exe?t=${TOKEN}`,
        macPkg: `${base}/dl/RemoteDesk-Agent-Installer.pkg?t=${TOKEN}`,
        windowsCmd: `${base}/dl/RemoteDesk-Agent-Setup.cmd?t=${TOKEN}`, windowsPs: ps(false), mac: sh(false),
      },
    }), { 'Content-Type': 'application/json' });
  }

  if (p in SETUP_EXES) {
    if (!tokenAuthed(req, url) && !viewerAuthed(req)) { noteFailure(ip); return send(res, 401, 'unauthorized'); }
    let body;
    try { body = setupExe(req, SETUP_EXES[p]); } catch { return send(res, 404, 'not built'); }
    return send(res, 200, body, {
      'Content-Type': 'application/vnd.microsoft.portable-executable',
      'Content-Disposition': `attachment; filename="${path.basename(p)}"`,
    });
  }

  // Double-clickable Windows installers: a .cmd that runs the PowerShell script.
  if (p in SETUP_CMDS) {
    if (!tokenAuthed(req, url) && !viewerAuthed(req)) { noteFailure(ip); return send(res, 401, 'unauthorized'); }
    const base = requestOrigin(req);
    const app = SETUP_CMDS[p];
    const body = [
      '@echo off',
      `title RemoteDesk Setup${app ? '' : ' (agent)'}`,
      `powershell -NoProfile -ExecutionPolicy Bypass -Command "irm '${base}/install.ps1?t=${TOKEN}${app ? '' : '&app=0'}' | iex"`,
      'pause',
      '',
    ].join('\r\n');
    return send(res, 200, body, {
      'Content-Type': 'application/octet-stream',
      'Content-Disposition': `attachment; filename="${path.basename(p)}"`,
    });
  }

  if (p === '/install.ps1' || p === '/install.sh' || p.startsWith('/dl/')) {
    if (isBlocked(ip)) return send(res, 429, 'Too many attempts, try later');
    if (!tokenAuthed(req, url) && !viewerAuthed(req)) { noteFailure(ip); return send(res, 401, 'unauthorized'); }
    if (p === '/install.ps1') return send(res, 200, renderInstaller('install.ps1', req, url.searchParams.get('app') !== '0'), { 'Content-Type': 'text/plain; charset=utf-8' });
    if (p === '/install.sh') return send(res, 200, renderInstaller('install.sh', req, url.searchParams.get('app') !== '0'), { 'Content-Type': 'text/plain; charset=utf-8' });
    const name = path.basename(p.slice(4));
    const file = path.join(DIST_DIR, name);
    return fs.stat(file, (err, st) => {
      if (err || !st.isFile()) return send(res, 404, 'not found');
      res.writeHead(200, {
        'Content-Type': 'application/octet-stream',
        'Content-Length': st.size,
        'Content-Disposition': `attachment; filename="${name}"`,
      });
      fs.createReadStream(file).pipe(res);
    });
  }

  if (req.method !== 'GET') return send(res, 405, 'method not allowed');
  const rel = p === '/' ? 'index.html' : p.slice(1);
  const file = path.normalize(path.join(PUBLIC_DIR, rel));
  if (!file.startsWith(PUBLIC_DIR + path.sep)) return send(res, 404, 'not found');
  serveStatic(res, file);
});

// ---------------------------------------------------------------- websockets

const wss = new WebSocketServer({ noServer: true, perMessageDeflate: false, maxPayload: 16 * 1024 * 1024 });

server.on('upgrade', (req, socket, head) => {
  const url = new URL(req.url, 'http://x');
  const ip = clientIp(req);
  const reject = (code, msg) => {
    socket.write(`HTTP/1.1 ${code} ${msg}\r\nConnection: close\r\n\r\n`);
    socket.destroy();
  };
  if (isBlocked(ip)) return reject(429, 'Too Many Requests');

  if (url.pathname === '/ws/agent') {
    if (!tokenAuthed(req, url)) { noteFailure(ip); return reject(401, 'Unauthorized'); }
    return wss.handleUpgrade(req, socket, head, (ws) => onAgent(ws, url, ip));
  }
  if (url.pathname === '/ws/view') {
    // Cookie is SameSite=Strict; the Origin check additionally stops cross-site WS hijacking.
    const origin = req.headers.origin || '';
    if (!viewerAuthed(req) || (origin && !PUBLIC_URLS.includes(origin))) { noteFailure(ip); return reject(401, 'Unauthorized'); }
    return wss.handleUpgrade(req, socket, head, (ws) => onViewer(ws, url, ip));
  }
  reject(404, 'Not Found');
});

function sendJson(ws, obj) {
  if (ws && ws.readyState === ws.OPEN) ws.send(JSON.stringify(obj));
}

function cleanLabel(s, fallback, max = 64) {
  const v = String(s || '').replace(/[^\p{L}\p{N} ._\-@()]/gu, '').trim().slice(0, max);
  return v || fallback;
}

function onAgent(ws, url, ip) {
  const id = cleanLabel(url.searchParams.get('id'), '', 48).replace(/\s+/g, '-').toLowerCase();
  if (!id) return ws.close(4400, 'missing id');
  const agent = {
    ws,
    id,
    name: cleanLabel(url.searchParams.get('name'), id),
    os: cleanLabel(url.searchParams.get('os'), '?', 16),
    ver: cleanLabel(url.searchParams.get('ver'), '?', 16),
    since: Date.now(),
    viewers: new Set(),
    params: null, // stream params the agent is currently encoding with
    gop: [], // frames since the last keyframe, replayed to viewers who join mid-stream
    gopBytes: 0,
    lastInfo: null,
    wsOn: true, // agent sends video to the relay (it starts that way on every join)
  };
  const old = agents.get(id);
  if (old) {
    // Hand the viewers over to the new connection (agent restarted / network changed).
    agent.viewers = old.viewers;
    agent.params = old.params;
    old.viewers = new Set();
    old.ws.close(4000, 'replaced');
    // The old agent's peer connections are gone: everyone back on the relay
    // stream; viewers renegotiate P2P.
    for (const v of agent.viewers) { v.p2p = false; sendJson(v.ws, { t: 'rtc-reset' }); }
  }
  agents.set(id, agent);
  ws.isAlive = true;
  console.log(`agent up: ${id} (${agent.name}, ${agent.os} ${agent.ver}) from ${ip}`);
  if (agent.viewers.size) sendJson(ws, { t: 'join', ...agent.params });

  ws.on('pong', () => { ws.isAlive = true; });
  ws.on('message', (data, isBinary) => {
    if (!isBinary) {
      const text = data.toString();
      if (text.startsWith('{"t":"info"') || text.includes('"t":"info"')) agent.lastInfo = text;
      let to = null;
      if (text.startsWith('{"t":"rtc"')) { try { to = JSON.parse(text).to; } catch { return; } }
      for (const v of agent.viewers) if ((to == null || v.vid === to) && v.ws.readyState === v.ws.OPEN) v.ws.send(text);
      return;
    }
    if (data[0] === 1) {
      const key = (data[1] & 1) === 1;
      if (key) { agent.gop = [data]; agent.gopBytes = data.length; }
      else if (agent.gop.length) {
        agent.gop.push(data);
        agent.gopBytes += data.length;
        if (agent.gopBytes > GOP_CACHE_BYTES) { agent.gop = []; agent.gopBytes = 0; }
      }
      for (const v of agent.viewers) sendVideo(v, data, key);
      return;
    }
    for (const v of agent.viewers) if (v.ws.readyState === v.ws.OPEN) v.ws.send(data, { binary: true });
  });
  ws.on('close', (code) => {
    if (agents.get(id) !== agent) return; // replaced
    agents.delete(id);
    console.log(`agent down: ${id} (${code})`);
    for (const v of agent.viewers) v.ws.close(4404, 'agent offline');
  });
  ws.on('error', () => {});
}

// Per-viewer backpressure: a slow viewer (phone on 4G) drops frames until the
// next keyframe without slowing anyone else down.
function sendVideo(v, data, key) {
  if (v.p2p || v.ws.readyState !== v.ws.OPEN) return;
  const buffered = v.ws.bufferedAmount;
  if (v.dropping) {
    if (!key || buffered > RESUME_BYTES) { v.dropped++; return; }
    v.dropping = false;
  } else if (buffered > CONGESTED_BYTES) {
    v.dropping = true;
    v.dropped++;
    return;
  }
  v.ws.send(data, { binary: true });
}

// Keep the agent's relay stream on only while some viewer is not on P2P.
function updateWsVideo(agent) {
  const need = [...agent.viewers].some((v) => !v.p2p);
  if (need === agent.wsOn) return;
  agent.wsOn = need;
  if (!need) { agent.gop = []; agent.gopBytes = 0; } // goes stale while the agent isn't sending
  sendJson(agent.ws, { t: 'wsvideo', on: need });
}

// Bring one viewer up to date: the cached keyframe + deltas since then.
function replayGop(agent, v) {
  v.dropping = false;
  for (const f of agent.gop) v.ws.send(f, { binary: true });
}

function onViewer(ws, url, ip) {
  const id = String(url.searchParams.get('id') || '');
  const agent = agents.get(id);
  if (!agent) return ws.close(4404, 'agent offline');
  const num = (k, d, lo, hi) => Math.min(hi, Math.max(lo, Number(url.searchParams.get(k)) || d));
  const viewer = { ws, vid: ++viewerSeq, dropping: false, dropped: 0, p2p: false };
  const params = { fps: num('fps', 30, 5, 60), maxw: num('maxw', 1920, 640, 3840), kbps: num('kbps', 3000, 300, 20000) };
  const first = agent.viewers.size === 0;
  agent.viewers.add(viewer);
  ws.isAlive = true;
  console.log(`viewer join: ${id} from ${ip} (${agent.viewers.size} watching)`);
  sendJson(ws, { t: 'hello', agent: agentSummary(agent) });
  if (agent.lastInfo) ws.send(agent.lastInfo);
  if (first) {
    // Nobody was watching: start the agent's capture with this viewer's quality.
    agent.params = params;
    agent.gop = []; agent.gopBytes = 0;
    sendJson(agent.ws, { t: 'join', ...params });
  } else if (agent.wsOn) {
    replayGop(agent, viewer); // already streaming: join instantly, no restart
  } else {
    updateWsVideo(agent); // everyone else is on P2P: the agent resumes at a keyframe
  }

  ws.on('pong', () => { ws.isAlive = true; });
  ws.on('message', (data, isBinary) => {
    const a = agents.get(id);
    if (!a || !a.viewers.has(viewer)) return;
    if (!isBinary) {
      const text = data.toString();
      if (text === '{"t":"kf"}' && a.gop.length) { replayGop(a, viewer); return; } // decoder reset: serve from cache
      if (text.startsWith('{"t":"rtc"')) {
        let m;
        try { m = JSON.parse(text); } catch { return; }
        m.vid = viewer.vid;
        sendJson(a.ws, m);
        return;
      }
      if (text.startsWith('{"t":"p2p"')) {
        let on = false;
        try { on = JSON.parse(text).on === true; } catch { return; }
        if (on === viewer.p2p) return;
        viewer.p2p = on;
        console.log(`viewer ${id}#${viewer.vid}: ${on ? 'P2P' : 'back on relay'}`);
        if (!on && a.wsOn) {
          // Relay stream still running for others: resume from the cached GOP,
          // or ask the agent if there is none yet.
          if (a.gop.length) replayGop(a, viewer); else sendJson(a.ws, { t: 'kf' });
        }
        updateWsVideo(a);
        return;
      }
      if (text.startsWith('{"t":"cfg"')) {
        try { const m = JSON.parse(text); a.params = { fps: m.fps, maxw: m.maxw, kbps: m.kbps }; } catch { /* ignore */ }
      }
    }
    if (a.ws.readyState === a.ws.OPEN) a.ws.send(data, { binary: isBinary });
  });
  ws.on('close', () => {
    const a = agents.get(id);
    if (!a || !a.viewers.delete(viewer)) return;
    console.log(`viewer leave: ${id} (dropped ${viewer.dropped} frames, ${a.viewers.size} still watching)`);
    sendJson(a.ws, { t: 'rtc-bye', vid: viewer.vid });
    if (a.viewers.size === 0) {
      a.gop = []; a.gopBytes = 0;
      a.wsOn = true; // the agent resets to relay video on the next join
      sendJson(a.ws, { t: 'leave' });
    } else {
      updateWsVideo(a);
    }
  });
  ws.on('error', () => {});
}

// Ping both sides so dead NAT mappings get noticed within ~40 s.
setInterval(() => {
  for (const ws of wss.clients) {
    if (!ws.isAlive) { ws.terminate(); continue; }
    ws.isAlive = false;
    try { ws.ping(); } catch { /* closing */ }
  }
}, 20_000).unref();

server.listen(PORT, HOST, () => console.log(`remote-desk relay on ${HOST}:${PORT} (${PUBLIC_URLS.join(', ')})`));
