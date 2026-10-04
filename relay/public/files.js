'use strict';
// RemoteDesk viewer: file transfer (protocol in agent/files.go). Loaded after
// app.js and shares its globals (S, $, sendNow, setControl…).
//
//   • 📁 panel: browse the remote machine, click a file to download it,
//     "Gửi file" uploads into the remote Downloads folder.
//   • Drag files onto the screen: upload into Downloads.
//   • Paste (⌘V / Ctrl+V) files copied on this machine: upload, put them on
//     the remote clipboard, then send the paste keystroke there.
//   • Files copied on the remote machine: the 📥 button (with a count) and a
//     short prompt offer them; one click downloads them into this machine's
//     Downloads. Nothing comes down on its own — copying there is often just
//     for pasting there. (A web page may not put files on the OS clipboard.)
//
// Everything goes over the relay WebSocket, with at most FILE_WINDOW bytes in
// flight, so the video sharing that socket stays smooth.

const FILE_CHUNK = 64 * 1024;
const FILE_WINDOW = 1024 * 1024;
const FILE_ACK_GAP = 256 * 1024;
const FILES_MIN_AGENT = '0.2.2';

const X = { // per-page transfer state (reset with each session)
  seq: 0,
  jobs: new Map(), // id -> job
  path: '',
  copied: [], // files last copied on the remote machine (📥)
};

function fmtSize(n) {
  if (n == null) return '';
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(2)} GB`;
}

// ".../Downloads/report.pdf": the last folder is what tells where it went.
function shortPath(p) {
  const parts = String(p).split(/[\\/]/).filter(Boolean);
  return parts.length > 2 ? `…/${parts.slice(-2).join('/')}` : p;
}

function verAtLeast(v, min) {
  const a = String(v || '0').split('.').map(Number);
  const b = min.split('.').map(Number);
  for (let i = 0; i < 3; i++) if ((a[i] || 0) !== b[i]) return (a[i] || 0) > b[i];
  return true;
}

// The agent must be new enough, and file actions change the remote machine:
// only in control mode.
function filesReady() {
  if (!S || !S.agent) return false;
  if (!S.control) { nudgeMode(); return false; }
  if (!verAtLeast(S.agent.ver, FILES_MIN_AGENT)) {
    flashNotice(`Máy kia đang chạy agent ${S.agent.ver}; chuyển file cần ${FILES_MIN_AGENT} trở lên (Phím… › Cập nhật agent, hoặc cài lại).`);
    return false;
  }
  return true;
}

function flashNotice(text, ms = 6000) {
  setOverlay(text);
  setTimeout(() => { if ($('overlay').textContent === text) setOverlay(''); }, ms);
}

function filesReset() {
  for (const j of X.jobs.values()) if (j.reject) j.reject(new Error('mất kết nối'));
  X.jobs.clear();
  X.path = '';
  $('xfer-list').replaceChildren();
  $('files-list').replaceChildren();
  $('files-path').textContent = '';
  $('clip-toast').hidden = true;
  $('files-panel').hidden = true;
  setCopied([]);
}

// ---------------------------------------------------------------- jobs + UI

function newJob(kind, name, size) {
  const id = ++X.seq;
  const li = document.createElement('li');
  li.className = 'xfer';
  li.innerHTML = '<div class="xfer-row"><span class="xfer-name"></span><span class="xfer-state muted small"></span></div><progress max="1" value="0"></progress>';
  li.querySelector('.xfer-name').textContent = `${kind === 'up' ? '⬆' : '⬇'} ${name}`;
  $('xfer-list').prepend(li);
  const job = { id, kind, name, size, got: 0, acked: 0, li, chunks: [], waiters: [] };
  X.jobs.set(id, job);
  jobProgress(job, 'Đang chuẩn bị…');
  return job;
}

function jobProgress(job, text) {
  job.li.querySelector('progress').value = job.size ? job.got / job.size : 0;
  job.li.querySelector('.xfer-state').textContent = text != null ? text : `${fmtSize(job.got)} / ${fmtSize(job.size)}`;
}

function jobEnd(job, text, ok) {
  X.jobs.delete(job.id);
  job.li.classList.add(ok ? 'done' : 'failed');
  job.li.querySelector('progress').value = ok ? 1 : job.li.querySelector('progress').value;
  job.li.querySelector('.xfer-state').textContent = text;
}

// ---------------------------------------------------------------- upload

function waitAck(job) {
  return new Promise((resolve) => { job.waiters.push(resolve); });
}

// Uploads one File; resolves with the remote path. clip = into a temp folder for pasting.
async function uploadFile(file, clip, batch) {
  const job = newJob('up', file.name, file.size);
  const done = new Promise((resolve, reject) => { job.resolve = resolve; job.reject = reject; });
  done.catch(() => {}); // an early error is also reported by the send loop
  sendNow({ t: 'fput', id: job.id, name: file.name, size: file.size, clip: !!clip, batch: batch || '' });
  try {
    let off = 0;
    while (off < file.size) {
      while (off - job.acked >= FILE_WINDOW || (S && S.ws && S.ws.bufferedAmount > 4 * FILE_WINDOW)) {
        await Promise.race([waitAck(job), new Promise((r) => setTimeout(r, 200))]);
        if (!X.jobs.has(job.id)) throw new Error(job.error || 'đã huỷ');
      }
      const buf = new Uint8Array(await file.slice(off, off + FILE_CHUNK).arrayBuffer());
      const msg = new Uint8Array(5 + buf.length);
      msg[0] = 2;
      new DataView(msg.buffer).setUint32(1, job.id);
      msg.set(buf, 5);
      if (!S || !S.ws || S.ws.readyState !== WebSocket.OPEN) throw new Error('mất kết nối');
      S.ws.send(msg);
      off += buf.length;
      job.got = off;
      jobProgress(job);
    }
    sendNow({ t: 'fend', id: job.id });
    jobProgress(job, 'Đang lưu…');
    return await done;
  } catch (e) {
    if (X.jobs.has(job.id)) { sendNow({ t: 'fcancel', id: job.id }); jobEnd(job, `Lỗi: ${e.message}`, false); }
    throw e;
  }
}

async function uploadFiles(files) {
  if (!filesReady()) return;
  showFilesPanel();
  for (const f of files) {
    try {
      const path = await uploadFile(f, false);
      flashNotice(`Đã gửi ${f.name} → ${path}`, 4000);
    } catch { /* shown in the list */ }
  }
}

// ---------------------------------------------------------------- download

function downloadPath(path, name, size, quiet) {
  if (!filesReady()) return null;
  if (!quiet) showFilesPanel();
  const job = newJob('down', name || path.split(/[\\/]/).pop(), size || 0);
  job.path = path;
  sendNow({ t: 'fget', id: job.id, path });
  return job;
}

function onFileData(buf) {
  if (buf.byteLength < 9) return;
  const dv = new DataView(buf);
  const job = X.jobs.get(dv.getUint32(5));
  if (!job || job.kind !== 'down') return;
  job.chunks.push(new Uint8Array(buf, 9));
  job.got += buf.byteLength - 9;
  if (job.got - job.acked >= FILE_ACK_GAP || job.got === job.size) {
    job.acked = job.got;
    sendNow({ t: 'fack', id: job.id, got: job.got });
  }
  jobProgress(job);
}

function saveBlob(name, chunks) {
  const url = URL.createObjectURL(new Blob(chunks));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

// ---------------------------------------------------------------- messages from the agent

function onFileMsg(m) {
  const job = X.jobs.get(m.id);
  switch (m.t) {
    case 'fls': renderDir(m); break;
    case 'fmeta':
      if (job) { job.size = m.size; job.name = m.name; jobProgress(job); }
      break;
    case 'fack':
      if (job) { job.acked = Math.max(job.acked, m.got); for (const w of job.waiters.splice(0)) w(); }
      break;
    case 'fdone':
      if (!job) break;
      if (job.kind === 'down') {
        saveBlob(job.name, job.chunks);
        job.chunks = [];
        jobEnd(job, `Đã tải ${fmtSize(job.size)}`, true);
        if (job.fromCopy) clipToast(`Đã tải ${job.name} về thư mục Downloads của máy này.`, null, null, 6000);
      }
      else { jobEnd(job, `Đã lưu: ${shortPath(m.path)}`, true); if (job.resolve) job.resolve(m.path); }
      break;
    case 'ferr':
      if (job) {
        job.error = m.msg;
        jobEnd(job, `Lỗi: ${m.msg}`, false);
        for (const w of job.waiters.splice(0)) w(); // wake an upload loop waiting for acks
        if (job.reject) job.reject(new Error(m.msg));
      }
      else flashNotice(`Lỗi: ${m.msg}`);
      break;
    case 'fclip':
      if (job && job.resolve) { X.jobs.delete(job.id); job.resolve(); }
      break;
    case 'clipfiles': showClipToast(m.files || []); break;
  }
}

// ---------------------------------------------------------------- remote browser

function listDir(path) {
  if (!filesReady()) return;
  sendNow({ t: 'fls', id: ++X.seq, path });
}

function renderDir(m) {
  X.path = m.path;
  $('files-path').textContent = m.path;
  $('files-up').disabled = !m.parent;
  $('files-up').onclick = () => listDir(m.parent);
  $('files-roots').replaceChildren(...(m.roots || []).map((r) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.textContent = r.split(/[\\/]/).filter(Boolean).pop() || r;
    b.title = r;
    b.onclick = () => listDir(r);
    return b;
  }));
  const sep = m.path.includes('\\') ? '\\' : '/';
  const join = (n) => (m.path.endsWith(sep) ? m.path + n : m.path + sep + n);
  $('files-list').replaceChildren(...(m.entries || []).map((e) => {
    const li = document.createElement('li');
    li.className = 'file' + (e.d ? ' dir' : '');
    li.innerHTML = '<span class="file-name"></span><span class="file-size muted small"></span>';
    li.querySelector('.file-name').textContent = `${e.d ? '📁' : '📄'} ${e.n}`;
    li.querySelector('.file-size').textContent = e.d ? '' : fmtSize(e.s);
    li.title = e.d ? 'Mở thư mục' : 'Tải về máy này';
    li.onclick = () => (e.d ? listDir(join(e.n)) : downloadPath(join(e.n), e.n, e.s));
    return li;
  }));
}

// ---------------------------------------------------------------- copied on the remote machine

function clipToast(text, button, onclick, ms = 20_000) {
  const t = $('clip-toast');
  $('clip-toast-text').textContent = text;
  $('clip-toast-get').hidden = !button;
  if (button) { $('clip-toast-get').textContent = button; $('clip-toast-get').onclick = () => { t.hidden = true; onclick(); }; }
  t.hidden = false;
  clearTimeout(t.timer);
  t.timer = setTimeout(() => { t.hidden = true; }, ms);
}

function fetchCopied(files, quiet) {
  for (const f of files) {
    const job = downloadPath(f.path, f.name, f.size, quiet);
    if (job) job.fromCopy = true;
  }
}

// 📥 on the toolbar: what was last copied over there, ready to download.
function setCopied(files) {
  X.copied = files;
  const b = $('btn-getclip');
  b.hidden = files.length === 0;
  b.textContent = files.length > 1 ? `📥 ${files.length}` : '📥';
  const bytes = files.reduce((n, f) => n + (f.size || 0), 0);
  b.title = files.length ? `Tải về máy này: ${files.map((f) => f.name).join(', ')} (${fmtSize(bytes)})` : '';
}

function getCopied() {
  const files = X.copied;
  if (!files.length || !filesReady()) return;
  setCopied([]);
  clipToast(`Đang tải ${files.length > 1 ? `${files.length} file` : files[0].name} về Downloads…`, null, null, 4000);
  fetchCopied(files, true);
}

// Copied on the remote machine: offer it (📥 + a short prompt); nothing
// downloads until asked.
function showClipToast(files) {
  const real = files.filter((f) => !f.dir);
  if (!files.length) return;
  const names = files.slice(0, 3).map((f) => f.name + (f.dir ? ' (thư mục)' : '')).join(', ') + (files.length > 3 ? `, … (${files.length} mục)` : '');
  const bytes = real.reduce((n, f) => n + (f.size || 0), 0);
  setCopied(real);
  clipToast(`Máy kia vừa copy: ${names}`,
    real.length ? `Tải về (${fmtSize(bytes)})` : null, getCopied, 6000);
}
$('clip-toast-close').onclick = () => { $('clip-toast').hidden = true; };

// ---------------------------------------------------------------- paste files

// Files from a paste event (folders are skipped: not supported yet).
function pastedFiles(dt) {
  const out = [];
  let skipped = 0;
  for (const item of dt.items || []) {
    if (item.kind !== 'file') continue;
    const entry = item.webkitGetAsEntry && item.webkitGetAsEntry();
    if (entry && entry.isDirectory) { skipped++; continue; }
    const f = item.getAsFile();
    if (f) out.push(f);
  }
  return { files: out, skipped };
}

// Upload, put on the remote clipboard, then press paste there.
async function pasteFiles(files) {
  if (!filesReady()) return;
  const batch = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const paths = [];
  flashNotice(`Đang gửi ${files.length} file để dán…`, 3000);
  try {
    for (const f of files) paths.push(await uploadFile(f, true, batch));
    const job = { id: ++X.seq, waiters: [] };
    const ok = new Promise((resolve, reject) => { job.resolve = resolve; job.reject = reject; });
    X.jobs.set(job.id, Object.assign(job, { li: document.createElement('li'), kind: 'clip' }));
    sendNow({ t: 'fclip', id: job.id, paths });
    await Promise.race([ok, new Promise((_, rej) => setTimeout(() => rej(new Error('máy kia không phản hồi')), 10_000))]);
    const mod = S.agent && S.agent.os === 'darwin' ? 'MetaLeft' : 'ControlLeft';
    for (const [t, c] of [['kd', mod], ['kd', 'KeyV'], ['ku', 'KeyV'], ['ku', mod]]) send({ t, c });
  } catch (e) {
    flashNotice(`Không dán được file: ${e.message}`);
  }
}

// Called from the keydown handler for ⌘V / Ctrl+V: returns true when this
// paste event carried files (then the keystroke is ours to send later).
function handlePasteEvent(e) {
  const dt = e.clipboardData;
  if (!dt) return false;
  const { files, skipped } = pastedFiles(dt);
  if (skipped && !files.length) { flashNotice('Chưa hỗ trợ dán thư mục — hãy nén (zip) rồi dán.'); return true; }
  if (!files.length) return false;
  pasteFiles(files);
  return true;
}

// ---------------------------------------------------------------- wiring

function showFilesPanel() {
  const p = $('files-panel');
  p.style.top = `${$('toolbar').offsetHeight}px`; // the toolbar wraps on narrow windows
  p.hidden = false;
}

$('btn-getclip').onclick = () => { getCopied(); $('stage').focus(); };

$('btn-files').onclick = () => {
  const p = $('files-panel');
  if (p.hidden) showFilesPanel(); else p.hidden = true;
  if (!p.hidden && !$('files-list').children.length) listDir(X.path);
  $('stage').focus();
};
$('files-close').onclick = () => { $('files-panel').hidden = true; $('stage').focus(); };
$('files-refresh').onclick = () => listDir(X.path);
$('files-pick').onclick = () => { if (filesReady()) $('files-input').click(); };
$('files-input').onchange = (e) => { uploadFiles([...e.target.files]); e.target.value = ''; };

const stageEl = $('stage');
stageEl.addEventListener('dragover', (e) => {
  if (!S || ![...(e.dataTransfer.types || [])].includes('Files')) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = 'copy';
  stageEl.classList.add('drop');
});
stageEl.addEventListener('dragleave', () => stageEl.classList.remove('drop'));
stageEl.addEventListener('drop', (e) => {
  stageEl.classList.remove('drop');
  if (!S || !e.dataTransfer.files.length) return;
  e.preventDefault();
  const { files, skipped } = pastedFiles(e.dataTransfer);
  if (skipped) flashNotice('Bỏ qua thư mục — hãy nén (zip) rồi gửi.');
  if (files.length) uploadFiles(files);
});
