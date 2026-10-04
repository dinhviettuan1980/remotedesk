'use strict';
// Who may use the relay. Every token is a separate tenant: it sees and controls
// only the machines installed with that same token. The admin token comes from
// .env (REMOTE_TOKEN) and additionally manages the other users on /admin; it
// does not see their machines.
//
// Users live in a JSON file outside the deployed code (rsync never touches it):
//   [{ id, name, token, created, disabled }]
// Tokens are stored in clear because installers are rendered with them; the
// file is written 0600.
//
// The admin can also log in with a password of their own (set on /admin),
// kept next to it as a scrypt hash in admin.json.

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

function safeEqual(a, b) {
  const ha = crypto.createHash('sha256').update(String(a)).digest();
  const hb = crypto.createHash('sha256').update(String(b)).digest();
  return crypto.timingSafeEqual(ha, hb);
}

const SCRYPT = { N: 1 << 15, r: 8, p: 1, maxmem: 64 * 1024 * 1024 };
const newToken = () => crypto.randomBytes(32).toString('base64url');
const newId = () => 'u' + crypto.randomBytes(5).toString('hex');

class Users {
  constructor(file, adminToken) {
    this.file = file;
    this.adminFile = path.join(path.dirname(file), 'admin.json');
    this.admin = { id: 'admin', name: 'Admin', token: adminToken, admin: true, created: 0, disabled: false };
    this.users = [];
    this.load();
  }

  load() {
    try {
      const list = JSON.parse(fs.readFileSync(this.file, 'utf8'));
      this.users = Array.isArray(list) ? list.filter((u) => u && u.id && u.token) : [];
    } catch (e) {
      if (e.code !== 'ENOENT') console.error(`users: cannot read ${this.file}: ${e.message}`);
      this.users = [];
    }
  }

  // ---- admin password (scrypt, N=2^15)

  adminPasswordSet() {
    try { return !!JSON.parse(fs.readFileSync(this.adminFile, 'utf8')).hash; } catch { return false; }
  }

  setAdminPassword(pw) {
    const salt = crypto.randomBytes(16);
    const hash = crypto.scryptSync(pw, salt, 32, SCRYPT);
    fs.mkdirSync(path.dirname(this.adminFile), { recursive: true });
    fs.writeFileSync(this.adminFile, JSON.stringify({ salt: salt.toString('base64'), hash: hash.toString('base64'), set: Date.now() }), { mode: 0o600 });
  }

  checkAdminPassword(pw) {
    let rec;
    try { rec = JSON.parse(fs.readFileSync(this.adminFile, 'utf8')); } catch { return false; }
    if (!rec.hash || !pw) return false;
    const want = Buffer.from(rec.hash, 'base64');
    const got = crypto.scryptSync(String(pw), Buffer.from(rec.salt, 'base64'), want.length, SCRYPT);
    return crypto.timingSafeEqual(want, got);
  }

  save() {
    fs.mkdirSync(path.dirname(this.file), { recursive: true });
    const tmp = `${this.file}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(this.users, null, 2), { mode: 0o600 });
    fs.renameSync(tmp, this.file);
  }

  // Tenant for a token, or null. Disabled users don't authenticate.
  byToken(token) {
    if (!token) return null;
    let found = null;
    // Compare against every candidate so timing doesn't reveal which one matched.
    for (const t of [this.admin, ...this.users]) if (safeEqual(token, t.token) && !found) found = t;
    return found && !found.disabled ? found : null;
  }

  byId(id) {
    if (id === 'admin') return this.admin;
    const u = this.users.find((x) => x.id === id);
    return u && !u.disabled ? u : null;
  }

  list() { return this.users; }

  create(name) {
    const u = { id: newId(), name, token: newToken(), created: Date.now(), disabled: false };
    this.users.push(u);
    this.save();
    return u;
  }

  update(id, fn) {
    const u = this.users.find((x) => x.id === id);
    if (!u) return null;
    fn(u);
    this.save();
    return u;
  }

  rotate(id) { return this.update(id, (u) => { u.token = newToken(); }); }
  setDisabled(id, disabled) { return this.update(id, (u) => { u.disabled = !!disabled; }); }
  rename(id, name) { return this.update(id, (u) => { u.name = name; }); }

  remove(id) {
    const n = this.users.length;
    this.users = this.users.filter((x) => x.id !== id);
    if (this.users.length === n) return false;
    this.save();
    return true;
  }
}

module.exports = { Users, safeEqual };
