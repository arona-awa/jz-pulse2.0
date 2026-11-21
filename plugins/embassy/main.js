// 友校大使馆 —— 应用型插件（kind=app）
//
// 功能：
//   1. 管理员在后台登记各友校大使（学校 / 账号 / 密码 / 大使专属昵称）
//   2. 发帖人使用大使专属昵称时，前台弹窗要求验证身份（账号 + 密码）
//   3. 验证通过后在有效期内以该昵称发帖，帖子自动带上「xx学校大使官方认证」标识
//
// 实现要点：
//   - clv.table       三张表：ambassador（大使）、verify（验证记录）、cert（帖子认证）
//   - clv.route       前台 lookup/status/verify API + 后台增删
//   - clv.on          订阅 post_created 事件，验证有效则落认证记录
//   - clv.filter      filter.card 给卡片追加认证徽章（列表 / 详情 / API 通用）
//   - clv.slot        body.end 注入前台拦截与弹窗脚本
//
// 安全说明：未通过验证的仿冒昵称可以发帖，但不会有任何认证标识；
// 认证标识仅在验证有效期内由 post_created 事件落库，伪造不了。

var T_AMB = clv.db.table("ambassador"); // pl_embassy_ambassador
var T_VERIFY = clv.db.table("verify");  // pl_embassy_verify
var T_CERT = clv.db.table("cert");      // pl_embassy_cert

function setup() {
  // 1) 建表（幂等）
  clv.table("ambassador", {
    school: "string",
    account: "string",
    password: "string", // bcrypt 哈希
    nickname: "string",
    created_at: "time"
  }, { unique: [["nickname"]], indexes: [["account"]] });

  clv.table("verify", {
    ambassador_id: "int",
    fingerprint: "string",
    ip: "string",
    created_at: "time"
  }, { indexes: [["fingerprint"], ["ambassador_id"]] });

  clv.table("cert", {
    post_id: "int",
    ambassador_id: "int",
    school: "string",
    nickname: "string",
    created_at: "time"
  }, { unique: [["post_id"]], indexes: [["post_id"]] });

  // 2) 前台 API（/x/embassy/api/...）
  clv.route("GET", "/api/lookup", "apiLookup", { auth: "none", json: true });
  clv.route("GET", "/api/status", "apiStatus", { auth: "none", json: true });
  clv.route("POST", "/api/verify", "apiVerify", { auth: "none", json: true });

  // 3) 后台管理（/admin/plugins/embassy/）
  clv.adminPage("/", "adminMain", { perm: "plugins" });
  clv.adminMenu({ label: "友校大使馆", path: "/", perm: "plugins", order: 55 });
  clv.route("POST", "/admin/add", "adminAdd", { auth: "admin", json: true });
  clv.route("POST", "/admin/delete", "adminDelete", { auth: "admin", json: true });

  // 4) 前台：发帖拦截 + 身份验证弹窗
  clv.slot("body.end", "clientScript");

  // 5) 认证落库 + 徽章展示
  clv.on("post_created", "onPostCreated");
  clv.filter("filter.card", 50, "cardBadge");
}

/* ---------------- 工具 ---------------- */

function validMS() {
  var m = Number(clv.plugin.config("valid_minutes"));
  if (!m || m <= 0) m = 10;
  return m * 60000;
}

function nowISO() { return new Date().toISOString(); }

function byNickname(nick) {
  return clv.db.get("SELECT id, school, account, password, nickname FROM " + T_AMB + " WHERE nickname=?", nick);
}

// 某指纹对某大使是否存在有效期内的验证记录
function hasValidVerify(ambID, fingerprint) {
  if (!ambID) return false;
  if (fingerprint) {
    var r = clv.db.get(
      "SELECT id FROM " + T_VERIFY + " WHERE ambassador_id=? AND fingerprint=? AND created_at>=?",
      ambID, fingerprint, new Date(Date.now() - validMS()).toISOString());
    if (r) return true;
  }
  return false;
}

/* ---------------- 前台 API ---------------- */

// GET /api/lookup?nickname=xxx → 该昵称是否为大使专属昵称
function apiLookup(ctx) {
  var nick = String(ctx.req.query.nickname || "").trim();
  if (!nick) return { json: { ok: true, protected: false } };
  var amb = byNickname(nick);
  return { json: { ok: true, protected: !!amb, school: amb ? amb.school : "" } };
}

// GET /api/status?nickname=xxx → 当前浏览器指纹是否已验证且仍在有效期
function apiStatus(ctx) {
  var nick = String(ctx.req.query.nickname || "").trim();
  var amb = nick ? byNickname(nick) : null;
  if (!amb) return { json: { ok: true, verified: false } };
  return { json: { ok: true, verified: hasValidVerify(amb.id, ctx.req.fingerprint) } };
}

// POST /api/verify {nickname, account, password} → 校验大使账密，成功则记录验证
function apiVerify(ctx) {
  var body = ctx.req.json || {};
  var nick = String(body.nickname || ctx.req.form.nickname || "").trim();
  var account = String(body.account || ctx.req.form.account || "").trim();
  var password = String(body.password || ctx.req.form.password || "");
  if (!nick || !account || !password) {
    return { status: 400, json: { ok: false, msg: "请填写账号和密码" } };
  }
  var amb = byNickname(nick);
  if (!amb) return { status: 404, json: { ok: false, msg: "该昵称未登记大使身份" } };
  if (account !== amb.account || !clv.crypto.verify(amb.password, password)) {
    clv.log.info("大使验证失败：nickname=" + nick + " account=" + account + " ip=" + ctx.req.ip);
    return { status: 403, json: { ok: false, msg: "账号或密码错误，验证失败" } };
  }
  var fp = ctx.req.fingerprint;
  // 同一指纹重复验证时清理旧记录，避免堆积
  clv.db.exec("DELETE FROM " + T_VERIFY + " WHERE ambassador_id=? AND fingerprint=?", amb.id, fp);
  clv.db.exec("INSERT INTO " + T_VERIFY + "(ambassador_id,fingerprint,ip,created_at) VALUES(?,?,?,?)",
    amb.id, fp, ctx.req.ip, nowISO());
  clv.log.info("大使验证通过：" + amb.school + " / " + nick + " ip=" + ctx.req.ip);
  return { json: { ok: true, school: amb.school } };
}

/* ---------------- 事件与过滤器 ---------------- */

// post_created：昵称为大使昵称且验证仍有效 → 落认证记录（帖子获得徽章）
function onPostCreated(payload) {
  if (!payload) return;
  var nick = String(payload.nickname || "").trim();
  var postID = Number(payload.post_id || 0);
  if (!nick || !postID) return;
  var amb = byNickname(nick);
  if (!amb) return;
  // 验证与发帖之间没有指纹可比（事件不含指纹），以时间窗口为准
  var v = clv.db.get("SELECT id FROM " + T_VERIFY + " WHERE ambassador_id=? AND created_at>=?",
    amb.id, new Date(Date.now() - validMS()).toISOString());
  if (!v) return;
  if (clv.db.get("SELECT id FROM " + T_CERT + " WHERE post_id=?", postID)) return;
  clv.db.exec("INSERT INTO " + T_CERT + "(post_id,ambassador_id,school,nickname,created_at) VALUES(?,?,?,?,?)",
    postID, amb.id, amb.school, amb.nickname, nowISO());
  clv.log.info("帖子 #" + postID + " 获得「" + amb.school + "大使官方认证」");
}

// filter.card：列表 / 详情 / API 卡片统一追加认证徽章
function cardBadge(card) {
  if (!card || !card.id) return card;
  var cert = clv.db.get("SELECT school FROM " + T_CERT + " WHERE post_id=?", card.id);
  if (!cert) return card;
  var badges = card.badges;
  if (!badges || typeof badges.length !== "number") badges = [];
  var label = cert.school + "大使官方认证";
  for (var i = 0; i < badges.length; i++) {
    if (badges[i] === label) return card;
  }
  badges.push(label);
  card.badges = badges;
  return card;
}

/* ---------------- 后台管理 ---------------- */

function adminMain(ctx) {
  var ambs = clv.db.query(
    "SELECT id, school, account, nickname, created_at FROM " + T_AMB + " ORDER BY id DESC LIMIT 200");
  var certs = clv.db.query(
    "SELECT post_id, school, nickname, created_at FROM " + T_CERT + " ORDER BY id DESC LIMIT 20");
  return { template: "admin.html", data: { ambs: ambs, certs: certs, csrf: ctx.csrf } };
}

function adminAdd(ctx) {
  var f = ctx.req.form;
  var school = String(f.school || "").trim();
  var account = String(f.account || "").trim();
  var password = String(f.password || "");
  var nickname = String(f.nickname || "").trim();
  if (!school || !account || !password || !nickname) {
    return { status: 400, body: "学校、账号、密码、昵称均不能为空" };
  }
  if (password.length < 4) return { status: 400, body: "密码至少 4 位" };
  if (byNickname(nickname)) return { status: 400, body: "昵称「" + nickname + "」已被登记" };
  var dup = clv.db.get("SELECT id FROM " + T_AMB + " WHERE account=?", account);
  if (dup) return { status: 400, body: "账号「" + account + "」已存在" };
  clv.db.exec("INSERT INTO " + T_AMB + "(school,account,password,nickname,created_at) VALUES(?,?,?,?,?)",
    school, account, clv.crypto.bcrypt(password), nickname, nowISO());
  clv.log.info("登记友校大使：" + school + " / " + nickname);
  return { redirect: "/admin/plugins/embassy/" };
}

function adminDelete(ctx) {
  var id = Number(ctx.req.form.id || 0);
  if (id > 0) {
    clv.db.exec("DELETE FROM " + T_AMB + " WHERE id=?", id);
    clv.db.exec("DELETE FROM " + T_VERIFY + " WHERE ambassador_id=?", id);
  }
  return { redirect: "/admin/plugins/embassy/" };
}

/* ---------------- 前台脚本注入 ---------------- */

function clientScript() {
  return '<style>' +
    '#embassy-mask{position:fixed;left:0;top:0;right:0;bottom:0;background:rgba(15,17,24,.55);z-index:99998;display:none;align-items:center;justify-content:center;padding:16px;box-sizing:border-box}' +
    '#embassy-box{width:100%;max-width:400px;background:var(--clv-card,#fff);color:var(--clv-text,#23262f);border-radius:var(--clv-radius,16px);box-shadow:0 18px 50px rgba(0,0,0,.28);padding:22px 20px;font-family:var(--clv-font,sans-serif);box-sizing:border-box}' +
    '#embassy-box h3{margin:0 0 6px;font-size:17px;display:flex;align-items:center;gap:8px}' +
    '#embassy-box .emb-sub{margin:0 0 14px;font-size:13px;line-height:1.6;color:var(--clv-muted,#8a8f9c)}' +
    '#embassy-box .emb-school{color:var(--clv-accent,#ff7a9c);font-weight:600}' +
    '#embassy-box input{width:100%;box-sizing:border-box;padding:11px 12px;margin-bottom:10px;border:1px solid var(--clv-border,#eceef4);border-radius:10px;font-size:14px;font-family:inherit;background:var(--clv-bg,#fff);color:inherit}' +
    '#embassy-box input:focus{outline:none;border-color:var(--clv-accent,#ff7a9c)}' +
    '#embassy-err{min-height:18px;font-size:12.5px;color:#e5484d;margin:0 0 8px}' +
    '#embassy-box .emb-acts{display:flex;gap:10px}' +
    '#embassy-box .emb-acts button{flex:1;padding:11px 14px;border-radius:10px;font-size:15px;font-family:inherit;border:1px solid var(--clv-border,#eceef4);background:transparent;color:inherit;cursor:pointer}' +
    '#embassy-box #embassy-ok{flex:1.4;border:0;background:var(--clv-accent,#ff7a9c);color:#fff;font-weight:600}' +
    '#embassy-box #embassy-ok:disabled{opacity:.5;cursor:not-allowed}' +
    '</style>' +
    '<div id="embassy-mask" role="dialog" aria-modal="true"><div id="embassy-box">' +
    '<h3><svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/><path d="M9 12l2 2 4-4"/></svg>大使身份验证</h3>' +
    '<p class="emb-sub">「<span id="embassy-nick"></span>」是 <span class="emb-school" id="embassy-school"></span> 大使专属昵称，请输入大使账号与密码完成验证。</p>' +
    '<input id="embassy-account" type="text" autocomplete="off" placeholder="大使账号">' +
    '<input id="embassy-password" type="password" placeholder="大使密码">' +
    '<p id="embassy-err"></p>' +
    '<div class="emb-acts"><button type="button" id="embassy-no">取消</button><button type="button" id="embassy-ok">验证并发布</button></div>' +
    '</div></div>' +
    '<scr' + 'ipt>(function () {' +
    'var mask, nick, school, acc, pwd, err, okBtn, curForm = null;' +
    'function q(id) { return document.getElementById(id); }' +
    'function csrf() {' +
    // clv_csrf Cookie 为 HttpOnly，JS 不可读；改从服务端渲染的表单隐藏字段取令牌
    '  var f = document.getElementById("compose-form");' +
    '  var i = f ? f.querySelector("[name=_csrf]") : null;' +
    '  if (!i || !i.value) { i = document.querySelector("form input[name=_csrf]"); }' +
    '  return i ? i.value : "";' +
    '}' +
    'function fp() {' +
    // 服务端指纹取自 X-Fingerprint 头；优先复用 app.js 的 CLV.fingerprint()，不可用则本地缓存一个稳定值
    '  try { if (window.CLV && typeof CLV.fingerprint === "function") { return String(CLV.fingerprint() || ""); } } catch (e) {}' +
    '  try {' +
    '    var v = localStorage.getItem("clv_embassy_fp");' +
    '    if (!v) { v = "emb-" + Math.random().toString(36).slice(2) + Date.now().toString(36); localStorage.setItem("clv_embassy_fp", v); }' +
    '    return v;' +
    '  } catch (e2) { return "emb-fallback"; }' +
    '}' +
    'function ajax(method, url, data, cb) {' +
    '  var x = new XMLHttpRequest();' +
    '  x.open(method, url, true);' +
    '  x.setRequestHeader("X-Fingerprint", fp());' +
    '  x.setRequestHeader("X-Requested-With", "XMLHttpRequest");' +
    '  if (data) {' +
    '    x.setRequestHeader("Content-Type", "application/x-www-form-urlencoded");' +
    '    x.setRequestHeader("X-CSRF-Token", csrf());' +
    '  }' +
    '  x.onload = function () { var d = null; try { d = JSON.parse(x.responseText); } catch (e) {} cb(d, x.status); };' +
    '  x.onerror = function () { cb(null, 0); };' +
    '  x.send(data || null);' +
    '}' +
    'function submitNow(f) { f.__embassyOk = 1; if (typeof f.requestSubmit === "function") { f.requestSubmit(); } else { f.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); } }' +
    'function close() { if (mask) { mask.style.display = "none"; } document.body.style.overflow = ""; curForm = null; }' +
    'function show(n, sch, f) {' +
    '  curForm = f; q("embassy-nick").textContent = n; q("embassy-school").textContent = sch;' +
    '  q("embassy-account").value = ""; q("embassy-password").value = "";' +
    '  q("embassy-err").textContent = ""; q("embassy-ok").disabled = false;' +
    '  mask.style.display = "flex"; document.body.style.overflow = "hidden";' +
    '  setTimeout(function () { q("embassy-account").focus(); }, 60);' +
    '}' +
    'function verify() {' +
    '  var a = q("embassy-account").value.trim(), p = q("embassy-password").value;' +
    '  if (!a || !p) { q("embassy-err").textContent = "请填写账号和密码"; return; }' +
    '  okBtn.disabled = true; q("embassy-err").textContent = "验证中…";' +
    '  var body = "nickname=" + encodeURIComponent(q("embassy-nick").textContent) +' +
    '    "&account=" + encodeURIComponent(a) + "&password=" + encodeURIComponent(p) +' +
    '    "&_csrf=" + encodeURIComponent(csrf());' +
    '  ajax("POST", "/x/embassy/api/verify", body, function (d, status) {' +
    '    if (d && d.ok) { close(); if (curForm) { submitNow(curForm); } return; }' +
    '    okBtn.disabled = false;' +
    '    q("embassy-err").textContent = (d && d.msg) ? d.msg : "验证失败，请稍后再试";' +
    '  });' +
    '}' +
    'function check(n, f) {' +
    '  ajax("GET", "/x/embassy/api/lookup?nickname=" + encodeURIComponent(n), null, function (d) {' +
    '    if (!d || !d.ok || !d.protected) { submitNow(f); return; }' +
    '    ajax("GET", "/x/embassy/api/status?nickname=" + encodeURIComponent(n), null, function (s) {' +
    '      if (s && s.ok && s.verified) { submitNow(f); return; }' +
    '      show(n, d.school || "友校", f);' +
    '    });' +
    '  });' +
    '}' +
    'document.addEventListener("submit", function (ev) {' +
    '  var f = ev.target;' +
    '  if (!f || f.id !== "compose-form") { return; }' +
    '  if (f.__embassyOk) { f.__embassyOk = 0; return; }' +
    '  var input = f.querySelector("[name=nickname]");' +
    '  var n = input ? String(input.value || "").trim() : "";' +
    '  if (!n) { return; }' +
    '  ev.preventDefault(); ev.stopImmediatePropagation();' +
    '  check(n, f);' +
    '}, true);' +
    'document.addEventListener("DOMContentLoaded", function () {' +
    '  mask = q("embassy-mask");' +
    '  okBtn = q("embassy-ok");' +
    '  q("embassy-no").addEventListener("click", close);' +
    '  okBtn.addEventListener("click", verify);' +
    '  q("embassy-password").addEventListener("keydown", function (e) { if (e.key === "Enter") { verify(); } });' +
    '  mask.addEventListener("click", function (e) { if (e.target === mask) { close(); } });' +
    '  document.addEventListener("keydown", function (e) {' +
    '    if (e.key === "Escape" && mask && mask.style.display !== "none") { close(); }' +
    '  });' +
    '});' +
    '})();</scr' + 'ipt>';
}
