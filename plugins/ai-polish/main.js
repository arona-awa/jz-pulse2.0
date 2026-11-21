// AI 语句美化 —— 应用型插件（kind=app）
//
// 组合了两种能力，兼顾性能与功能：
//   1. 声明式 hook（footer_html）：静态注入按钮脚本，页面渲染零额外开销
//   2. 应用型路由：GET /x/ai-polish/config 探测可用性；POST /x/ai-polish/polish 调用 AI 润色
//
// 复用站点「网站设置 → AI 模型接入」里已配置的接口，无需重复填写密钥。

function setup() {
  clv.route("GET", "/config", "readConfig", { auth: "none", json: true });
  clv.route("POST", "/polish", "polish", { auth: "none", json: true });
}

/* ---------------- 配置与可用性 ---------------- */

function aiReady() {
  if (clv.setting.get("ai_enabled") !== "1") return false;
  var base = clv.setting.get("ai_base");
  var key = clv.setting.get("ai_key");
  var model = clv.setting.get("ai_model");
  return !!(base && key && model);
}

// readConfig 前端加载时探测一次：不可用时直接不显示按钮
function readConfig(ctx) {
  var requireLogin = clv.plugin.config("require_login") === "1";
  var loggedIn = !!ctx.userId;
  var ready = aiReady();
  return {
    json: {
      ok: true,
      available: ready && (!requireLogin || loggedIn),
      label: clv.plugin.config("button_text") || "AI 美化",
      max_chars: intOr(clv.plugin.config("max_chars"), 600),
      reason: ready ? (requireLogin && !loggedIn ? "登录后可使用 AI 美化" : "") : "站点尚未接入 AI 模型"
    }
  };
}

/* ---------------- 核心：调用 AI 润色 ---------------- */

function polish(ctx) {
  if (!aiReady()) {
    return { status: 400, json: { ok: false, msg: "站点尚未开启 AI 模型，请联系管理员在「网站设置 → AI 模型接入」中配置" } };
  }
  if (clv.plugin.config("require_login") === "1" && !ctx.userId) {
    return { status: 401, json: { ok: false, msg: "请先登录后再使用 AI 美化" } };
  }

  var text = String((ctx.req && ctx.req.json && ctx.req.json.text) || "").trim();
  var maxChars = intOr(clv.plugin.config("max_chars"), 600);
  if (text.length < 2) {
    return { status: 400, json: { ok: false, msg: "内容太短，无需美化" } };
  }
  if (text.length > maxChars) {
    return { status: 400, json: { ok: false, msg: "内容超过 " + maxChars + " 字，请分段美化" } };
  }

  // 按访客 IP 限流（每分钟）
  var limit = intOr(clv.plugin.config("rate_limit"), 3);
  var rk = "rate:" + ((ctx.req && ctx.req.ip) || "unknown");
  var used = Number(clv.cache.get(rk)) || 0;
  if (used >= limit) {
    return { status: 429, json: { ok: false, msg: "操作过于频繁，请稍后再试" } };
  }
  clv.cache.set(rk, used + 1, 60);

  var base = String(clv.setting.get("ai_base") || "").replace(/\/+$/, "");
  var key = clv.setting.get("ai_key") || "";
  var model = clv.setting.get("ai_model") || "";
  var temp = parseFloat(clv.plugin.config("temperature"));
  if (isNaN(temp) || temp < 0 || temp > 1) temp = 0.7;
  var prompt = clv.plugin.config("prompt") ||
    "请在不改变原意的前提下润色下面的文字，只输出润色后的正文。";

  var payload = JSON.stringify({
    model: model,
    messages: [
      { role: "system", content: prompt },
      { role: "user", content: text }
    ],
    temperature: temp
  });

  var resp;
  try {
    resp = clv.http.request({
      method: "POST",
      url: base + "/chat/completions",
      headers: {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + key
      },
      body: payload,
      timeout_ms: 25000,
      max_bytes: 262144
    });
  } catch (e) {
    clv.log.warn("AI 请求失败：" + e);
    return { status: 502, json: { ok: false, msg: "AI 服务连接失败，请稍后重试" } };
  }

  if (!resp || resp.status < 200 || resp.status >= 300) {
    var code = resp ? resp.status : "无响应";
    clv.log.warn("AI 返回异常状态：" + code);
    return { status: 502, json: { ok: false, msg: "AI 服务返回异常（" + code + "）" } };
  }

  var data = null;
  try { data = JSON.parse(resp.body); } catch (e) { data = null; }
  var out = "";
  if (data && data.choices && data.choices.length && data.choices[0].message) {
    out = String(data.choices[0].message.content || "").trim();
  }
  // 去掉模型有时会加上的引号包裹
  out = out.replace(/^["'“”‘’\s]+|["'“”‘’\s]+$/g, "").trim();
  if (!out) {
    return { status: 502, json: { ok: false, msg: "AI 未返回有效内容，请稍后重试" } };
  }
  // 防止模型输出失控（例如复述要求、长篇解释）
  var cap = maxChars * 3;
  if (out.length > cap) out = out.slice(0, cap) + "……";

  clv.log.info("AI 美化完成：" + text.length + " 字 → " + out.length + " 字（用户 " + (ctx.userId || "匿名") + "）");
  return { json: { ok: true, text: out } };
}

/* ---------------- 小工具 ---------------- */

function intOr(v, def) {
  var n = parseInt(v, 10);
  return (isNaN(n) || n <= 0) ? def : n;
}
