# 应用型插件开发指南（B 型）

> 应用型插件是一段跑在内核里的 JavaScript：用 `clv.*` 注册路由、数据表、后台菜单、
> 定时任务、UI 注入点、事件与过滤器。**能力不受钩子白名单限制**，可以承载一个完整功能。
>
> 通用部分（打包、安装、配置项、权限模型、调试）见 [PLUGIN.md](PLUGIN.md)。
> 轻量注入类需求用声明式插件更省事 → [PLUGIN-DECLARATIVE.md](PLUGIN-DECLARATIVE.md)。

---

## 1. 设计原则

| 原则 | 含义 |
|---|---|
| **清单决定"能碰什么"** | 权限（`permissions`）必须写在 `plugin.json` 里，加载脚本**之前**就已确定；脚本无法在运行时申请权限 |
| **脚本决定"要做什么"** | 路由、表、菜单、任务的注册写在 `setup()` 里，可以按配置条件注册、循环注册 |
| **故障不拖垮站点** | 插件异常只影响自身：超时中断、失败熔断、过滤器失败放行原值、事件失败仅记日志 |
| **无并发心智负担** | 每个插件独享一条串行执行队列，插件作者**不需要考虑并发**（没有锁、没有竞态） |

---

## 2. 最小可用插件

```
hello/
├── plugin.json
└── main.js
```

**plugin.json**

```json
{
  "kind": "app",
  "name": "你好世界",
  "slug": "hello",
  "version": "1.0.0",
  "author": "you",
  "description": "最小示例：一个前台页面",
  "requires": "2.1.0",
  "runtime": { "engine": "js", "entry": "main.js" },
  "permissions": []
}
```

**main.js**

```js
function setup() {
  clv.route("GET", "/", "pageHello");
}

function pageHello(ctx) {
  return { body: "<h1>你好，" + (ctx.userId ? "用户 " + ctx.userId : "访客") + "</h1>" };
}
```

安装并启用后，访问 `/x/hello/` 即可看到页面。

---

## 3. 清单字段

```json
{
  "kind": "app",
  "name": "每日签到",
  "slug": "signin",
  "version": "1.0.0",
  "author": "ClearLove",
  "description": "……",
  "requires": "2.1.0",
  "runtime": { "engine": "js", "entry": "main.js", "timeout_ms": 1000 },
  "permissions": ["db", "settings.read", "site.read"],
  "config": [ { "key": "points", "label": "每次签到积分", "type": "number", "default": "5" } ]
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `kind` | ✅ | 固定 `"app"`（缺省或为其它值会被当作声明式插件处理） |
| `name` | ✅ | 插件名（同时作为安装目录名） |
| `slug` | 建议 | URL 与表名标识，仅允许 `[a-z0-9_-]`；不写则由 `name` 归一化（中文名会退化为 `plugin-<hash>`） |
| `version` / `author` / `description` / `requires` | | 展示与提示用 |
| `runtime.engine` | ✅ | 目前仅支持 `"js"` |
| `runtime.entry` | ✅ | 入口脚本相对路径（不允许绝对路径与 `..`） |
| `runtime.timeout_ms` | | 单次脚本调用超时，默认 `1500`，硬上限 `30000` |
| `permissions` | | 能力白名单，见 [PLUGIN.md](PLUGIN.md) 第四节 |
| `config` | | 配置项声明（与 A 型完全一致，后台自动渲染表单） |
| `hooks` | | 可选：声明式钩子，可与脚本混用 |

---

## 4. 生命周期与执行模型

### 4.1 加载流程（启用插件时）

```
读取 plugin.json
   ↓ 校验 engine=js、entry 合法
执行入口脚本（顶层代码）           ← 只做定义，不要有副作用
   ↓
onInstall()                        ← 可选，仅首次安装后调用
   ↓
setup()                            ← 必须定义：注册全部扩展点
   ↓
生成注册表快照（路由生效）
   ↓
启动定时任务
```

- **顶层代码**在脚本载入时执行一次：定义函数、常量、打开数据库表句柄（`var T = clv.db.table("log")`）
- **`setup()` 必须存在**，否则加载失败（后台状态显示「未运行」，`plugin_logs` 有 `load_error`）
- **`onInstall()`** 可选：适合写初始化数据（注意它在 `setup()` 之前执行）
- **`onDisable()`** 可选：插件停用 / 卸载前调用，适合清理
- 重新启用或调用重载时，会**先卸载旧实例再重新加载**，脚本内变量随之重置

### 4.2 执行模型

| 事项 | 说明 |
|---|---|
| 串行队列 | 每个插件的所有回调（路由、事件、任务、slot）在**同一条队列**上排队执行，天然无并发 |
| 超时中断 | 单次调用超过 `timeout_ms` 会被强制中断（死循环也能中断），之后插件仍可继续服务 |
| setup 预算 | `setup()` 单独限时 3~5 秒（初始化只做注册，网络请求应放在路由或任务里） |
| 队列容量 | 64 个待处理调用；队列满时新调用等待，超时即失败 |
| 失败熔断 | 连续失败 20 次 → 自动禁用并卸载插件（后台可看失败计数） |
| 请求体上限 | 插件路由读取请求体上限 1MB |

### 4.3 JavaScript 环境

| 项 | 说明 |
|---|---|
| 引擎 | goja（纯 Go 实现的 ES5.1 + 大部分 ES6 语法） |
| 支持 | `let` / `const`、箭头函数、模板字符串、解构、`class`、`JSON`、正则、`Date` |
| 不支持 | **`async` / `await`、`Promise` 回调链、DOM、`fetch`、`XMLHttpRequest`、Node.js API** |
| 重要约定 | 所有 `clv.*` API 都是**同步**调用，直接拿返回值，不需要回调或 `await` |
| 推荐风格 | 参考 `plugins/signin/main.js`：`var` + `function`，兼容性最好 |

---

## 5. 注册 API（在 `setup()` 中调用）

### `clv.table(name, columns, opts)` → 实际表名

幂等建表（已存在则跳过）。表名自动加前缀：`pl_<slug>_<name>`。

```js
clv.table("log", {
  user_id: "int",
  day: "string",
  points: "int",
  created_at: "time"
}, {
  unique: [["user_id", "day"]],   // 唯一索引（多列）
  indexes: [["day"]]              // 普通索引
});
```

列类型（自动适配 SQLite / MySQL）：

| 声明 | SQLite | MySQL | 建议用途 |
|---|---|---|---|
| `int` | INTEGER | INT | 计数、ID 引用 |
| `bigint` | INTEGER | BIGINT | 大整数 |
| `bool` | INTEGER | TINYINT | 0/1 |
| `number` | REAL | DOUBLE | 小数（如金额、评分） |
| `string` | TEXT | VARCHAR(255) | 短文本 |
| `text` | TEXT | MEDIUMTEXT | 长文本、JSON 字符串 |
| `json` | TEXT | MEDIUMTEXT | 同 `text` |
| `time` | TEXT | VARCHAR(40) | ISO 时间字符串 |

> 主键 `id` 自动创建，无需声明。时间统一存 ISO 字符串（`new Date().toISOString()`），字符串比较即时间顺序。

### `clv.route(method, path, handler, opts?)`

注册前台路由，实际路径 `/x/<slug><path>`。

```js
clv.route("GET",  "/",      "pageSignin", { auth: "user" });
clv.route("POST", "/do",    "doSignin",   { auth: "user" });
clv.route("GET",  "/rank",  "apiRank",    { auth: "none", json: true });
clv.route("GET",  "/books/*", "bookDetail");   // 支持 /* 后缀通配
```

| opts | 说明 |
|---|---|
| `auth` | `none`（默认）/ `user`（未登录跳转 `/login?next=...`）/ `admin`（非管理员 403） |
| `json` | `true` 时错误以 JSON 返回（前端 `fetch` 接口建议开启） |

- 第 3 个参数可写**函数名字符串**或**直接传函数**
- 路径匹配不含参数提取：需要 ID 请用查询串（`/x/signin/rank?day=2026-09-21`），在 `ctx.req.query` 中读取
- 静态资源放插件目录 `assets/` 下，经 `/x/<slug>/assets/<文件>` 访问（只读、防穿越）

### `clv.adminPage(path, handler, opts?)`

注册后台页面，实际路径 `/admin/plugins/<slug><path>`。

```js
clv.adminPage("/", "adminPage", { perm: "plugins" });
```

`perm` 为内核权限组（`posts` / `reports` / `users` / `settings` / `notices` / `plugins` / `api` / `admins`），缺省为 `plugins`。参数中的 `ctx.params.admin === "1"` 表示来自后台。

### `clv.adminMenu({ label, path, perm, order })`

在后台侧边栏注册菜单项（`path` 与 `adminPage` 对应）。

```js
clv.adminMenu({ label: "签到管理", path: "/", perm: "plugins", order: 50 });
```

`order` 升序排列，`perm` 为空则所有管理员可见，`label` 超长会被截断。

### `clv.slot(name, handler)`

在页面注入点输出 HTML。`handler(data)` 返回字符串（返回空串 / `null` 则不输出）。

```js
clv.slot("post.detail.after", "detailBadge");
clv.slot("body.end", "floatingEntry");

function detailBadge(data) {
  var post = data && data.post;
  if (!post) return "";
  return '<div class="tip">帖子 #' + post.id + '</div>';
}
```

可用 Slot 与传入数据见第 9.1 节。

### `clv.on(event, handler)` / `clv.emit(event, payload)`

订阅站点事件（异步执行，参数为载荷对象，自动带 `event` 与 `time` 字段）：

```js
clv.on("post_created", function (e) {
  clv.log.info("新帖 #" + e.post_id);
});
clv.on("like_toggled", function (e) {
  if (e.liked) clv.cache.set("liked:" + e.post_id, 1, 3600);
});
```

广播自定义事件（插件间通信，也可被其它插件订阅）：

```js
clv.emit("signin.done", { user_id: ctx.userId, points: gain });
```

事件名与载荷见第 9.2 节。

### `clv.filter(name, handler)` 或 `clv.filter(name, priority, handler)`

注册过滤器，链式处理内核数据（`priority` 小的先执行，默认 100）：

```js
clv.filter("filter.content", 10, function (content) {
  return content.replace(/加群/g, "***");
});
clv.filter("filter.post.visible", function (visible, extra) {
  return visible; // 返回 false 即隐藏该帖
});
```

返回值约定：返回 `undefined` / `null` 表示"不改"，其余值作为下一环的输入。过滤器抛错只记日志并放行原值。

### `clv.job(name, every, handler)`

注册定时任务（`every` 为时长字符串，最小 `10s`）：

```js
clv.job("daily-clean", "24h", "onDaily");
clv.job("heartbeat", "30s", function () { clv.log.info("tick"); });
```

任务在插件启用时启动、停用时停止；单次执行同样受超时限制。

### `clv.middleware(phase, handler)`

注册 HTTP 中间件，`phase` 为 `"http.before"` 或 `"http.after"`。

```js
// 前置：可短路请求（返回 {abort:true, ...}）
clv.middleware("http.before", function (req) {
  if (clv.cache.get("banned:" + req.ip)) {
    return { abort: true, status: 403, body: "您的访问已被限制" };
  }
});

// 后置：只观察（状态码、耗时）
clv.middleware("http.after", function (info) {
  if (info.status >= 500) clv.log.warn(info.path + " 异常 " + info.status);
});
```

| phase | 入参 | 返回值 |
|---|---|---|
| `http.before` | `{ method, path, query, ip, fingerprint }` | `{ abort:true, status?, headers?, body?, json?, redirect? }` 短路；不返回则继续 |
| `http.after` | `{ method, path, status, ip, fingerprint, duration_ms }` | 忽略 |

> `http.before` 在 CSRF 校验之前执行，且对 `/static/`、`/uploads/` 不生效；`http.after` 只能观察，不能改写响应。

---

## 6. 请求处理器：上下文与返回值

### 6.1 上下文 `ctx`

```js
function myHandler(ctx) {
  ctx.req.method;        // "GET" / "POST" / ...
  ctx.req.path;          // "/x/demo/do"
  ctx.req.query;         // { day: "2026-09-21" }   查询串（取第一个值）
  ctx.req.form;          // { _csrf: "...", ... }   表单字段
  ctx.req.json;          // 请求体（Content-Type: application/json）
  ctx.req.ip;            // 访客 IP
  ctx.req.fingerprint;   // 浏览器指纹
  ctx.params;            // 后台页面含 { admin: "1" }；前台路由为空
  ctx.userId;            // 登录用户 ID（未登录为 0）
  ctx.adminId;           // 登录管理员 ID（未登录为 0）
  ctx.isAdmin;           // 是否管理员
  ctx.csrf;              // 当前请求的 CSRF 令牌（渲染表单用）
}
```

### 6.2 返回值约定

处理器返回一个对象，按以下优先级写出响应（只命中第一个）：

| 返回字段 | 效果 |
|---|---|
| `{ redirect: "/x/demo/" }` | 303 跳转 |
| `{ json: {...}, status: 200 }` | JSON 响应（`json` 可为任意可序列化值） |
| `{ template: "page.html", data: {...} }` | 渲染插件目录下的模板文件，输出 HTML |
| `{ body: "<h1>hi</h1>", status: 200 }` | 原样输出（默认 `text/html`） |
| `{ status: 204 }` | 仅状态码 |
| `{ headers: { "Cache-Control": "no-store" }, ... }` | 设置响应头（可与上述任一组合） |

```js
function doSignin(ctx) {
  if (alreadyDone) return { status: 400, json: { ok: false, msg: "今天已经签到过了" } };
  return { json: { ok: true, msg: "签到成功" } };
}
```

> **POST 请求同样受 CSRF 保护**：插件表单需带 `{{.csrf}}`，JS 请带请求头 `X-CSRF-Token`
> （可从 `ctx.csrf` 渲染到页面，或读 `<meta name="csrf">`，参考 `plugins/ai-polish/polish.html`）。

---

## 7. Host API 参考

所有 API 均为**同步调用**，出错时抛出 JS 异常（可用 `try/catch` 捕获），错误信息会说明缺失的权限。

### 7.1 基础信息

| API | 说明 |
|---|---|
| `clv.version` | 内核版本号字符串 |
| `clv.plugin.name` / `.slug` / `.dir` / `.version` | 插件自身信息 |
| `clv.plugin.config(key)` | 读取配置项（未设置时回落清单 `default`） |
| `clv.plugin.configs()` | 读取全部配置（对象） |
| `clv.log.info(msg)` / `.warn(msg)` / `.error(msg)` | 写运行日志（同时进系统日志与 `plugin_logs` 表） |
| `clv.json.encode(v)` / `clv.json.decode(s)` | JSON 互转（JS 原生 `JSON` 也可用） |

### 7.2 数据层（权限 `db`）

| API | 说明 |
|---|---|
| `clv.db.table(name)` | 逻辑名 → 实际表名（`pl_<slug>_<name>`） |
| `clv.db.dialect()` | 返回 `"sqlite"` 或 `"mysql"`（写兼容 SQL 用） |
| `clv.db.query(sql, ...args)` | 查询，返回对象数组（最多 1000 行，超出报错） |
| `clv.db.get(sql, ...args)` | 查询单行，无结果返回 `null` |
| `clv.db.exec(sql, ...args)` | 执行写操作，返回 `{ lastId, rowsAffected }` |
| `clv.db.tx(fn)` | 事务：`fn(tx)` 内可 `tx.exec / tx.get / tx.query`，抛异常自动回滚 |

```js
var T = clv.db.table("log");          // → pl_signin_log

var row = clv.db.get("SELECT COUNT(1) AS n FROM " + T + " WHERE user_id=?", uid);
clv.db.exec("INSERT INTO " + T + "(user_id,day,points,created_at) VALUES(?,?,?,?)",
  uid, day, 5, new Date().toISOString());
```

**安全边界（内核强制）**：

- 写操作（INSERT/UPDATE/DELETE/ALTER/DROP…）只能落在本插件的 `pl_<slug>_*` 表
- 内核表**可读不可写**（除非声明 `db.admin` 权限）
- 查询结果上限 1000 行 —— 大表请加 `LIMIT` 与索引
- 始终使用 `?` 占位符传参，不要拼接用户输入

### 7.3 站点设置（`settings.read` / `settings.write`）

| API | 权限 | 说明 |
|---|---|---|
| `clv.setting.get(key)` | `settings.read` | 读取站点设置（如 `site_name`） |
| `clv.setting.all()` | `settings.read` | 读取全部设置（对象） |
| `clv.setting.set(key, value)` | `settings.write` | 写入设置 |

常用设置键（值为字符串，开关用 `"1"` / `"0"`；未列出的键读取返回空串）：

| 分类 | 键 | 说明 |
|---|---|---|
| 基础 | `site_name` | 站点名称 |
| 外观 | `theme` / `theme_bg` / `donate_img` | 主题名 / 背景图地址 / 捐赠二维码地址 |
| 注册 | `allow_register` / `require_login_post` | 是否允许注册 / 发帖是否必须登录 |
| 内容 | `community_rules` | 社区守则文本 |
| AI | `ai_enabled` / `ai_base` / `ai_key` / `ai_model` / `ai_precheck` / `ai_autopatrol` | AI 接入与审核开关 |
| SMTP | `smtp_host` / `smtp_port` / `smtp_user` / `smtp_pass` / `smtp_from` | 邮件发送配置 |
| 其它 | `admin_nicknames` / `cloud_api` / `update_url` / `enabled_plugins` | 管理员专享昵称 / 插件商店地址 / 检查更新地址 / 启用中的插件列表 |

> 插件自己的配置也存在这张表里，键为 `plugin.<插件名>.<字段名>`，读取请用 `clv.plugin.config()`。
> 写入站点设置会影响内核行为，请谨慎并优先使用插件自己的配置项。

### 7.4 站点数据（`site.read` / `site.write`）

```js
clv.post.list({ limit: 10, before: 0, topic: "", include_hidden: false });
clv.post.get(id);
clv.post.create({ content: "内容", nickname: "匿名", topic: "话题名" });   // → { id }
clv.post.update(id, { content: "新内容" });
clv.post.remove(id);            // 级联删除评论 / 媒体 / 点赞 / 举报

clv.comment.list(postId);
clv.comment.create({ post_id: id, content: "评论", nickname: "匿名" });
clv.comment.remove(id);

clv.user.get(id);
clv.user.byName("username");
clv.user.current();             // 当前请求的登录用户（无则 null）

clv.stats.overview();           // { users, posts, comments, reports_pending }
clv.stats.today();              // { posts, comments, users }
```

| API | 权限 |
|---|---|
| `clv.post.list` / `clv.post.get` / `clv.comment.list` / `clv.user.*` / `clv.stats.*` | `site.read` |
| `clv.post.create` / `clv.post.update` / `clv.post.remove` / `clv.comment.create` / `clv.comment.remove` | `site.write` |

- 内容统一经 `StripHTML` 清洗，长度限制与前台一致（帖子 3000 字、评论 500 字）
- `clv.post.create` 会触发 `post_created` Webhook（A 型钩子）
- 用户对象只含公开字段（`id / username / avatar / status / created_at`）

### 7.5 视图（`clv.view`）

| API | 说明 |
|---|---|
| `clv.view.template(name, data)` | 渲染插件目录下的模板文件，返回 HTML 字符串（可用于 slot 或路由响应） |
| `clv.view.escape(s)` | HTML 转义（输出用户数据时务必使用） |
| `clv.view.nl2br(s)` | 转义后将换行转成 `<br>` |

```js
clv.slot("body.end", function () {
  return clv.view.template("widget.html", { site: clv.setting.get("site_name") });
});
```

模板文件位于插件目录下（如 `page.html` / `widget.html`），语法为 Go `html/template`，
可用函数：`nl2br` / `datefmt` / `snippet` / `substr0` / `plus1` / `minus1`。

### 7.6 网络（权限 `net`）

```js
var resp = clv.http.request({
  url: "https://api.example.com/data",
  method: "POST",                                  // 默认 GET
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ q: "x" }),
  timeout_ms: 10000,                               // 默认 10000，上限 30000
  max_bytes: 262144                                // 默认 1MB，上限 2MB
});
resp.status;   // 状态码
resp.headers;  // 响应头
resp.body;     // 响应文本
```

- 仅支持 `http` / `https`
- 默认**拒绝内网 / 环回 / 链路本地地址**（SSRF 防护）；确需访问本机或内网服务（如 Ollama）请声明 `net.local` 权限
- 这是**同步阻塞**调用：耗时算在脚本超时内，记得给 `runtime.timeout_ms` 留足时间

### 7.7 缓存（权限 `cache`）

```js
clv.cache.set("key", value, 3600);   // ttl 秒，上限 24 小时；省略则永不过期
clv.cache.get("key");                // 不存在返回 null
clv.cache.del("key");
```

内存级缓存（进程内、重启丢失），按插件隔离命名空间，适合限流计数、去重、临时状态。

### 7.8 加密与随机（无需权限）

| API | 说明 |
|---|---|
| `clv.crypto.bcrypt(pwd)` | 生成密码哈希 |
| `clv.crypto.verify(hash, pwd)` | 校验密码（→ bool） |
| `clv.crypto.hmacSha256(key, msg)` | HMAC-SHA256，返回十六进制 |
| `clv.crypto.randomHex(n)` | 随机十六进制串（n ≤ 64，默认 16 字节） |
| `clv.crypto.uuid()` | UUID v4 |
| `clv.crypto.base64Encode(s)` / `.base64Decode(s)` | Base64 互转 |

> `bcrypt` 是刻意设计的慢操作（百毫秒级），请计入超时预算。

### 7.9 邮件（权限 `mail`）

```js
clv.mail.send("to@example.com", "主题", "正文");   // 需要站点已配置 SMTP
```

### 7.10 媒体（权限 `media.write`）

```js
var url = clv.media.saveImage({ name: "a.png", data: base64str });  // → "/uploads/xxx.webp"
var u2  = clv.media.saveVideo({ name: "a.mp4", data: base64str });
```

图片经内核统一处理（自动转 WebP），单图上限 10MB。

---

## 8. 权限清单

| 权限 | 授予的能力 | 典型场景 |
|---|---|---|
| `db` | `clv.db.*`、`clv.table`（写仅限本插件表） | 存取插件自己的数据 |
| `db.admin` | 放开全库写（含内核表） | 数据修复类工具（谨慎） |
| `settings.read` | `clv.setting.get/all` | 读取站名、AI 配置 |
| `settings.write` | `clv.setting.set` | 修改站点设置 |
| `site.read` | 帖子 / 评论 / 用户 / 统计读取 | 排行榜、报表 |
| `site.write` | 发帖 / 改帖 / 删帖 / 评论 | 定时公告、内容机器人 |
| `net` | `clv.http.request` | 调用外部接口 |
| `net.local` | 允许访问内网 / 本机地址 | 本机 Ollama、内网服务 |
| `cache` | `clv.cache.*` | 限流、去重 |
| `mail` | `clv.mail.send` | 通知邮件 |
| `media.write` | `clv.media.*` | 生成/保存图片视频 |

---

## 9. 扩展点全集

### 9.1 UI Slot

`handler(data)` 返回 HTML 字符串；与 A 型的同名 `html` 钩子输出会拼接（A 型在前）。

| Slot 名 | 注入位置 | 生效页面 |
|---|---|---|
| `header_html` | `<body>` 起始处 | 全站前台 |
| `footer_html` | 页脚（`app.js` 之前） | 全站前台 |
| `head.end` | `</head>` 之前 | 全站前台 |
| `body.end` | `</body>` 之前 | 全站前台 |
| `admin.head.end` | 后台 `</head>` 之前 | 全站后台 |
| `admin.body.end` | 后台 `</body>` 之前 | 全站后台 |
| `admin.sidebar.after` | 后台侧边栏之后 | 全站后台 |
| `admin.settings.after` | 设置页表单之后 | 后台设置页 |
| `admin.dashboard.after` | 仪表盘内容之后 | 后台仪表盘 |
| `post.detail.actions.after` | 详情页操作栏之后（卡片内） | 帖子详情页 |
| `post.detail.after` | 详情页帖子卡片之后（评论区之前） | 帖子详情页 |
| `comments.after` | 评论区之后 | 帖子详情页 |
| `compose.after` | 发帖表单之后 | 发帖页 |
| `profile.after` | 个人主页内容之后 | 个人主页 |

`data` 字段（轻量数据，避免整页开销）：

| 字段 | 说明 |
|---|---|
| `page` | 当前页面模板名，如 `pg_detail`、`pg_index` |
| `path` | 当前 URL 路径 |
| `site` | 站点名称 |
| `topic` / `notice` | 话题名 / 公告（视页面而定） |
| `user` | `{ id, username }`（未登录则不存在） |
| `admin` | `{ id, username, role }`（后台页面） |
| `post` | `{ id, user_id, nickname }`（详情页 / 个人主页） |

### 9.2 事件

`handler(e)`，`e` 自动包含 `event` 与 `time`。事件异步执行，失败只记日志。

| 事件 | 触发时机 | 载荷 |
|---|---|---|
| `post_created` | 发帖成功（表单 / API） | `post_id`、`nickname`、`content` |
| `post_updated` | 帖子编辑保存 | `post_id`、`content` |
| `post_deleted` | 帖子删除 | `post_id` |
| `comment_created` | 评论成功 | `post_id`、`content` |
| `like_toggled` | 点赞 / 取消 | `post_id`、`liked`、`count` |
| `report_created` | 提交举报 | `post_id`、`reason` |
| `user_login` | 用户登录成功 | `user_id`、`username` |
| `user_registered` | 用户注册成功 | `user_id`、`username` |

自定义事件：`clv.emit(name, payload)` 广播，其他插件用 `clv.on(name, fn)` 订阅。

### 9.3 过滤器

`handler(value, extra?)`：返回 `undefined` / `null` 表示不改；抛错则放行原值。

| 过滤器 | 值类型 | 触发点 | extra |
|---|---|---|---|
| `filter.content` | string | 发帖 / 评论内容（A 型正则之后） | — |
| `filter.card` | map | 首页卡片数据（可改写 `nickname` / `content` / `topic` / `images` / `videos` / `likes` / `comment_count` / `badges`，`id` 与 `user_id` 不可改） | — |
| `filter.api.response` | map | 内核 JSON 接口成功响应 | — |
| `filter.compose.guard` | map | 发帖守卫：可改 `require` / `nickname` / `labels` / `message` | — |
| `filter.post.visible` | bool | 帖子是否可见 | `{ post_id }` |
| `filter.auth.login` | bool | 登录是否放行（返回 false 拒绝登录） | `{ user_id, username }` |
| `filter.theme.css` | string | 主题 CSS（可追加全站样式） | — |
| `filter.admin.stats` | map | 后台仪表盘数据 | — |
| `filter.settings.save` | map | 后台保存设置的键值表 | — |

```js
// 首页卡片：给指定用户加标识
clv.filter("filter.card", function (card) {
  if (card.user_id === 1) card.badges = (card.badges || []).concat(["元老"]);
  return card;
});
```

### 9.4 HTTP 中间件

见 5.9 节 `clv.middleware`。

### 9.5 后台菜单与页面

见 5.3 / 5.4 节。菜单渲染在后台侧边栏，按 `order` 排序，无权限的管理员看不到该项。

### 9.6 模板覆盖（最强的前端改造能力）

在插件目录下新建 `templates/`，放入**与内核模板同名**的文件，用 `{{define "..."}}`
重写对应片段。插件模板在启用 / 停用后自动重新解析，**后解析的覆盖先前的**。

```
templates/
└── front.html        # 只写你要覆盖的 define，其余不受影响
```

```html
{{define "pg_detail"}}
<article class="card detail">
  <h1>自定义的详情页布局</h1>
  <div class="detail-content">{{nl2br .Content}}</div>
  {{.detail_after}}
</article>
{{end}}
```

可覆盖的内核片段（define 名）：

| 文件 | 片段 |
|---|---|
| `layout.html` | `layout.html`（前台布局整体） |
| `admin_layout.html` | `admin_layout.html`（后台布局整体） |
| `front.html` | `pg_index`、`pg_detail`、`pg_compose`、`pg_edit`、`pg_login`、`pg_register`、`pg_profile` |
| `admin.html` | `pg_dashboard`、`pg_admin_posts`、`pg_admin_settings`、`pg_admin_reports`、`pg_admin_users`、`pg_admin_admins`、`pg_admin_notices`、`pg_admin_plugins`、`pg_admin_store`、`pg_admin_apikeys`、`pg_admin_migrate`、`pg_admin_about`、`pg_admin_update`、`pg_admin_plugin_logs`、`pg_admin_login` |
| `install.html` | `pg_install` |

可用模板函数：`nl2br`、`datefmt`、`snippet`、`substr0`、`plus1`、`minus1`；
页面数据（`.site`、`.user`、`.csrf` 及各 slot 变量如 `.body_end`）同样可用。

> 覆盖布局或页面会**整体替换**该片段，请以内核模板为蓝本复制修改，避免遗漏必要结构。
> 多个插件覆盖同一片段时，启用顺序决定最终生效者（后解析者胜）。

### 9.7 前端 API（`window.CLV`）

插件注入到页面的脚本（`clv.slot` 输出、模板覆盖、A 型 `html` 钩子）可以复用内核的前端能力：

| API | 说明 |
|---|---|
| `CLV.version` | 内核版本号字符串 |
| `CLV.csrf` | 当前请求的 CSRF 令牌（自己发请求时用） |
| `CLV.api(url, opts)` | `fetch` 封装：自动带 CSRF / 指纹 / XHR 头，返回 Promise（解析为 JSON） |
| `CLV.fingerprint()` | 浏览器指纹字符串 |
| `CLV.el(tag, className, text)` | 创建元素 |
| `CLV.toast(msg)` | 弹出提示 |
| `CLV.hooks.cardHtml` | 数组，`fn(html, post) => string`：返回内容插入首页卡片底部 |
| `CLV.hooks.cardEl` | 数组，`fn(cardEl, post)`：直接操作卡片元素 |
| `CLV.hooks.afterRender` | 数组，`fn(cardEl, post)`：卡片渲染完成后回调 |

```html
<script>
(function () {
  // 给热度高的卡片加一个角标
  CLV.hooks.cardHtml.push(function (html, post) {
    return post.likes > 10 ? '<span class="hot-tag">热门</span>' : '';
  });
  // 调用插件自己的接口
  CLV.api('/x/demo/rank').then(function (res) { console.log(res); });
})();
</script>
```

- `post` 字段与 `GET /api/v1/posts` 的返回一致（`id` / `nickname` / `content` / `topic` / `likes` / `comment_count` / `badges` …）
- 首页卡片由前端 JS 渲染且支持无限滚动，**`CLV.hooks.*` 是介入卡片的唯一途径**（服务端 Slot 只作用于首屏之外的服务端渲染部分）
- `window.CLV` 在页面 `<head>` 中已预初始化，插件脚本放在任意位置都能安全注册
- 所有 hook 回调都被 `try/catch` 包裹，插件脚本报错不会影响页面其它功能

---

## 10. 完整示例：每日签到插件（全部源码内嵌，可直接复制）

下面是官方示例插件「每日签到」的**完整源码**：共 4 个文件，全部复制保存、打包上传即可运行，
**不需要访问任何源码仓库**。它覆盖了 B 型插件的核心能力：数据表、前台页面与接口、
后台管理页与菜单、UI 注入、定时任务、事件广播。

### 10.1 目录结构

```
signin/
├── plugin.json      # 清单：kind=app、权限、配置项
├── main.js          # 脚本入口：setup() 注册全部扩展点
├── page.html        # 前台签到页（完整 HTML 页面）
└── admin.html       # 后台管理页（HTML 片段）
```

### 10.2 plugin.json

```json
{
  "kind": "app",
  "name": "每日签到",
  "slug": "signin",
  "version": "1.0.0",
  "author": "ClearLove",
  "description": "应用型插件示例：自定义数据表 + 前台页面 + 后台管理页 + 定时任务 + UI 注入",
  "requires": "2.1.0",
  "runtime": {
    "engine": "js",
    "entry": "main.js",
    "timeout_ms": 1000
  },
  "permissions": ["db", "settings.read", "site.read"],
  "config": [
    {
      "key": "points",
      "label": "每次签到积分",
      "type": "number",
      "default": "5",
      "help": "签到一次获得的积分数量"
    }
  ]
}
```

### 10.3 main.js

```js
// 每日签到 —— 应用型插件（kind=app）完整示例
// 能力：clv.table / clv.route / clv.view.template / clv.adminPage
//       clv.adminMenu / clv.slot / clv.job / clv.emit

var T = clv.db.table("log"); // 实际表名 pl_signin_log（顶层只做定义）

function setup() {
  // 1) 建表（幂等，启用时执行）
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
```

要点解读：

- `unique: [["user_id","day"]]` 唯一索引保证同一用户同一天只有一条记录，业务层再判重一次，双保险
- 路由第 3 个参数传**函数名字符串**；`{ auth: "user" }` 未登录自动跳转登录页，`{ json: true }` 让错误以 JSON 返回
- `{ template: "page.html", data: {...} }` 用插件目录下的模板渲染 HTML；POST 表单必须带 `{{.csrf}}`，否则被 CSRF 拦截
- slot 处理器返回 HTML 字符串即注入页面；输出用户数据时用 `clv.view.escape` 转义
- `clv.emit` 广播自定义事件，其它插件可用 `clv.on("signin.done", fn)` 订阅

### 10.4 page.html（前台页面模板）

前台页面是**完整 HTML**：自带页面骨架并引用内核样式（`/static/app.css` 与主题 `/theme.css`），
表单提交到插件自己的路由，带 CSRF 隐藏域。

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>每日签到</title>
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="stylesheet" href="/static/app.css">
<link rel="stylesheet" href="/theme.css">
</head>
<body>
<main class="container">
  <h1 class="page-title">每日签到</h1>
  <div class="card">
    <p>累计签到 <strong>{{.days}}</strong> 天 · 累计积分 <strong>{{.points}}</strong></p>
    {{if .done}}
    <p class="muted">今天已经签到过了，明天再来～</p>
    {{else}}
    <form method="post" action="/x/signin/do">
      <input type="hidden" name="_csrf" value="{{.csrf}}">
      <button class="btn btn-primary" type="submit">立即签到（+{{.gain}}）</button>
    </form>
    {{end}}
    <p style="margin-top:14px">
      <a href="/x/signin/rank">查看签到榜</a> ·
      <a href="/">返回首页</a>
    </p>
  </div>
</main>
</body>
</html>
```

### 10.5 admin.html（后台管理页模板）

后台页面只是 **HTML 片段**（由内核嵌进后台布局，不要写 `<html>` 骨架），
通过 `{{range}}` / `{{else}}` 渲染 `data` 传来的查询结果：

```html
<h1 class="page-title">签到管理</h1>

<div class="stat-grid">
  <div class="stat card"><span class="stat-num">{{.total.n}}</span><span class="stat-label">累计签到次数</span></div>
  <div class="stat card"><span class="stat-num">{{.total.p}}</span><span class="stat-label">累计发放积分</span></div>
</div>

<div class="card table-card">
  <h3>最近 30 天</h3>
  <div class="table-scroll">
    <table class="table">
      <thead><tr><th>日期</th><th>签到人数</th></tr></thead>
      <tbody>
      {{range .days}}
        <tr><td>{{.day}}</td><td>{{.n}}</td></tr>
      {{else}}
        <tr><td colspan="2" class="empty">暂无数据</td></tr>
      {{end}}
      </tbody>
    </table>
  </div>
</div>
```

### 10.6 打包、安装、验证

打包（三平台命令详见 [PLUGIN.md](PLUGIN.md) 2.0 节）：

```bash
zip -r ../plugin-signin.zip .          # macOS / Linux
tar -a -c -f ../plugin-signin.zip *    # Windows 10/11 自带命令
```

安装验证清单：

1. 后台「插件管理 → 上传并安装」选择 zip → 点「启用」→ 状态变为「运行中」
2. 登录用户访问 `/x/signin/` → 签到页；签到后再次访问显示"已签到"
3. 访问 `/x/signin/rank` → JSON 排行榜
4. 后台侧边栏出现「签到管理」→ 可看到累计统计与近 30 天数据
5. 任意帖子详情页 → 若作者签过到，评论区上方出现"已累计签到 N 天"
6. 「插件设置」把"每次签到积分"改成 1 → 保存后再次签到，提示 `积分 +1`
7. 排错查后台「插件日志」页（`load_error` / `job:daily-clean` 等记录），见第 11 节

---

## 11. 调试与 FAQ

| 现象 | 原因与处理 |
|---|---|
| 后台状态「未运行」 | 看 `plugin_logs` 的 `load_error`：常见为 `setup()` 抛错、`runtime.entry` 文件缺失、JS 语法错误 |
| 路由 404 | 确认前缀 `/x/<slug>/`、方法一致、插件处于「运行中」；带 `/*` 的通配只匹配后缀 |
| 报"未声明权限 db" | 权限在启用时确定，补 `permissions` 后需**重新启用** |
| 请求报 CSRF 校验失败 | 表单加 `{{.csrf}}`，JS 加 `X-CSRF-Token` 请求头 |
| 脚本超时 | 默认 1500ms。慢操作（外部 API、bcrypt）请显式设置 `runtime.timeout_ms`（上限 30000） |
| 改了 main.js 不生效 | 脚本驻留内存，需重新启用插件（开发时可反复点停用/启用） |
| 定时任务没跑 | 周期最小 `10s`；插件需在「运行中」；执行记录在 `plugin_logs`（`job:<name>`） |
| 插件自动被停用 | 触发熔断（连续 20 次失败），查日志修好后再启用 |
| 模板覆盖不生效 | 文件需在 `templates/` 下且用 `{{define "同名片段"}}`；启用/停用插件后自动重载 |
| slot 不输出 | 仅当页面渲染时执行；返回空串不输出；确认 slot 名称拼写与生效页面 |
| 数据库表名写错 | 用 `clv.db.table("log")` 取实际表名，不要硬编码 `pl_xxx_log`（slug 变化时表名会变） |

**开发建议**

1. 先在本地装好站点，把插件目录直接放到 `data/plugins/<name>/`，改完重新启用即可调试
2. 用 `clv.log.info()` 打点，后台「插件日志」页查看
3. 复杂逻辑先写纯函数，再接入路由 / 事件，便于排查
4. 涉及写操作务必用 `?` 参数占位，绝不拼接用户输入

---

## 12. 安全边界（诚实的说明）

应用型插件运行在**进程内沙箱**中，受权限、配额、超时、熔断约束，但它是**可执行代码**——请只安装可信来源的插件。

**做不到的事**：

- 不能读取任意文件、不能执行系统命令、不能访问 OS
- 不能绕过权限访问未声明的能力（`db` 之外的库、`net` 之外的网络）
- 不能访问内网 / 本机（除非显式声明 `net.local`）
- 不能替换内核已有页面的处理流程（模板覆盖可改外观，但改不了业务逻辑与路由）
- 不能修改内核函数或内核 SQL

**部署建议**：

- 生产环境安装插件前，先在测试站点验证
- 关注后台插件页的**失败计数**与 `plugin_logs`
- 出问题时「停用」即可立刻止损（路由与任务随停用一起失效）
