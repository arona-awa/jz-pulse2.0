// 每日签到 —— 应用型插件（kind=app）示例
//
// 演示能力：
//   1. 自定义数据表        clv.table
//   2. 前台路由与页面      clv.route + clv.view.template
//   3. 后台页面与菜单      clv.adminPage + clv.adminMenu
//   4. UI 注入             clv.slot
//   5. 定时任务            clv.job
//   6. 事件广播            clv.emit
//
// 注意：本插件没有 hooks 字段 —— 全部能力通过 setup() 中的运行时注册获得。
//
// 应用型插件可用的其它能力（本示例未使用，供参考）：
//   clv.on("post.created", fn)          订阅站点事件（发帖/评论/点赞/注册/登录…）
//   clv.filter("filter.content", 10, fn) 链式改写内容（另见 filter.card / filter.api.response
//                                        / filter.post.visible / filter.theme.css /
//                                        filter.auth.login / filter.compose.guard /
//                                        filter.admin.stats / filter.settings.save）
//   clv.middleware("http.after", fn)     请求结束后观察状态码与耗时（http.before 可改写或短路）
//   clv.http.request({...})              出网请求（需 net 权限，内网地址被拦截）
//   clv.mail.send(to, subject, body)     发邮件（需 mail 权限）
//   clv.media.saveImage/saveVideo({name,data}) 保存 base64 媒体（需 media.write）
//   clv.crypto.*  clv.cache.*  clv.post.*  clv.comment.*  clv.user.*  clv.stats.*
//   插件目录下放 templates/<内核模板名>.html 可覆盖站点模板片段

var T = clv.db.table("log"); // 实际表名 pl_signin_log

function setup() {
  // 1) 建表（幂等，装/启用时都会执行）
  clv.table("log", {
    user_id: "int",
    day: "string",
    points: "int",
    created_at: "time"
  }, { unique: [["user_id", "day"]], indexes: [["day"]] });

  // 2) 前台路由（实际路径：/x/signin/、/x/signin/do、/x/signin/rank）
  clv.route("GET", "/", "pageSignin", { auth: "user" });
  clv.route("POST", "/do", "doSignin", { auth: "user" });
  clv.route("GET", "/rank", "apiRank", { auth: "none", json: true });

  // 3) 后台页面与菜单（实际路径：/admin/plugins/signin/）
  clv.adminPage("/", "adminPage", { perm: "plugins" });
  clv.adminMenu({ label: "签到管理", path: "/", perm: "plugins", order: 50 });

  // 4) UI 注入：详情页显示作者签到天数 + 前台悬浮签到按钮
  clv.slot("post.detail.after", "detailBadge");
  clv.slot("body.end", "floatingEntry");

  // 5) 定时任务：每天清理 90 天前的记录
  clv.job("daily-clean", "24h", "onDaily");
}

function today() {
  var d = new Date();
  return d.getFullYear() + "-" +
    ("0" + (d.getMonth() + 1)).slice(-2) + "-" +
    ("0" + d.getDate()).slice(-2);
}

/* ---------------- 前台 ---------------- */

function pageSignin(ctx) {
  var day = today();
  var done = clv.db.get("SELECT id FROM " + T + " WHERE user_id=? AND day=?", ctx.userId, day);
  var total = clv.db.get("SELECT IFNULL(SUM(points),0) AS points, COUNT(1) AS days FROM " + T + " WHERE user_id=?", ctx.userId);
  return {
    template: "page.html",
    data: {
      done: !!done,
      points: total ? total.points : 0,
      days: total ? total.days : 0,
      gain: clv.plugin.config("points"),
      csrf: ctx.csrf
    }
  };
}

function doSignin(ctx) {
  var day = today();
  if (clv.db.get("SELECT id FROM " + T + " WHERE user_id=? AND day=?", ctx.userId, day)) {
    return { status: 400, json: { ok: false, msg: "今天已经签到过了" } };
  }
  var gain = Number(clv.plugin.config("points")) || 1;
  clv.db.exec("INSERT INTO " + T + "(user_id,day,points,created_at) VALUES(?,?,?,?)",
    ctx.userId, day, gain, new Date().toISOString());
  clv.emit("signin.done", { user_id: ctx.userId, day: day, points: gain });
  clv.log.info("用户 " + ctx.userId + " 签到成功，积分 +" + gain);
  return { json: { ok: true, msg: "签到成功，积分 +" + gain } };
}

function apiRank(ctx) {
  var rows = clv.db.query(
    "SELECT user_id, SUM(points) AS points, COUNT(1) AS days FROM " + T +
    " GROUP BY user_id ORDER BY points DESC LIMIT 20");
  return { json: { ok: true, rank: rows } };
}

/* ---------------- UI 注入 ---------------- */

// 详情页：显示帖子作者的累计签到天数（服务端渲染，可直接查库）
function detailBadge(data) {
  var post = data && data.post;
  if (!post || !post.user_id) return "";
  var row = clv.db.get("SELECT COUNT(1) AS n FROM " + T + " WHERE user_id=?", post.user_id);
  var n = row ? row.n : 0;
  if (!n) return "";
  return '<div class="card clv-signin-days" style="margin-top:12px;font-size:13px;opacity:.85">' +
    clv.view.escape(post.nickname || "该用户") + " 已累计签到 " + n + " 天</div>";
}

// 前台悬浮入口
function floatingEntry() {
  return '<a class="clv-signin-fab" href="/x/signin/" title="每日签到">' +
    '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2">' +
    '<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/><path d="M9 12l2 2 4-4"/></svg></a>' +
    '<style>.clv-signin-fab{position:fixed;left:18px;bottom:22px;z-index:900;width:46px;height:46px;' +
    'border-radius:50%;display:flex;align-items:center;justify-content:center;color:#fff;' +
    'background:linear-gradient(135deg,var(--clv-accent,#ff7a9c),var(--clv-accent-2,#8a6cff));' +
    'box-shadow:0 8px 22px rgba(0,0,0,.22)}</style>';
}

/* ---------------- 后台 ---------------- */

function adminPage(ctx) {
  var days = clv.db.query("SELECT day, COUNT(1) AS n FROM " + T + " GROUP BY day ORDER BY day DESC LIMIT 30");
  var total = clv.db.get("SELECT COUNT(1) AS n, IFNULL(SUM(points),0) AS p FROM " + T);
  return { template: "admin.html", data: { days: days, total: total } };
}

/* ---------------- 定时任务 ---------------- */

function onDaily(ctx) {
  var before = new Date(Date.now() - 90 * 86400000).toISOString();
  var r = clv.db.exec("DELETE FROM " + T + " WHERE created_at < ?", before);
  clv.log.info("清理 90 天前的签到记录：" + (r ? r.rowsAffected : 0) + " 条");
}
