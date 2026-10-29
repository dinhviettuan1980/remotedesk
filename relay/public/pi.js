'use strict';
// Raspberry Pi page (#/pi/<id>): overview, systemd services, attached hardware
// and a terminal, over the same /ws/view socket as a screen session. The Pi
// agent (pi-agent/) answers "pi-*" and "sh-*" messages; the relay tags each
// with our viewer id and routes the replies back to us only.

let P = null; // the open Pi page

const PI_TABS = ['info', 'svc', 'hw', 'term'];

function startPi(id) {
  stopPi();
  show('pi');
  P = { id, ws: null, closed: false, retry: null, infoTimer: null, units: [], term: null, fit: null, ctrl: false, shellOpen: false, agent: null };
  $('pi-name').textContent = id;
  $('pi-state').textContent = ' · đang kết nối…';
  $('pi-update').hidden = true;
  $('pi-svc-list').replaceChildren();
  $('pi-hw-list').replaceChildren();
  $('pi-facts').replaceChildren();
  $('pi-out').hidden = true;
  piTab(prefs.get('pitab', 'info'));
  piConnect();
}

function stopPi() {
  if (!P) return;
  P.closed = true;
  clearTimeout(P.retry);
  clearInterval(P.infoTimer);
  try { P.ws && P.ws.close(); } catch { /* ignore */ }
  if (P.term) P.term.dispose();
  $('pi-xterm').replaceChildren();
  P = null;
}

function piConnect() {
  const p = P;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  // fps/maxw/kbps: a Pi ignores them; the lowest values keep the relay happy.
  const ws = new WebSocket(`${proto}//${location.host}/ws/view?id=${encodeURIComponent(p.id)}&fps=5&maxw=640&kbps=300`);
  p.ws = ws;
  ws.onopen = () => {
    if (P !== p) return;
    $('pi-state').textContent = ' · online';
    $('pi-state').className = 'ok small';
    piRefresh();
    if (p.term) { // the agent closed our old terminal with the old socket
      p.term.write('\r\n\x1b[33m[Đã nối lại — mở terminal mới]\x1b[0m\r\n');
      piOpenShell();
    }
  };
  ws.onmessage = (ev) => {
    if (P !== p || typeof ev.data !== 'string') return;
    let m;
    try { m = JSON.parse(ev.data); } catch { return; }
    piMessage(m);
  };
  ws.onclose = (ev) => {
    if (P !== p || p.closed) return;
    p.shellOpen = false;
    $('pi-state').textContent = ev.code === 4404 ? ' · offline' : ' · mất kết nối, đang thử lại…';
    $('pi-state').className = 'error small';
    if (ev.code === 4401) { location.hash = '#/'; return; }
    p.retry = setTimeout(() => { if (P === p) piConnect(); }, 3000);
  };
}

function piSend(m) {
  if (P && P.ws && P.ws.readyState === WebSocket.OPEN) P.ws.send(JSON.stringify(m));
}

function piMessage(m) {
  switch (m.t) {
    case 'hello':
      P.agent = m.agent;
      $('pi-name').textContent = m.agent.name;
      document.title = `${m.agent.name} — RemoteDesk`;
      $('pi-update').hidden = !m.agent.canUpdate;
      $('pi-update').textContent = `Cập nhật agent ${m.agent.latest || ''}`.trim();
      break;
    case 'pi-info': renderPiInfo(m); break;
    case 'pi-svc': P.units = m.units || []; P.svcLoaded = true; renderPiServices(); break;
    case 'pi-out': showPiOut(m.title, m.out, m.ok); break;
    case 'pi-hw': renderPiHardware(m.sections || []); break;
    case 'sh-out': if (P.term) P.term.write(b64bytes(m.d)); break;
    case 'sh-exit':
      P.shellOpen = false;
      if (P.term) P.term.write(`\r\n\x1b[33m[Terminal đã đóng${m.err ? ': ' + m.err : ''} — bấm ↻ để mở lại]\x1b[0m\r\n`);
      break;
    case 'notice': piNotice(m.msg); break;
  }
}

function piNotice(text) {
  const n = $('pi-notice');
  n.textContent = text;
  n.hidden = false;
  clearTimeout(piNotice.timer);
  piNotice.timer = setTimeout(() => { n.hidden = true; }, 10000);
}

// ---------------------------------------------------------------- tabs

function piTab(tab) {
  if (!PI_TABS.includes(tab)) tab = 'info';
  prefs.set('pitab', tab);
  for (const b of $('pi-tabs').querySelectorAll('button')) b.classList.toggle('on', b.dataset.tab === tab);
  for (const t of PI_TABS) $(`pi-${t}`).hidden = t !== tab;
  if (!P) return;
  P.tab = tab;
  clearInterval(P.infoTimer);
  if (tab === 'info') P.infoTimer = setInterval(() => piSend({ t: 'pi-info' }), 5000);
  if (tab === 'term') piTerminal();
  piRefresh();
}

function piRefresh() {
  if (!P) return;
  if (P.tab === 'info') piSend({ t: 'pi-info' });
  if (P.tab === 'svc') { piSend({ t: 'pi-svc' }); if (!P.svcLoaded) $('pi-svc-list').textContent = 'Đang đọc…'; }
  if (P.tab === 'hw') { piSend({ t: 'pi-hw' }); if (!$('pi-hw-list').children.length) $('pi-hw-list').textContent = 'Đang đọc…'; }
}

for (const b of $('pi-tabs').querySelectorAll('button')) b.onclick = () => piTab(b.dataset.tab);
$('pi-back').onclick = () => { location.hash = '#/'; };
$('pi-reboot').onclick = () => {
  if (!P || !confirm(`Khởi động lại "${$('pi-name').textContent}"? Pi sẽ offline khoảng 1 phút.`)) return;
  piSend({ t: 'reboot' });
};
$('pi-update').onclick = async () => {
  if (!P || !P.agent) return;
  const b = $('pi-update');
  b.disabled = true;
  try {
    const r = await fetch('/api/agents/update', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: P.id }) });
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `HTTP ${r.status}`);
    piNotice('Đang cập nhật agent — Pi sẽ nối lại sau vài giây.');
  } catch (err) {
    piNotice(`Không gửi được lệnh cập nhật: ${err.message}`);
  }
  b.disabled = false;
};

// ---------------------------------------------------------------- overview

const GB = (n) => `${(n / 1e9).toFixed(n >= 1e10 ? 0 : 1)} GB`;
const MB = (n) => (n >= 1e9 ? GB(n) : `${Math.round(n / 1e6)} MB`);

function duration(s) {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return d ? `${d} ngày ${h} giờ` : h ? `${h} giờ ${m} phút` : `${m} phút`;
}

// vcgencmd get_throttled bits: now (low) / since boot (high).
function throttled(v) {
  const n = parseInt(v, 16);
  if (!n) return ['Bình thường', ''];
  const what = [];
  if (n & 0x1) what.push('đang thiếu điện áp');
  if (n & 0x4) what.push('đang bị giảm xung');
  if (n & 0x8) what.push('đang chạm ngưỡng nhiệt');
  if (n & 0x10000 && !(n & 0x1)) what.push('đã từng thiếu điện áp');
  if (n & 0x40000 && !(n & 0x4)) what.push('đã từng bị giảm xung');
  return [`⚠ ${what.join(', ') || v} (${v})`, n & 0x5 ? 'error' : 'warn'];
}

function renderPiInfo(m) {
  const rows = [];
  const add = (k, v, cls) => { if (v != null && v !== '' && v !== false) rows.push([k, v, cls || '']); };
  add('Model', m.model);
  add('Hệ điều hành', m.os);
  add('Kernel', m.kernel && `${m.kernel} · ${m.arch}`);
  add('Hostname', m.host);
  add('Đã chạy', m.uptime != null && duration(m.uptime));
  add('Tải CPU', m.load && `${m.load} (${m.cpus} lõi)`);
  if (m.temp != null) add('Nhiệt độ CPU', `${m.temp.toFixed(1)} °C`, m.temp >= 80 ? 'error' : m.temp >= 70 ? 'warn' : '');
  if (m.memTotal) add('RAM', `còn ${MB(m.memAvail)} / ${MB(m.memTotal)}`, m.memAvail / m.memTotal < 0.1 ? 'warn' : '');
  if (m.swapTotal) add('Swap', `dùng ${MB(m.swapTotal - m.swapFree)} / ${MB(m.swapTotal)}`);
  if (m.diskTotal) add('Thẻ nhớ', `còn ${GB(m.diskFree)} / ${GB(m.diskTotal)}`, m.diskFree / m.diskTotal < 0.1 ? 'warn' : '');
  add('Địa chỉ', (m.ips || []).join('\n'));
  if (m.ssid) add('Wi-Fi', `${m.ssid}${m.rssi != null ? ` · ${m.rssi} dBm` : ''}`, m.rssi != null && m.rssi < -75 ? 'warn' : '');
  if (m.throttled) { const [t, c] = throttled(m.throttled); add('Nguồn', t, c); }
  if (m.ntp != null) add('Đồng bộ giờ', m.ntp ? 'Đã đồng bộ' : '⚠ Chưa đồng bộ (lịch phát có thể lệch)', m.ntp ? '' : 'warn');
  add('Dịch vụ lỗi', m.failed && m.failed.length ? m.failed.join(', ') : 'Không có', m.failed && m.failed.length ? 'error' : '');
  add('Agent', m.agent);
  $('pi-facts').replaceChildren(...rows.flatMap(([k, v, cls]) => {
    const dt = document.createElement('dt');
    dt.textContent = k;
    const dd = document.createElement('dd');
    dd.textContent = v;
    if (cls) dd.className = cls;
    if (k === 'Dịch vụ lỗi' && m.failed && m.failed.length) { dd.classList.add('link'); dd.onclick = () => piTab('svc'); }
    return [dt, dd];
  }));
}

// ---------------------------------------------------------------- services

function renderPiServices() {
  const q = $('pi-svc-filter').value.trim().toLowerCase();
  const all = $('pi-svc-all').checked || !!q;
  const list = P.units.filter((u) => (all || u.watch || u.active === 'failed') && (!q || u.name.toLowerCase().includes(q) || (u.desc || '').toLowerCase().includes(q)));
  $('pi-svc-list').replaceChildren(...list.map((u) => {
    const li = document.createElement('li');
    li.className = 'pi-svc';
    const head = document.createElement('div');
    head.className = 'pi-svc-head';
    const name = document.createElement('b');
    name.textContent = u.name.replace(/\.service$/, '');
    const badge = document.createElement('span');
    badge.className = `badge ${u.active === 'active' ? 'ok' : u.active === 'failed' ? 'bad' : ''}`;
    badge.textContent = u.sub && u.sub !== u.active ? `${u.active} · ${u.sub}` : u.active;
    head.append(name, badge);
    if (u.enabled) {
      const en = document.createElement('span');
      en.className = 'muted small';
      en.textContent = u.enabled === 'enabled' ? 'tự chạy' : u.enabled;
      head.append(en);
    }
    const desc = document.createElement('div');
    desc.className = 'muted small';
    desc.textContent = u.desc || '';
    const acts = document.createElement('div');
    acts.className = 'pi-svc-acts';
    const btn = (label, act, ask) => {
      const b = document.createElement('button');
      b.textContent = label;
      b.onclick = () => {
        if (ask && !confirm(`${ask} ${u.name}?`)) return;
        showPiOut(`${label} ${u.name}…`, 'Đang chạy…', true);
        piSend({ t: 'pi-act', act, unit: u.name });
      };
      acts.append(b);
    };
    const running = u.active === 'active' || u.active === 'activating';
    btn('Restart', 'restart', 'Khởi động lại');
    if (running) btn('Stop', 'stop', 'Dừng'); else btn('Start', 'start');
    btn('Log', 'logs');
    btn('Status', 'status');
    li.append(head, desc, acts);
    return li;
  }));
  if (!list.length) $('pi-svc-list').textContent = P.units.length ? 'Không có dịch vụ nào khớp.' : P.svcLoaded ? 'Không đọc được danh sách dịch vụ (máy không có systemd?).' : 'Đang đọc…';
}

function showPiOut(title, text, ok) {
  $('pi-out').hidden = false;
  $('pi-out-title').textContent = title;
  $('pi-out-title').className = ok ? '' : 'error';
  const pre = $('pi-out-text');
  pre.textContent = text || '(không có output)';
  pre.scrollTop = pre.scrollHeight; // logs: newest at the bottom
}

$('pi-svc-filter').addEventListener('input', () => P && renderPiServices());
$('pi-svc-all').onchange = () => P && renderPiServices();
$('pi-svc-refresh').onclick = () => piSend({ t: 'pi-svc' });
$('pi-out-close').onclick = () => { $('pi-out').hidden = true; };

// ---------------------------------------------------------------- hardware

function renderPiHardware(sections) {
  $('pi-hw-list').replaceChildren(...sections.map((s) => {
    const d = document.createElement('details');
    d.className = 'pi-hw';
    d.open = true;
    const sum = document.createElement('summary');
    sum.textContent = s.title;
    const pre = document.createElement('pre');
    pre.textContent = s.text;
    d.append(sum, pre);
    return d;
  }));
}
$('pi-hw-refresh').onclick = () => { $('pi-hw-list').textContent = 'Đang đọc…'; piSend({ t: 'pi-hw' }); };

// ---------------------------------------------------------------- terminal

function b64bytes(d) {
  const s = atob(d || '');
  const b = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) b[i] = s.charCodeAt(i);
  return b;
}

function b64text(text) {
  const b = new TextEncoder().encode(text);
  let s = '';
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
  return btoa(s);
}

function piTerminal() {
  if (P.term) { requestAnimationFrame(piFit); P.term.focus(); return; }
  if (!window.Terminal) { $('pi-xterm').textContent = 'Không tải được xterm.js.'; return; }
  const term = new Terminal({
    cursorBlink: true,
    fontSize: COMPACT.matches ? 12 : 14,
    fontFamily: 'ui-monospace, Menlo, Consolas, monospace',
    theme: { background: '#000000' },
    scrollback: 5000,
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($('pi-xterm'));
  P.term = term;
  P.fit = fit;
  term.onData((d) => {
    if (P.ctrl && d.length === 1) { // sticky Ctrl from the key row: next key as a control character
      const c = d.toUpperCase().charCodeAt(0);
      if (c >= 64 && c <= 95) d = String.fromCharCode(c - 64);
      piCtrl(false);
    }
    piSend({ t: 'sh-in', d: b64text(d) });
  });
  term.onResize(({ cols, rows }) => piSend({ t: 'sh-size', cols, rows }));
  new ResizeObserver(() => { if (P && P.tab === 'term') piFit(); }).observe($('pi-xterm'));
  requestAnimationFrame(() => { piFit(); piOpenShell(); term.focus(); });
}

function piFit() {
  if (!P || !P.fit) return;
  try { P.fit.fit(); } catch { /* hidden */ }
}

function piOpenShell() {
  if (!P || !P.term) return;
  piFit();
  P.shellOpen = true;
  piSend({ t: 'sh-open', cols: P.term.cols, rows: P.term.rows });
}

function piCtrl(on) {
  if (!P) return;
  P.ctrl = on;
  $('pi-keys').querySelector('[data-ctrl]').classList.toggle('armed', on);
}

// Key row for phones: keys the on-screen keyboard lacks. Buttons don't take
// focus, so the phone keyboard stays up.
$('pi-keys').addEventListener('mousedown', (e) => e.preventDefault());
for (const b of $('pi-keys').querySelectorAll('button')) {
  b.addEventListener('click', () => {
    if (!P || !P.term) return;
    if (b.id === 'pi-term-new') { P.term.reset(); piOpenShell(); P.term.focus(); return; }
    if (b.dataset.ctrl) { piCtrl(!P.ctrl); P.term.focus(); return; }
    piSend({ t: 'sh-in', d: b64text(JSON.parse(`"${b.dataset.seq}"`)) });
    P.term.focus();
  });
}
