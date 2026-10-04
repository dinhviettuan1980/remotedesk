'use strict';
// RemoteDesk relay: connects agents (controlled machines) with viewers (browser/app)
// and forwards bytes between them. It never decodes video, so it stays cheap.
//
//   agent  ──wss /ws/agent──►  relay  ◄──wss /ws/view──  viewer (browser)
//
// Any number of viewers can watch one agent; the agent encodes once and the
// relay fans the stream out.
//
// Every token is its own tenant (see users.js): an agent belongs to the token
// it connected with, and a browser only sees agents of the token it logged in
// with. The admin token (REMOTE_TOKEN) also manages users on /admin.
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
//   {"t":"f…",…}   file transfer (agent/files.go): stamped with "vid" like rtc;
//                  agent replies carrying "to" go to that viewer only.
// Binary type 2 = file data. Viewer → agent [2][id u32][…] is forwarded as
// [2][vid u32][id u32][…]; agent → viewer [2][vid u32][…] goes to that viewer.

const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { WebSocketServer } = require('ws');
const { Users } = require('./users');

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
// Outside relay/ so deploys (rsync --delete) never touch it.
const USERS_FILE = process.env.USERS_FILE || path.join(__dirname, '..', 'data', 'users.json');
const COOKIE = 'rd_auth';
// v1 cookies (single-token era) keep working as the admin.
const COOKIE_V1 = crypto.createHmac('sha256', TOKEN).update('viewer-cookie-v1').digest('base64url');

// Video backpressure: once a viewer's socket buffers this much, drop frames
// until the next keyframe (dropping deltas would corrupt the picture anyway).
const CONGESTED_BYTES = 3 * 1024 * 1024;
const RESUME_BYTES = 512 * 1024;
// Frames since the last keyframe are kept so a new viewer gets a picture at once.
const GOP_CACHE_BYTES = 12 * 1024 * 1024;

const users = new Users(USERS_FILE, TOKEN);

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

// Cookie = "<tenant id>.<HMAC over id + that tenant's current token>": rotating
// or revoking a token logs its browsers out, and the cookie never holds a token.
function cookieFor(t) {
  return `${t.id}.${crypto.createHmac('sha256', TOKEN).update(`viewer-cookie-v2\n${t.id}\n${t.token}`).digest('base64url')}`;
}

/** Tenant of a logged-in browser, or null. */
function viewerTenant(req) {
  const c = parseCookies(req)[COOKIE];
  if (!c) return null;
  if (safeEqual(c, COOKIE_V1)) return users.admin;
  const t = users.byId(c.slice(0, c.indexOf('.')));
  return t && safeEqual(c, cookieFor(t)) ? t : null;
}

/** Tenant of a token in the Authorization header or ?t=, or null. */
function tokenTenant(req, url) {
  return users.byToken(bearer(req, url));
}

// ---------------------------------------------------------------- state

/** Agents by "<tenant>/<id>": machine ids only need to be unique within a tenant. */
const agents = new Map();
let viewerSeq = 0;

const agentKey = (tenant, id) => `${tenant}/${id}`;

function agentSummary(a) {
  return { id: a.id, name: a.name, os: a.os, ver: a.ver, since: a.since, busy: a.viewers.size > 0, watching: a.viewers.size };
}

function tenantAgents(tenantId) {
  return [...agents.values()].filter((a) => a.tenant === tenantId);
}

// Token revoked / rotated / user deleted: drop their machines and viewers now.
function kickTenant(tenantId) {
  for (const a of tenantAgents(tenantId)) {
    for (const v of a.viewers) v.ws.close(4401, 'revoked');
    a.ws.close(4401, 'revoked');
  }
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

function sendJsonRes(res, status, obj) {
  send(res, status, JSON.stringify(obj), { 'Content-Type': 'application/json' });
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
function renderInstaller(name, req, token, withApp) {
  const tmpl = fs.readFileSync(path.join(INSTALL_DIR, name), 'utf8');
  return tmpl.replaceAll('{{SERVER}}', requestOrigin(req)).replaceAll('{{TOKEN}}', token)
    .replaceAll('{{WITH_APP}}', withApp ? '1' : '0');
}

// Links and commands that install a machine into tenant t (they embed its token).
function installLinks(base, t) {
  const tk = encodeURIComponent(t.token);
  const ps = (app) => `irm "${base}/install.ps1?t=${tk}${app ? '' : '&app=0'}" | iex`;
  const sh = (app) => `curl -fsSL "${base}/install.sh?t=${tk}${app ? '' : '&app=0'}" | bash`;
  return {
    full: {
      windowsExe: `${base}/dl/RemoteDesk-Setup.exe?t=${tk}`,
      macPkg: `${base}/dl/RemoteDesk-Installer.pkg?t=${tk}`,
      windowsCmd: `${base}/dl/RemoteDesk-Setup.cmd?t=${tk}`, windowsPs: ps(true), mac: sh(true),
    },
    agent: {
      windowsExe: `${base}/dl/RemoteDesk-Agent-Setup.exe?t=${tk}`,
      macPkg: `${base}/dl/RemoteDesk-Agent-Installer.pkg?t=${tk}`,
      windowsCmd: `${base}/dl/RemoteDesk-Agent-Setup.cmd?t=${tk}`, windowsPs: ps(false), mac: sh(false),
    },
  };
}

// Windows Setup.exe: one base binary; the config is appended at download time
// as [json][magic "RDCFG1"][uint32 LE length] (see setup-win/main.go).
const SETUP_EXES = {
  '/dl/RemoteDesk-Setup.exe': true, // agent + app
  '/dl/RemoteDesk-Agent-Setup.exe': false, // agent only
};

function setupExe(req, token, app) {
  const base = fs.readFileSync(path.join(DIST_DIR, 'RemoteDesk-Setup-base.exe'));
  const cfg = Buffer.from(JSON.stringify({ server: requestOrigin(req), token, app }));
  const len = Buffer.alloc(4);
  len.writeUInt32LE(cfg.length);
  return Buffer.concat([base, cfg, Buffer.from('RDCFG1'), len]);
}

const SETUP_CMDS = {
  '/dl/RemoteDesk-Setup.cmd': true, // agent + app
  '/dl/RemoteDesk-Agent-Setup.cmd': false, // agent only
};

function cleanName(s) {
  return String(s || '').replace(/[\u0000-\u001f<>]/g, '').trim().slice(0, 64);
}

// /api/admin/... — the admin manages users. It never sees their machines, only
// how many are online.
async function adminApi(req, res, p) {
  // State changes need our own Origin (the cookie is SameSite=Strict as well).
  if (req.method !== 'GET' && !PUBLIC_URLS.includes(req.headers.origin || '')) return sendJsonRes(res, 403, { error: 'origin' });
  const base = requestOrigin(req);
  const view = (u) => ({
    id: u.id, name: u.name, created: u.created, disabled: !!u.disabled, token: u.token,
    online: tenantAgents(u.id).length, install: installLinks(base, u),
  });
  const body = async () => { try { return JSON.parse(await readBody(req)); } catch { return {}; } };

  if (p === '/api/admin/password') {
    if (req.method === 'GET') return sendJsonRes(res, 200, { set: users.adminPasswordSet() });
    if (req.method !== 'POST') return sendJsonRes(res, 405, { error: 'method' });
    const pw = String((await body()).password || '');
    if (pw.length < 10 || pw.length > 200) return sendJsonRes(res, 400, { error: 'Mật khẩu cần 10–200 ký tự' });
    users.setAdminPassword(pw);
    console.log('admin: password changed');
    return sendJsonRes(res, 200, { set: true });
  }
  if (p === '/api/admin/users' && req.method === 'GET') return sendJsonRes(res, 200, users.list().map(view));
  if (p === '/api/admin/users' && req.method === 'POST') {
    const name = cleanName((await body()).name);
    if (!name) return sendJsonRes(res, 400, { error: 'name' });
    const u = users.create(name);
    console.log(`admin: created user ${u.id} (${u.name})`);
    return sendJsonRes(res, 200, view(u));
  }
  const m = p.match(/^\/api\/admin\/users\/(u[0-9a-f]{10})(?:\/(rotate|disable|enable|rename))?$/);
  if (!m) return sendJsonRes(res, 404, { error: 'not found' });
  const [, id, action] = m;
  if (req.method === 'DELETE' && !action) {
    kickTenant(id);
    if (!users.remove(id)) return sendJsonRes(res, 404, { error: 'not found' });
    console.log(`admin: deleted user ${id}`);
    return sendJsonRes(res, 200, { ok: true });
  }
  if (req.method !== 'POST' || !action) return sendJsonRes(res, 405, { error: 'method' });
  let u;
  if (action === 'rename') {
    const name = cleanName((await body()).name);
    if (!name) return sendJsonRes(res, 400, { error: 'name' });
    u = users.rename(id, name);
  } else {
    kickTenant(id); // old token (rotate) or the whole user (disable) stops working now
    u = action === 'rotate' ? users.rotate(id) : users.setDisabled(id, action === 'disable');
  }
  if (!u) return sendJsonRes(res, 404, { error: 'not found' });
  console.log(`admin: ${action} user ${id}`);
  return sendJsonRes(res, 200, view(u));
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://x');
  const p = url.pathname;
  const ip = clientIp(req);

  if (p === '/healthz') return send(res, 200, 'ok');

  if (p === '/login' && req.method === 'POST') {
    if (isBlocked(ip)) return send(res, 429, 'Too many attempts, try later');
    let token = '';
    try { token = (JSON.parse(await readBody(req)).token || '').trim(); } catch { /* bad body */ }
    // A user's token, the admin token, or the admin password.
    const t = users.byToken(token) || (users.checkAdminPassword(token) ? users.admin : null);
    if (!t) { noteFailure(ip); return send(res, 401, 'invalid'); }
    failures.delete(ip);
    const secure = PUBLIC_URL.startsWith('https:') ? '; Secure' : '';
    return send(res, 204, '', {
      'Set-Cookie': `${COOKIE}=${cookieFor(t)}; Path=/; HttpOnly; SameSite=Strict; Max-Age=31536000${secure}`,
    });
  }

  if (p === '/logout') {
    return send(res, 302, '', { 'Set-Cookie': `${COOKIE}=; Path=/; Max-Age=0`, Location: '/' });
  }

  if (p.startsWith('/api/')) {
    const t = viewerTenant(req);
    if (!t) return sendJsonRes(res, 401, {});
    if (p === '/api/me') return sendJsonRes(res, 200, { name: t.name, admin: !!t.admin });
    if (p === '/api/agents') {
      const list = tenantAgents(t.id).map(agentSummary).sort((a, b) => a.name.localeCompare(b.name));
      return sendJsonRes(res, 200, list);
    }
    // Install commands for the logged-in viewer's own tenant.
    if (p === '/api/install') return sendJsonRes(res, 200, installLinks(requestOrigin(req), t));
    if (p.startsWith('/api/admin/')) {
      if (!t.admin) return sendJsonRes(res, 403, { error: 'admin only' });
      return adminApi(req, res, p);
    }
    return sendJsonRes(res, 404, {});
  }

  if (p in SETUP_EXES || p in SETUP_CMDS || p === '/install.ps1' || p === '/install.sh' || p.startsWith('/dl/')) {
    if (isBlocked(ip)) return send(res, 429, 'Too many attempts, try later');
    // Installers are rendered for the token in the link (or the logged-in tenant).
    const t = tokenTenant(req, url) || viewerTenant(req);
    if (!t) { noteFailure(ip); return send(res, 401, 'unauthorized'); }
    const app = url.searchParams.get('app') !== '0';

    if (p in SETUP_EXES) {
      let body;
      try { body = setupExe(req, t.token, SETUP_EXES[p]); } catch { return send(res, 404, 'not built'); }
      return send(res, 200, body, {
        'Content-Type': 'application/vnd.microsoft.portable-executable',
        'Content-Disposition': `attachment; filename="${path.basename(p)}"`,
      });
    }
    // Double-clickable Windows installers: a .cmd that runs the PowerShell script.
    if (p in SETUP_CMDS) {
      const body = [
        '@echo off',
        `title RemoteDesk Setup${SETUP_CMDS[p] ? '' : ' (agent)'}`,
        `powershell -NoProfile -ExecutionPolicy Bypass -Command "irm '${requestOrigin(req)}/install.ps1?t=${encodeURIComponent(t.token)}${SETUP_CMDS[p] ? '' : '&app=0'}' | iex"`,
        'pause',
        '',
      ].join('\r\n');
      return send(res, 200, body, {
        'Content-Type': 'application/octet-stream',
        'Content-Disposition': `attachment; filename="${path.basename(p)}"`,
      });
    }
    if (p === '/install.ps1') return send(res, 200, renderInstaller('install.ps1', req, t.token, app), { 'Content-Type': 'text/plain; charset=utf-8' });
    if (p === '/install.sh') return send(res, 200, renderInstaller('install.sh', req, t.token, app), { 'Content-Type': 'text/plain; charset=utf-8' });
    // Everything in dist/ is tenant-neutral (the macOS .pkg holds no token: its
    // postinstall reads it from the download URL macOS records).
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
    // Not counted as a failure: a revoked machine retries every 30 s and would
    // lock its whole IP out (and tokens are 256-bit, so guessing is hopeless).
    const t = tokenTenant(req, url);
    if (!t) return reject(401, 'Unauthorized');
    return wss.handleUpgrade(req, socket, head, (ws) => onAgent(ws, url, ip, t));
  }
  if (url.pathname === '/ws/view') {
    // Cookie is SameSite=Strict; the Origin check additionally stops cross-site WS hijacking.
    const origin = req.headers.origin || '';
    const t = viewerTenant(req);
    if (!t || (origin && !PUBLIC_URLS.includes(origin))) { noteFailure(ip); return reject(401, 'Unauthorized'); }
    return wss.handleUpgrade(req, socket, head, (ws) => onViewer(ws, url, ip, t));
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

function onAgent(ws, url, ip, tenant) {
  const id = cleanLabel(url.searchParams.get('id'), '', 48).replace(/\s+/g, '-').toLowerCase();
  if (!id) return ws.close(4400, 'missing id');
  const key = agentKey(tenant.id, id);
  const agent = {
    ws,
    id,
    tenant: tenant.id,
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
  const old = agents.get(key);
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
  agents.set(key, agent);
  ws.isAlive = true;
  console.log(`agent up: ${key} (${agent.name}, ${agent.os} ${agent.ver}) from ${ip}`);
  if (agent.viewers.size) sendJson(ws, { t: 'join', ...agent.params });

  ws.on('pong', () => { ws.isAlive = true; });
  ws.on('message', (data, isBinary) => {
    if (!isBinary) {
      const text = data.toString();
      if (text.startsWith('{"t":"info"') || text.includes('"t":"info"')) agent.lastInfo = text;
      let to = null;
      if (text.startsWith('{"t":"rtc"') || text.includes('"to":')) { try { to = JSON.parse(text).to; } catch { return; } }
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
    if (data[0] === 2 && data.length >= 5) { // file data for one viewer
      const vid = data.readUInt32BE(1);
      for (const v of agent.viewers) if (v.vid === vid && v.ws.readyState === v.ws.OPEN) v.ws.send(data, { binary: true });
      return;
    }
    for (const v of agent.viewers) if (v.ws.readyState === v.ws.OPEN) v.ws.send(data, { binary: true });
  });
  ws.on('close', (code) => {
    if (agents.get(key) !== agent) return; // replaced
    agents.delete(key);
    console.log(`agent down: ${key} (${code})`);
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

function onViewer(ws, url, ip, tenant) {
  const id = String(url.searchParams.get('id') || '');
  const key = agentKey(tenant.id, id);
  const agent = agents.get(key);
  if (!agent) return ws.close(4404, 'agent offline');
  const num = (k, d, lo, hi) => Math.min(hi, Math.max(lo, Number(url.searchParams.get(k)) || d));
  const viewer = { ws, vid: ++viewerSeq, dropping: false, dropped: 0, p2p: false };
  const params = { fps: num('fps', 30, 5, 60), maxw: num('maxw', 1920, 640, 3840), kbps: num('kbps', 3000, 300, 20000) };
  const first = agent.viewers.size === 0;
  agent.viewers.add(viewer);
  ws.isAlive = true;
  console.log(`viewer join: ${key} from ${ip} (${agent.viewers.size} watching)`);
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
    const a = agents.get(key);
    if (!a || !a.viewers.has(viewer)) return;
    if (!isBinary) {
      const text = data.toString();
      if (text === '{"t":"kf"}' && a.gop.length) { replayGop(a, viewer); return; } // decoder reset: serve from cache
      if (text.startsWith('{"t":"rtc"') || text.startsWith('{"t":"f')) {
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
        console.log(`viewer ${key}#${viewer.vid}: ${on ? 'P2P' : 'back on relay'}`);
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
    if (isBinary && data[0] === 2) { // file data: tell the agent whose it is
      if (data.length < 5) return;
      const vid = Buffer.alloc(4);
      vid.writeUInt32BE(viewer.vid);
      data = Buffer.concat([data.subarray(0, 1), vid, data.subarray(1)]);
    }
    if (a.ws.readyState === a.ws.OPEN) a.ws.send(data, { binary: isBinary });
  });
  ws.on('close', () => {
    const a = agents.get(key);
    if (!a || !a.viewers.delete(viewer)) return;
    console.log(`viewer leave: ${key} (dropped ${viewer.dropped} frames, ${a.viewers.size} still watching)`);
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
