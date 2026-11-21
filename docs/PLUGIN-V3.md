# ClearLove 插件系统设计与实现说明

> 定位：**内核开发者文档**（架构决策、扩展点接入方式与实现细节）。
> 插件作者请阅读：[PLUGIN.md](PLUGIN.md)（总览）、[PLUGIN-DECLARATIVE.md](PLUGIN-DECLARATIVE.md)（声明式）、
> [PLUGIN-APP.md](PLUGIN-APP.md)（应用型）。
>
> 状态：**P0 + P1 已实现并通过测试**（声明式 + 应用型双形态均已落地）
> 目标：在现有声明式插件之外，**新增一种"应用型插件"形态**，让新功能可以完全通过插件实现，不必改内核源码、不必重新编译整套系统
> 硬约束：**现有声明式插件 100% 向后兼容**；保持 `CGO_ENABLED=0` 单二进制部署
> 实际落地情况与差异见文末「[15. 实现状态](#15-实现状态本仓库当前进度)」

---

## 1. 现状与问题（升级动因）

当前插件系统是"声明式"的：`plugin.json` + 5 类钩子（`html` / `filter` / `http` / `guard` / `badge`），由内核解释执行。它能覆盖"注入一段 HTML / 正则改写 / 回调 Webhook / 昵称守卫"这类简单需求，但**无法承载一个新功能**。

经全仓库排查，具体缺口如下：

| 缺口 | 事实依据 |
|---|---|
| 不能注册页面/路由 | `main.go` 的 `buildMux()` 硬编码全部路由，插件无法挂载任何 URL |
| 不能建表存数据 | 插件配置只能塞进 `settings` 表（键名 `plugin.<name>.<key>`），无法拥有独立数据结构 |
| 不能注册后台菜单 | `admin_layout.html:18-45` 侧边栏为硬编码 HTML，插件只能往"插件管理页"顶部注入一块 |
| 不能定时执行 | 只有内核自带的 AI 巡查定时器（`ai.StartPatrol`），插件无调度能力 |
| 前台 UI 几乎无法介入 | 首页卡片由前端 JS 拼接（`app.js:119-201` `cardEl`），服务端模板只有 3 个有效注入点 |
| 只能"加"不能"拦" | `html` 钩子只能追加内容，无法拦截/改写请求；`filter` 只能正则替换字符串 |
| 后台完全没有注入点 | `admin_layout.html` 不引用 `header_html`/`footer_html`；唯一后台注入点是 `admin_plugins_top` |
| 死钩子与不对称 | 注释里的 `card_extra` 无渲染位置；`content_filter` 在表单发帖 `POST /compose`（`front.go:395`）路径缺失 |
| 前端无扩展接口 | `app.js` 是 IIFE，插件无法复用 `api()`/`cardEl()`/`fingerprint()` |

**结论**：声明式钩子做不成"万物插件化"。需要引入可执行的插件运行时，并且以**独立的第二种插件形态**存在，而不是继续往 `hooks` 里加类型。

---

## 2. 目标与边界

### 2.1 验收标准

以下 5 个功能**全部只用应用型插件实现**（不改一行内核源码、不重新编译），即视为达标：

1. **签到/积分系统**：自定义数据表 + 前台页面 + 前台展示位 + 后台管理菜单 + 每日重置任务
2. **活动抽奖页**：独立路由 + 自有数据 + 后台配置 + 前端交互
3. **第三方登录/外部同步**：拦截请求 + 调用外部 HTTP + 读写站点数据
4. **内容增强**：改写帖子正文、往卡片/详情页追加 UI、往后台设置页追加配置卡片
5. **数据报表**：读取站点统计 + 生成后台图表页

### 2.2 边界（诚实的说明）

- 不能修改内核已有页面的布局与流程（P3 的"模板覆盖"能力可逼近，但仍有边界）
- 不能 patch 内核函数、不能替换内核 SQL（脚本运行在能力白名单内）
- 不允许插件无限制访问 OS/文件系统/内网（安全底线，见第 8 节）
- 不引入 CGO，不破坏"单二进制 + 交叉编译"

---

## 3. 两种插件形态

这是本方案的核心：**两种形态并列存在，互不干扰，各司其职。**

```
                    ┌──────────────────────────────┐
                    │      data/plugins/<name>/     │
                    │        plugin.json            │
                    └──────────────┬───────────────┘
                                   │ 读取 kind 字段判定
                 ┌─────────────────┴─────────────────┐
                 │                                   │
        ┌────────▼─────────┐              ┌──────────▼──────────┐
        │  形态 A 声明式    │              │  形态 B 应用型       │
        │  （现有，保留）   │              │  （新增，本次重点）  │
        ├──────────────────┤              ├─────────────────────┤
        │ 无代码           │              │ main.js 脚本        │
        │ hooks 白名单      │              │ 运行时注册一切       │
        │ 内核解释执行      │              │ 内核调用脚本         │
        │ 5 类固定能力      │              │ 能力不受白名单限制   │
        └──────────────────┘              └─────────────────────┘
                 │                                   │
                 └─────────────────┬─────────────────┘
                                   │
                    ┌──────────────▼───────────────┐
                    │   Extension Registry 注册表   │
                    │  Event / Filter / Middleware  │
                    │  Slot / Route / Job / Table   │
                    └──────────────┬───────────────┘
                                   │
                    ┌──────────────▼───────────────┐
                    │  内核：handlers / templates   │
                    │        middleware / web       │
                    └──────────────────────────────┘
```

### 3.1 形态 A：声明式插件（现有，原样保留）

- 清单：`plugin.json`（**无 `kind` 字段**，即现在的格式）
- 能力：`hooks` 声明的 5 类钩子（html / filter / http / guard / badge）
- 特点：零代码、可审计、最适合市场分发；改配置即生效
- 定位：**轻量增强**（注入脚本、敏感词替换、Webhook 通知、身份守卫）
- 行为：**与今天完全一致**，不做任何改动

### 3.2 形态 B：应用型插件（新增）

- 清单：`plugin.json` + `"kind": "app"` + `runtime.entry`（如 `main.js`）
- 入口：脚本中的 `setup()` 函数，在其中**调用注册 API 注册一切能力**
- 能力：路由、后台页面与菜单、UI 注入、事件订阅、过滤器、请求中间件、定时任务、自定义数据表、HTTP 出网、媒体、邮件、缓存……
- **不使用声明式钩子**：`hooks` 字段可以完全不写；B 型的能力来源是运行时注册，不受 5 类钩子白名单约束
- 定位：**承载完整功能**（签到、抽奖、积分、报表、第三方对接）

### 3.3 对比与选型

| 维度 | A 型 声明式 | B 型 应用型 |
|---|---|---|
| 清单 | `plugin.json` | `plugin.json` + `"kind":"app"` |
| 是否需要代码 | 否 | 是（`main.js`） |
| 能力来源 | `hooks` 白名单 | 运行时注册 API |
| 新增路由/页面 | 不支持 | 支持 |
| 自定义数据表 | 不支持 | 支持 |
| 后台菜单 | 不支持 | 支持 |
| 定时任务 | 不支持 | 支持 |
| 请求拦截/改写 | 不支持 | 支持（中间件） |
| UI 注入点 | 3 个（header/footer/插件页顶部） | 15+ 个 slot，可动态渲染 |
| 分发安全等级 | 最高（无代码） | 需权限声明 + 沙箱 + 审计 |
| 典型场景 | 加一段 JS、敏感词、Webhook、昵称守卫 | 签到、抽奖、积分商城、数据看板 |

**选型建议**：能用 A 型表达的继续用 A 型（更安全、更简单）；一旦需要存数据、开页面、做后台，直接用 B 型，不要在 A 型的 `hooks` 上继续打补丁。

### 3.4 关键设计原则

1. **兼容优先**：A 型的清单格式、目录结构、设置键、公开函数签名、行为全部不变。
2. **职责分离**：A 型能力来自"清单声明"，B 型能力来自"脚本注册"，两套机制不混用。
3. **权限必须声明式**：`permissions` 只能在清单里声明（详见 5.3）。
4. **能力白名单**：B 型插件默认最小权限，安装时向管理员展示。
5. **热路径零开销**：没有插件注册扩展点时，调用链开销为一次长度判断。
6. **故障隔离**：任何插件出错不得影响站点，可熔断、可一键停用。

---

## 4. 清单规范

### 4.1 A 型清单（= 现有 v1，不变）

```json
{
  "name": "bgm",
  "version": "1.0.0",
  "author": "ClearLove",
  "description": "背景音乐",
  "requires": "2.0.0",
  "config": [ { "key": "music", "label": "音乐文件", "type": "audio" } ],
  "hooks": [ { "hook": "footer_html", "type": "html", "html": "@player.html" } ]
}
```

现有 3 个插件（bgm / 官方身份守卫 / rules-gate）继续按此格式工作。

### 4.2 B 型清单

```json
{
  "kind": "app",
  "name": "每日签到",
  "slug": "signin",
  "version": "1.0.0",
  "author": "yourname",
  "description": "签到、积分与签到榜",
  "requires": "2.1.0",

  "runtime": {
    "engine": "js",
    "entry": "main.js",
    "timeout_ms": 1000
  },

  "permissions": ["db", "settings.read"],

  "config": [
    { "key": "points", "label": "每次签到积分", "type": "number", "default": "5" }
  ]
}
```

B 型清单**只有元信息、权限、运行时、配置表单**四类字段。路由、表、菜单、任务、钩子全部在脚本里注册——这是与 A 型最本质的区别。

字段说明：

| 字段 | 必填 | 说明 |
|---|---|---|
| `kind` | 是 | 固定 `"app"`，用于与 A 型区分 |
| `name` / `slug` / `version` / `author` / `description` / `requires` | 同 A 型 | `slug` 规则见 4.4 |
| `runtime.engine` | 是 | 引擎标识，当前 `"js"` |
| `runtime.entry` | 是 | 入口脚本（相对插件目录，禁止路径穿越） |
| `runtime.timeout_ms` | 否 | 单次调用超时，默认 1500，硬上限 5000 |
| `permissions` | 是 | 能力白名单（至少 `[]`），见第 8 节 |
| `config` | 否 | 配置项声明，后台自动渲染表单（复用 A 型的现有实现） |

### 4.3 形态识别与兼容规则

| 清单情况 | 判定 | 内核行为 |
|---|---|---|
| 无 `kind` 字段 | A 型 | 走现有全部逻辑，**行为不变** |
| `"kind": "declarative"` | A 型 | 同上（显式写法） |
| `"kind": "app"` | B 型 | 加载脚本运行时 + 执行 `setup()` 注册 |
| `kind: "app"` 但缺少 `runtime.entry` 或脚本加载失败 | 加载失败 | 插件标记为"错误"，不影响其它插件与站点 |
| B 型同时写了 `hooks` | — | 兼容执行（便于从 A 型平滑迁移），但**文档不推荐**：B 型应使用 `clv.slot()` 等效实现 |
| 未知 `kind` 值 | 拒绝加载 | 列表页提示"不支持的插件类型" |
| `permissions` 缺失但脚本调用了受限 API | 运行时拒绝 | 报错并写审计日志 |

### 4.4 slug 规则

现有插件名允许中文（如"官方身份守卫"），但 URL 与表名需要安全标识，因此引入 `slug`：

- 显式声明优先；缺省时由 `name` 归一化（小写、非 `[a-z0-9_-]` 字符转 `-`、去除首尾 `-`）
- 归一化后为空则回退为 `plugin-<sha1(name)[:8]>`
- slug 在站点内唯一（冲突时安装失败并提示）
- 派生规则：前台路由前缀 `/x/<slug>/`、后台 `/admin/plugins/<slug>/`、数据表前缀 `pl_<slug>_`

---

## 5. B 型插件：运行时注册体系

### 5.1 加载与执行模型

```
启用插件
   │
   ├─ 1. 读取 plugin.json → 校验 kind / permissions / slug
   ├─ 2. 创建脚本 Runtime + 串行 worker（每插件一个）
   ├─ 3. 注入 Host API（受 permissions 约束）
   ├─ 4. 执行 main.js 顶层代码（只做定义，不注册）
   ├─ 5. 调用 setup()  ← 所有扩展点在此注册
   ├─ 6. 生成注册表快照（此后不可变，直到禁用/重载）
   └─ 7. 站点开始按注册表分发请求与事件

禁用 / 重载
   └─ 丢弃快照 + 关闭 worker（数据表保留）
```

关键约束：

- `setup()` 必须**幂等**（重载/重新启用会重复执行）
- `setup()` 内不得做耗时操作（内核对其施加更短的超时，如 3s）
- 注册的 handler 用**函数名**引用（便于调试与热重载），不用闭包引用
- `setup()` 未注册的能力，内核不调用
- worker 串行执行：同一插件的所有回调（路由、事件、任务）排队执行，**脚本内无需考虑并发**

### 5.2 注册 API 全集

```js
function setup() {
  // ── 数据表（运行时建表，幂等）──────────────────────
  clv.table("log", {                    // 实际表名 pl_<slug>_log
    user_id: "int", day: "string", created_at: "time"
  }, { unique: [["user_id", "day"]], indexes: [["day"]] });

  // ── 路由（path 相对 /x/<slug>）─────────────────────
  clv.route("GET",  "/",    "pageSignin", { auth: "user" });
  clv.route("POST", "/do",  "doSignin",   { auth: "user" });
  clv.route("GET",  "/rank","apiRank",    { auth: "none", json: true });

  // ── 后台页面与菜单（path 相对 /admin/plugins/<slug>）─
  clv.adminPage("/", "adminPage", { perm: "plugins" });
  clv.adminMenu({ label: "签到管理", path: "/", perm: "plugins", order: 50 });

  // ── UI 注入（slot 详见 6.4）───────────────────────
  clv.slot("post.card.after", "cardBadge");     // 也可直接传函数
  clv.slot("admin.settings.after", function (d) { return "<div class='card'>...</div>"; });

  // ── 事件与过滤器 ──────────────────────────────────
  clv.on("post.created", "onPostCreated");
  clv.filter("filter.content", 10, "censor");   // 优先级升序

  // ── 请求中间件 ────────────────────────────────────
  clv.middleware("http.before", function (req) {
    if (req.path === "/x/signin") { /* 可改写 req 或短路返回响应 */ }
  });

  // ── 定时任务 ──────────────────────────────────────
  clv.job("daily-reset", "24h", "onDaily");
}
```

`clv.*` 注册项与 A 型 `hooks` 的对应关系（迁移参考）：

| A 型写法 | B 型等价写法 |
|---|---|
| `{"hook":"footer_html","type":"html","html":"..."}` | `clv.slot("footer_html", function(d){ return "..."; })` |
| `{"hook":"content_filter","type":"filter","match":"..","replace":".."}` | `clv.filter("filter.content", 100, function(s){ return s.replace(/../g,"..") })` |
| `{"hook":"post_created","type":"http","url":"..."}` | `clv.on("post.created", function(e){ clv.http.request({...}) })` |
| `{"hook":"compose_guard","type":"guard",...}` | `clv.filter("filter.compose.guard", 50, function(g){ ... })` |
| `{"hook":"post_badge","type":"badge",...}` | `clv.slot("post.card.badge", ...)` 或 `clv.filter("filter.card", ...)` |

### 5.3 为什么"注册是命令式、权限是声明式"

这是一个必须坚持的安全边界：

- **权限只能声明式**：如果允许脚本运行时申请权限（`clv.requestPermission("db")`），那么插件在获得权限前就已经执行了任意代码，权限控制形同虚设。因此 `permissions` 必须在清单中声明，内核在**加载脚本之前**完成校验，并展示给管理员。
- **能力注册必须命令式**：注册逻辑本身是代码（可条件判断、可循环、可按配置裁剪），写在清单里会重新落回"白名单枚举"的老路，永远追不上需求。

一句话：**清单决定"能碰什么"，脚本决定"要做什么"。**

### 5.4 生命周期回调

B 型插件可选实现以下入口，内核在对应时机调用：

| 回调 | 时机 | 典型用途 |
|---|---|---|
| `setup()` | 启用/加载时，**必选** | 注册全部扩展点 |
| `onInstall(ctx)` | 安装完成后、首次 setup 前 | 初始化默认数据 |
| `onEnable(ctx)` / `onDisable(ctx)` | 启用 / 禁用时 | 资源准备与清理 |
| `onUninstall(ctx)` | 卸载前（管理员选择删除数据时） | 清理数据 |
| `onUpgrade(ctx, { from, to })` | 检测到版本变更 | 数据迁移、补列 |

### 5.5 handler 上下文与返回值约定

所有 handler（路由、slot、事件、过滤器、任务）统一接收 `ctx` 参数：

```js
function pageSignin(ctx) {
  // ctx = {
  //   req:   { method, path, query, form, json, headers, ip, fingerprint, files },
  //   params:{ id: "12" },        // 路由路径参数
  //   userId, adminId, isAdmin,   // 已解析的身份（未登录为 0 / false）
  //   csrf,                       // 当前 CSRF 令牌
  //   plugin:{ name, slug, dir, config(k) }
  // }

  return { template: "page.html", data: { days: 3 } };
}
```

返回值约定（内核统一处理）：

| 返回 | 行为 |
|---|---|
| `{ json: {...} }` | JSON 响应（可带 `status`、`headers`） |
| `{ body: "<h1>x</h1>" }` | HTML 响应 |
| `{ template: "page.html", data: {...} }` | 用插件目录下 `html/template` 模板渲染（可选 `layout: "layout.html"` 套用站点布局） |
| `{ redirect: "/x/signin/rank" }` | 302 跳转 |
| `{ file: "download.csv" }` | 插件目录下文件下载 |
| `undefined` / `null` | 该 slot 无输出 / 该事件处理完成 |
| 抛异常 | 记录日志；路由返回 500，事件/过滤器按"放行原值"处理 |

---

## 6. 扩展点全集（两种形态共用）

同一个注册表支撑 A、B 两型：A 型由清单转译成注册项，B 型由 `setup()` 注册。

### 6.1 Event（事件：只观察，不可改数据）

| 事件名 | 触发点 | A 型 | B 型 |
|---|---|---|---|
| `post.created` / `post.updated` / `post.deleted` | `front.go` ComposeSubmit / PostEditSave / PostDelete | `http` 钩子 | `clv.on` |
| `post.status_changed` | `admin.go` AdminReportHandle、`ai.go` applyVerdict | 同上 | `clv.on` |
| `comment.created` / `comment.deleted` | `front.go` addComment、deletePostCascade | 同上 | `clv.on` |
| `like.toggled` | `front.go` toggleLike | 同上 | `clv.on` |
| `report.created` / `report.handled` | `api.go` APIReport / `admin.go` AdminReportHandle | 同上 | `clv.on` |
| `user.registered` / `user.login` / `user.logout` | `auth.go` | 同上 | `clv.on` |
| `admin.login` | `admin.go` AdminLoginSubmit | 同上 | `clv.on` |
| `media.uploaded` | `util.SaveImage/SaveVideo` 调用方 | 同上 | `clv.on` |
| `plugin.installed` / `enabled` / `disabled` / `uninstalled` | 生命周期 | 同上 | `clv.on` |
| `ai.verdict` | `ai.go` applyVerdict | 同上 | `clv.on` |

事件 payload 沿用现有 Webhook 风格（含 `event` 与 `time`），A 型 `http` 钩子与 B 型 `clv.on` 收到同一份数据。

### 6.2 Filter（过滤器：链式改写，带优先级）

| 过滤器 | 位置 | 签名 |
|---|---|---|
| `filter.content` | 发帖/评论入库前（**含补齐 `front.go:395` 缺失调用**） | `string → string` |
| `filter.nickname` | 发帖/评论前 | `string → string` |
| `filter.card` | `fetchCards` 每张 Card 组装后 | `Card → Card` |
| `filter.post.visible` | 详情页可见性判定 | `bool → bool`（任一插件 veto 即不可见） |
| `filter.api.response` | `okJSON` 前 | `map → map` |
| `filter.theme.css` | `ThemeCSS` 输出前 | `string → string` |
| `filter.admin.stats` | `AdminDashboard` 统计 | `map → map` |
| `filter.settings.save` | `AdminSettingsSave` 写入前 | `map → map` |
| `filter.auth.login` | `LoginSubmit` 校验通过后 | `bool → bool`（可拒绝登录） |
| `filter.compose.guard` | `guardCompose` 内 | `guardResult → guardResult` |

约定：按 `priority` 升序串联（A 型固定 100）；任一环节抛错 → 记日志并**放行原值**，不因插件故障阻断站点。

### 6.3 Middleware（中间件：可拦截 HTTP）

| 钩子 | 位置 | 能力 |
|---|---|---|
| `http.before` | `middleware.Use` 中、CSRF 校验**之前** | 观察/改写请求、设置响应头、**短路返回**（自定义鉴权、维护模式） |
| `http.after` | 响应写出后 | 观察状态码/耗时 |
| `http.route` | 路由未命中时 | 自定义路由匹配（兜底给插件路由） |

### 6.4 Slot（UI 注入点）

| Slot | 位置 | A 型 | B 型 |
|---|---|---|---|
| `head.end` | `layout.html` `</head>` 前 | 新增 | `clv.slot` |
| `body.start` | `layout.html` `<body>` 后 | 新增 | `clv.slot` |
| `body.end` | `layout.html` `</body>` 前 | 新增 | `clv.slot` |
| `header_html` / `footer_html` | 现有位置 | 支持 | `clv.slot` |
| `post.card.after` | 首页卡片（前端渲染，见 6.5） | 新增 | `clv.slot` |
| `post.detail.after` | 详情页正文后 | 新增 | `clv.slot` |
| `post.detail.actions.after` | 详情页操作区后 | 新增 | `clv.slot` |
| `comment.item.after` | 每条评论后 | 新增 | `clv.slot` |
| `compose.form.after` | 发帖表单后 | 新增 | `clv.slot` |
| `profile.after` | 个人主页 | 新增 | `clv.slot` |
| `admin.head.end` / `admin.body.end` | 后台布局 | 新增 | `clv.slot` |
| `admin.sidebar.after` | 后台侧边栏底部 | 新增 | `clv.slot` |
| `admin.dashboard.after` | 仪表盘 | 新增 | `clv.slot` |
| `admin.settings.after` | 设置页 | 新增 | `clv.slot` |
| `admin_plugins_top` | 现有位置 | 支持 | `clv.slot` |

与 A 型的关键区别：B 型的 slot handler **接收数据、可动态生成 HTML**（例如按当前帖子内容、当前用户权限决定输出），而 A 型只是静态片段。

### 6.5 前端扩展（`window.CLV`）

首页卡片由 JS 渲染，服务端 slot 覆盖不到，需前端 hook：

```js
// 内核暴露（app.js 末尾）
window.CLV = {
  version, csrf,
  api,            // fetch 封装（自动带 CSRF / 指纹 / X-Requested-With）
  el, toast, fingerprint,
  hooks: {
    cardHtml: [],   // (html, post) => html
    cardEl:   [],   // (el, post) => el | void
    afterRender: [] // (root) => void
  }
};
```

`cardEl()` 返回前依次调用 `hooks.cardHtml`，渲染后触发 `afterRender`。B 型插件在 `body.end` slot 注入脚本注册：

```js
CLV.hooks.cardHtml.push(function (html, p) {
  if (p.likes > 10) html += '<span class="hot">热帖</span>';
  return html;
});
```

### 6.6 Route（路由）

| 类型 | 实际挂载 | 鉴权 |
|---|---|---|
| 前台页面 | `/x/<slug>/<path>` | `auth`：`none` / `user` / `admin` |
| 前台 API | 同上，`json: true` | 同上 |
| 后台页面 | `/admin/plugins/<slug>/<path>` | 强制 `requireAdmin` + `perm` 声明 |
| 静态资源 | `/x/<slug>/assets/*` | 自动映射插件目录下 `assets/`（无鉴权，只读） |

`path` 是相对路径，内核统一加前缀，从根本上避免插件间路由冲突。

### 6.7 Job（定时任务）

`clv.job(name, every, handler)`，`every` 支持 `30s` / `10m` / `1h` / `24h`。

内核调度器（新增 `internal/plugin/scheduler.go`，复用 `ai.StartPatrol` 模式）：
- 启动时注册，`time.Ticker` 驱动，单任务互斥
- 触发时同样走沙箱（超时、权限、审计）
- 执行结果写入 `plugin_logs`，后台可查

### 6.8 Data（自定义数据表）

`clv.table(name, columns, opts)`（也可在 A 型清单中声明，供"无脚本但需建表"的场景）：

类型映射由方言层翻译：

| 声明类型 | SQLite | MySQL |
|---|---|---|
| `int` | `INTEGER` | `INT` |
| `bigint` | `INTEGER` | `BIGINT` |
| `string` | `TEXT` | `VARCHAR(255)` |
| `text` | `TEXT` | `MEDIUMTEXT` |
| `bool` | `INTEGER` | `TINYINT` |
| `time` | `TEXT`（RFC3339） | `VARCHAR(40)`（与内核时间字段一致） |
| `json` | `TEXT` | `MEDIUMTEXT` |

- 表名统一 `pl_<slug>_` 前缀，插件无需关心实际名称（用 `clv.db.table("log")` 取）
- 卸载时**默认保留数据**，后台询问"是否同时删除插件数据表"
- 写操作默认只允许 `pl_` 前缀表；内核表只读（`db.admin` 权限可放开，需管理员显式授权）

---

## 7. Host API 清单

B 型插件可用的全部能力（按 `permissions` 逐项授权）：

```js
// ── 元信息与配置 ────────────────────────────────
clv.plugin.name / slug / dir / version
clv.plugin.config(key)          // 读配置（等价于现有 plugin.<name>.<key>）
clv.plugin.configs()
clv.version                     // 内核版本

// ── 数据 ───────────────────────────────────────
clv.db.query(sql, ...args)      // -> [{...}]（行数受配额约束）
clv.db.get(sql, ...args)        // -> {...} | null
clv.db.exec(sql, ...args)       // -> { rowsAffected, lastId }
clv.db.tx(fn)                   // 事务；fn 抛错自动回滚
clv.db.dialect()                // "sqlite" | "mysql"
clv.db.table("log")             // -> "pl_signin_log"

// ── 站点高层数据（推荐，替代手写 SQL） ─────────────
clv.post.list({ before, limit, topic })
clv.post.get(id) / create({...}) / update(id, {...}) / remove(id)
clv.comment.list(postId) / create({...}) / remove(id)
clv.user.get(id) / byName(name) / current(ctx)
clv.setting.get(k) / set(k, v) / all()
clv.stats.overview() / today()

// ── 视图 ───────────────────────────────────────
clv.view.template("page.html", data)   // 渲染插件目录下模板
clv.view.escape(s) / nl2br(s)

// ── 其它能力 ───────────────────────────────────
clv.http.request({ method, url, headers, body, timeout_ms, max_bytes })
clv.media.saveImage(file) / saveVideo(file)
clv.cache.get(k) / set(k, v, ttlSec) / del(k)
clv.mail.send(to, subject, body)
clv.crypto.bcrypt(pwd) / verify(hash, pwd) / hmacSha256(key, msg) / randomHex(n) / uuid()
clv.log.info(...) / warn(...) / error(...)     // 写 plugin_logs
clv.json.encode(v) / decode(s)
```

> 同步风格：goja 不支持 `async/await` 语义，所有 Host API 均为同步调用（HTTP 请求由内核阻塞执行并受超时保护）。
>
> 引擎选型：默认 **goja**（纯 Go、无 CGO，插件作者最熟悉 JS，现有 `rules-gate` 已是 JS）；抽象出 `Runtime` 接口，后续可加 gopher-lua / wazero。goja 的 `Runtime` 非 goroutine-safe，因此每个插件配一条串行 worker 队列，天然串行 + 可超时中断（`vm.Interrupt`）。

---

## 8. 沙箱、权限与治理

| 维度 | 措施 |
|---|---|
| 执行时间 | 每次调用 `context` 超时，默认 1500ms，清单可调，**硬上限 30s**（支持 AI 调用等耗时操作）；`setup()` 另设 5s 上限 |
| 资源配额 | 中断计数（防死循环）；单次 HTTP 响应 ≤ 2MB；`db.query` 行数 ≤ 1000 |
| 权限白名单 | 每个 Host API 入口校验清单 `permissions`，未声明直接抛错（错误提示"如何声明"） |
| 数据隔离 | 默认仅插件表可写；内核表只读；`db.admin` 需管理员在后台显式授权 |
| SSRF 防护 | `clv.http` 需 `net` 权限；默认拒绝私有/环回/链路本地 IP；声明 `net.local` 后放行（用于本机 Ollama 等内网 AI 服务） |
| 审计 | 新增 `plugin_logs` 表（`slug`/`action`/`detail`/`duration_ms`/`created_at`），后台可查 |
| 故障隔离 | 所有回调 `recover`；单插件异常不影响其它插件与主流程；连续 20 次异常自动禁用（熔断） |
| 总开关 | 网站设置新增 `plugins_runtime_enabled`（默认开），可一键停用全部 B 型插件用于故障处置 |
| 分发安全 | 市场下载保留 SHA256 校验；P3 增加"官方签名"标记（展示用途，非强制） |

权限清单（`permissions` 取值）：

| 权限 | 覆盖能力 |
|---|---|
| `db` | `clv.db.*`（仅插件表可写） |
| `db.admin` | 可写内核表（危险，需管理员二次确认） |
| `settings.read` / `settings.write` | `clv.setting.get` / `set` |
| `site.read` | `clv.post.*` / `clv.comment.*` / `clv.user.*`（只读） |
| `site.write` | 同上 + 写操作 |
| `net` | `clv.http.request`（默认拒绝内网地址） |
| `net.local` | 额外允许访问内网/本机地址（如 `http://127.0.0.1:11434` 的 Ollama） |
| `media.write` | `clv.media.saveImage/saveVideo` |
| `mail` | `clv.mail.send` |
| `cache` | `clv.cache.*` |
| `log` | `clv.log.*` |

---

## 9. 内核改造清单（文件级）

| 文件 | 改动 |
|---|---|
| `main.go` | 挂载插件路由捕获器（`/x/`、`/admin/plugins/`）；启动 `plugin.Boot()`（加载 A/B 两型）与 `plugin.StartScheduler()` |
| `internal/database/database.go` | 新增 `plugin_logs` 表；暴露 `Dialect()` 供插件层使用 |
| `internal/plugin/plugin.go` | 保留全部现有函数签名（变为兼容 shim，内部转调新注册表） |
| `internal/plugin/manifest.go`（新） | A/B 型识别、slug 归一化、权限校验、兼容性检查 |
| `internal/plugin/registry.go`（新） | 扩展点注册表（Event/Filter/Middleware/Slot/Route/Job/Table） |
| `internal/plugin/hub.go`（新） | 事件总线 + 过滤器链（优先级、panic 隔离、空链零开销） |
| `internal/plugin/runtime.go` + `runtime_goja.go`（新） | 引擎抽象 + goja 实现 + 每插件串行 worker |
| `internal/plugin/api_*.go`（新） | 注册 API + 能力 API 分组实现 |
| `internal/plugin/sandbox.go`（新） | 超时、配额、权限、审计、熔断 |
| `internal/plugin/lifecycle.go`（新） | 安装/启用/禁用/卸载/升级 + 建表 |
| `internal/plugin/scheduler.go`（新） | 定时任务调度 |
| `internal/plugin/adminui.go`（新） | 后台菜单注册表 + 插件页面分发 |
| `internal/plugin/view.go`（新） | `ViewProvider` 反向注入接口，解决包循环依赖（见下） |
| `internal/handlers/handlers.go` | `Render` 注入 slot 数据；初始化时向 plugin 注册 `ViewProvider` |
| `internal/handlers/front.go` | 插入第 6.1/6.2 节的事件与过滤器调用点（约 12 处），并补齐 `ComposeSubmit` 的 `filter.content` |
| `internal/handlers/api.go` | 同上（约 8 处） |
| `internal/handlers/admin.go` | 侧边栏菜单数据化；插件管理页增加插件类型徽章、「运行日志」「插件页面」入口 |
| `internal/middleware/middleware.go` | CSRF 之前插入 `http.before` 链；响应后触发 `http.after` |
| `web/templates/layout.html` | 新增 `head.end` / `body.start` / `body.end` 三个 slot |
| `web/templates/admin_layout.html` | 侧边栏改为 `{{range .pluginMenus}}`；新增 `admin.head.end` / `admin.sidebar.after` slot |
| `web/templates/front.html` / `admin.html` | 新增各内容级 slot（详见 6.4） |
| `web/static/app.js` | 末尾暴露 `window.CLV`；`cardEl` 接入 `hooks.cardHtml` / `afterRender` |
| `docs/PLUGIN.md` | 重构为**插件开发总览**；A 型细节拆至 `PLUGIN-DECLARATIVE.md`，B 型细节拆至 `PLUGIN-APP.md` |

**关键架构约束：包循环依赖**

`handlers` → `plugin`（现有依赖方向）。插件页面渲染需要模板能力（在 `handlers`），但 `plugin` 不能 import `handlers`。解决方式：**反向注入接口**。

```go
// internal/plugin/view.go
type ViewProvider interface {
    RenderPage(w http.ResponseWriter, r *http.Request, layout, page string, data map[string]any)
    RenderTemplate(fsys fs.FS, name string, data any) (string, error)
    CSRF(r *http.Request) string
    CurrentUserID(r *http.Request) int64
    CurrentAdminID(r *http.Request) int64
}

var viewProvider ViewProvider
func SetViewProvider(p ViewProvider) { viewProvider = p }
```

`handlers` 在 `SetupTemplates()` 之后调用 `plugin.SetViewProvider(...)` 完成装配。

---

## 10. 兼容性策略与回归清单

### 10.1 兼容保证

| 项 | 保证 |
|---|---|
| 现有 3 个插件包（bgm / 官方身份守卫 / rules-gate） | 不改动即可安装、启用、生效 |
| A 型 `plugin.json` 格式 | 完全支持，行为不变（无 `kind` 字段即 A 型） |
| `data/plugins/<name>/` 目录结构 | 不变（slug 只影响路由/表名，不影响目录） |
| `enabled_plugins` 设置键 | 不变 |
| `plugin.<name>.<key>` 配置键 | 不变 |
| 现有 HTTP 路由与模板输出 | 不变（slot 为空时输出空字符串） |
| 现有 Go 函数签名（`CallHTML`/`CallFilter`/`Notify`/`CheckComposeGuard`/`NicknameBadges`/`GuardNicknames`/`List`/`Install`/`Remove`/`SetEnabled`/`GetConfig`/`SaveConfig`/`ConfigOf`） | 全部保留 |
| 编译 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` 必须通过 |
| 二进制体积 | 记录基线；goja 预计 +3~6MB，可留 build tag（`-tags no_plugin_runtime`）裁剪 |

### 10.2 回归测试清单

1. 三只现有 A 型插件：安装 → 启用 → 功能验证（背景音乐、守卫拦截、守则弹窗）
2. 插件配置保存、素材上传/替换/清理
3. 插件商店安装（SHA256 校验路径）
4. 前台发帖 / 评论 / 点赞 / 举报 / 编辑 / 删除全流程
5. 后台全部页面可打开、菜单完整（含 B 型插件注册的菜单）
6. SQLite 与 MySQL 双库各跑一遍
7. 无插件时（`data/plugins` 为空）站点行为与性能不变
8. B 型插件死循环 / 抛异常：站点不阻塞、自动熔断
9. A 型与 B 型**同时启用**：互不干扰，注册表合并正确

---

## 11. 分阶段实施计划

> 每个阶段结束都必须满足：可编译、可部署、现有功能无回归。

### P0 —— 运行时地基（不影响任何现有行为）

1. `manifest.go`：A/B 型识别 + slug 归一化 + 权限字段解析
2. `runtime.go` + `runtime_goja.go`：引擎抽象 + goja + 每插件串行 worker
3. `sandbox.go`：超时、权限校验、panic 隔离、`plugin_logs`
4. 注册 API 第一批：`clv.table` / `clv.on` / `clv.filter` / `clv.log` / `clv.plugin.config` / `clv.setting.*` / `clv.db.*` / `clv.json`
5. `lifecycle.go`：`setup()` 调用 + 建表 + 生命周期回调
6. 示例插件 `plugins/demo-app/`（B 型：建表 + 事件 + 过滤器）
7. 引入依赖：`github.com/dop251/goja`（纯 Go，无 CGO）

**验收**：A 型插件行为零变化；B 型示例插件能建表、订阅 `post.created`、过滤 `filter.content`。

### P1 —— 页面与路由扩展

1. `registry.go` + `view.go`（ViewProvider 反向注入）
2. `clv.route` / `clv.adminPage` / `clv.adminMenu`：前台 `/x/<slug>/`、后台 `/admin/plugins/<slug>/`
3. 后台侧边栏数据化 + 插件类型徽章
4. Slot 全量落地（前台 + 后台，约 15 个）+ `clv.slot`
5. `window.CLV` 前端 API + 卡片 hook
6. 示例插件 `plugins/signin/`（签到：表 + 前台页 + 后台菜单 + 卡片展示）

**验收**：第 2.1 节验收功能 1、2 达成。

### P2 —— 深度集成

1. 事件/过滤器全清单接入（第 6.1 / 6.2 节全部）
2. `clv.middleware`：`http.before` / `http.after` / `http.route`
3. `clv.job`：定时任务调度 + 后台可视化
4. `clv.http` / `clv.media` / `clv.cache` / `clv.mail` / `clv.crypto`
5. `clv.view.template`：插件模板文件渲染
6. 示例插件：内容摘要（filter）、活动抽奖页（route + table）

**验收**：第 2.1 节验收功能 3、4、5 达成。

### P3 —— 治理与生态

1. 权限管理 UI（安装时向管理员展示并授权）+ 后台"脚本插件总开关"
2. `plugin_logs` 审计页 + 熔断策略可配置
3. 插件市场支持 B 型包（权限/引擎/体积字段展示 + 审核流程）
4. **模板覆盖**：插件目录 `templates/` 覆盖内核同名片段（最接近"任意改前端"的能力）
5. 内置功能插件化试点（如"公告增强"用 B 型插件重写）
6. 开发工具：`clearlove plugin new/pack/check` 命令行 + B 型开发文档

---

## 12. 示例：签到应用插件（B 型，完全不用声明式钩子）

目录结构：

```
plugins/signin/
├── plugin.json
├── main.js
└── page.html
```

`plugin.json`：

```json
{
  "kind": "app",
  "name": "每日签到",
  "slug": "signin",
  "version": "1.0.0",
  "author": "ClearLove",
  "description": "签到、积分与签到榜",
  "requires": "2.1.0",
  "runtime": { "engine": "js", "entry": "main.js", "timeout_ms": 1000 },
  "permissions": ["db", "settings.read"],
  "config": [
    { "key": "points", "label": "每次签到积分", "type": "number", "default": "5" }
  ]
}
```

`main.js`：

```js
var T = clv.db.table("log");   // pl_signin_log

function setup() {
  clv.table("log", {
    user_id: "int", day: "string", points: "int", created_at: "time"
  }, { unique: [["user_id", "day"]] });

  clv.route("GET",  "/",   "pageSignin", { auth: "user" });
  clv.route("POST", "/do", "doSignin",   { auth: "user" });
  clv.route("GET",  "/rank", "apiRank",  { auth: "none", json: true });

  clv.adminPage("/", "adminPage", { perm: "plugins" });
  clv.adminMenu({ label: "签到管理", path: "/", perm: "plugins", order: 50 });

  clv.slot("post.card.after", "cardBadge");
  clv.job("monthly-reset", "24h", "onDaily");
}

function pageSignin(ctx) {
  var today = new Date().toISOString().slice(0, 10);
  var done = clv.db.get("SELECT id FROM " + T + " WHERE user_id=? AND day=?", ctx.userId, today);
  var total = clv.db.get("SELECT IFNULL(SUM(points),0) AS n FROM " + T + " WHERE user_id=?", ctx.userId);
  return { template: "page.html", data: { done: !!done, total: total.n, points: clv.plugin.config("points") } };
}

function doSignin(ctx) {
  var today = new Date().toISOString().slice(0, 10);
  if (clv.db.get("SELECT id FROM " + T + " WHERE user_id=? AND day=?", ctx.userId, today)) {
    return { status: 400, json: { ok: false, msg: "今天已经签到过了" } };
  }
  clv.db.exec("INSERT INTO " + T + "(user_id,day,points,created_at) VALUES(?,?,?,?)",
              ctx.userId, today, Number(clv.plugin.config("points")), new Date().toISOString());
  return { json: { ok: true, msg: "签到成功" } };
}

function apiRank(ctx) {
  return { json: clv.db.query(
    "SELECT user_id, SUM(points) AS n FROM " + T + " GROUP BY user_id ORDER BY n DESC LIMIT 20") };
}

function cardBadge(post) {
  return post.user_id ? "<span class='signin-dot' title='活跃用户'></span>" : "";
}

function adminPage(ctx) {
  var rows = clv.db.query("SELECT day, COUNT(1) AS n FROM " + T + " GROUP BY day ORDER BY day DESC LIMIT 30");
  return { template: "admin.html", data: { rows: rows } };
}

function onDaily(ctx) {
  clv.log.info("签到插件每日任务执行");
}
```

对比 A 型：该插件**没有 `hooks` 字段**，全部能力（表、路由、后台菜单、slot、任务）均通过 `setup()` 中的注册 API 获得——这正是新增的第二种插件形式。

---

## 13. 风险与取舍

| 风险 | 影响 | 对策 |
|---|---|---|
| 二进制体积增大（goja） | 单文件约 +3~6MB | 可接受；预留 build tag 裁剪 |
| 脚本 = 代码执行 | 安全面扩大 | 权限声明（清单）+ 配额 + 审计 + 熔断 + 总开关 + 市场审核 |
| 双形态并存 | 概念与维护成本上升 | 职责边界清晰（A 型=声明、B 型=脚本），管理页用类型徽章区分；两型共用同一注册表，内核代码不翻倍 |
| 热路径性能 | 每张卡片/每请求都过插件链 | 注册表快照 + 空链零开销 + 每请求只取一次快照 |
| MySQL/SQLite 方言 | 插件 SQL 可能只兼容一种 | `clv.db.dialect()` + 高层 `clv.post.*` API + 建表由内核翻译 |
| goja 不支持 async/await | 插件作者写法受限 | 全部同步 API + 文档明确说明 |
| 插件质量参差 | 拖垮站点 | 熔断 + 后台一键停用 + 运行日志可见 |
| "万物"预期过高 | 内核布局/流程无法替换 | 文档明确边界；P3 模板覆盖补齐前端自定义能力 |

---

## 14. 附录：工作量估算

| 阶段 | 主要产出 | 预估 |
|---|---|---|
| P0 | B 型运行时 + 沙箱 + 注册 API 第一批 + 示例 | 5~8 人日 |
| P1 | 路由 + 后台菜单 + slot + 前端 API + 签到样例 | 5~8 人日 |
| P2 | 全量事件/过滤器接入 + 中间件 + jobs + 能力 API | 6~10 人日 |
| P3 | 治理 + 市场 + 模板覆盖 + 工具链 | 5~8 人日 |

> 建议：**P0 + P1 为一个交付批次**（约 2 周），完成后即可支撑绝大多数"新功能类插件"；P2 补齐深度集成，P3 面向生态与治理。

---

## 15. 实现状态（本仓库当前进度）

> 本节记录实际落地情况；与上方设计稿如有差异，以本节为准。

### 15.1 已实现

运行时内核 `internal/plugin/`：

| 文件 | 职责 |
|---|---|
| `manifest.go` | A/B 型识别、slug 归一化、权限声明解析、超时策略 |
| `registry.go` | 扩展点注册表 + 不可变快照（热路径无锁读取） |
| `runtime.go` | 每插件串行 Worker（goja 非线程安全，全部调用排队执行） |
| `runtime_goja.go` | goja 装配、函数调用、值转换、日志 / JSON / 配置 API |
| `api_db.go` | `clv.db.*` + `clv.table`（写表前缀白名单、行数配额、双库方言翻译） |
| `api_site.go` | `clv.setting` / `clv.post` / `clv.comment` / `clv.user` / `clv.stats` |
| `api_view.go` | `clv.route` / `adminPage` / `adminMenu` / `slot` / `on` / `emit` / `filter` / `job` / `middleware` / `view` |
| `api_net.go` | `clv.http` / `cache` / `crypto` / `mail` / `media`（SSRF 防护、配额） |
| `hub.go` | 事件总线、过滤器链、Slot 渲染、中间件执行 |
| `router.go` | `/x/<slug>/...` 与 `/admin/plugins/<slug>/...` 分发、鉴权、返回值约定 |
| `lifecycle.go` | `Boot` / `LoadApp` / `UnloadApp` / `ReloadApp`、setup 调用、生命周期回调 |
| `scheduler.go` | 定时任务调度（最小周期 10s，运行留痕） |
| `sandbox.go` | 权限校验、`plugin_logs` 审计、失败熔断（连续 20 次自动禁用） |
| `view.go` | `ViewProvider` 反向注入接口（解决包循环依赖） |

内核接入：

- `main.go`：`plugin.Boot()` + `/x/` 与 `/admin/plugins/` 路由挂载
- `middleware`：`http.before` 中间件链（可短路响应，绕过 CSRF）
- `handlers`：Slot 注入、后台插件菜单数据化、HTML 渲染器接入
- 事件触发点已接入：`post.created` / `comment.created` / `report.created` / `like.toggled` / `post.updated` / `post.deleted` / `user.login` / `user.registered`
- `handlers/plugin_bridge.go`：模板与会话能力的反向注入
- `database`：新增 `plugin_logs` 表
- 前端：`window.CLV`（`api` / `el` / `toast` / `fingerprint` + `cardHtml` / `cardEl` / `afterRender` 钩子）
- 模板 Slot：`head.end` / `body.end` / `admin.head.end` / `admin.body.end` / `admin.sidebar.after` / `admin.settings.after` / `admin.dashboard.after`，并保留原有 `header_html` / `footer_html` / `admin_plugins_top`
- 后台「插件管理」显示「声明式 / 应用型」类型与运行状态、插件路由前缀

示例与测试：

- `plugins/signin/`：应用型插件示例（数据表 + 前台页 + 后台页 + 菜单 + Slot + 定时任务，**完全不使用声明式钩子**）
- `internal/plugin/app_test.go`：生命周期 / 权限拒绝 / 超时中断 / 声明式回归
- `internal/plugin/router_test.go`：鉴权与返回值约定
- `main_test.go`：走完整中间件与路由链的端到端集成测试

### 15.2 已实现 API

```js
// 元信息与配置
clv.version · clv.plugin.{name,slug,dir,version,config,configs}
// 日志与序列化
clv.log.{info,warn,error} · clv.json.{encode,decode}
// 数据
clv.table(name, columns, opts) · clv.db.{query,get,exec,tx,dialect,table}
// 站点数据
clv.setting.{get,set,all} · clv.post.{list,get,create,update,remove}
clv.comment.{list,create,remove} · clv.user.{get,byName,current} · clv.stats.{overview,today}
// 注册
clv.route · clv.adminPage · clv.adminMenu · clv.slot · clv.on · clv.emit
clv.filter(name, priority, handler) · clv.job(name, every, handler) · clv.middleware(phase, handler)
// 视图
clv.view.{template,escape,nl2br}
// 其它
clv.http.request · clv.cache.{get,set,del} · clv.crypto.{bcrypt,verify,hmacSha256,randomHex,uuid,base64Encode,base64Decode}
clv.mail.send · clv.media.{saveImage,saveVideo}
```

权限取值：`db` / `db.admin` / `settings.read` / `settings.write` / `site.read` / `site.write` / `net` / `net.local` / `media.write` / `mail` / `cache` / `log`。

**内置应用型插件示例**

| 插件 | 说明 | 用到的能力 |
|---|---|---|
| `plugins/signin/` | 每日签到：数据表 + 前台页 + 后台页 + 菜单 + 定时任务 | `db` / `cache` / 路由 / slot / job |
| `plugins/ai-polish/` | AI 语句美化：发帖页按钮，调用站点已接入的 AI 模型润色内容 | 声明式 hook 静态注入 + 路由 + `settings.read` / `net` / `net.local` / `cache` + 限流 |

> `ai-polish` 演示了一个实用组合：**UI 用声明式 hook 静态注入**（页面渲染零开销），
> **功能走应用型路由**（只在用户点击时才进入脚本运行时）——避免把耗时逻辑放在 slot 渲染里阻塞页面。

### 15.3 已实现（第二轮补齐）

| 能力 | 说明 |
|---|---|
| `http.after` 中间件 | 包装 ResponseWriter 记录状态码，请求结束后回调插件（含耗时、IP、指纹） |
| 过滤器点（全量） | `filter.content`、`filter.card`、`filter.post.visible`、`filter.api.response`、`filter.theme.css`、`filter.auth.login`、`filter.compose.guard`、`filter.admin.stats`、`filter.settings.save` |
| 模板覆盖 | 启用插件的 `templates/*.html` 覆盖内核同名片段（Go 模板后解析优先）；插件启停后自动重建模板集 |
| 后台运行日志页 | `/admin/plugin-logs`，支持按插件筛选、一键清空；侧边栏「插件日志」入口 |
| 定时任务可视化 | 插件管理页展示各插件的任务名与周期 |
| 权限审阅 | 插件卡片展示已声明权限，启用前 `data-confirm` 提示；插件商店展示形态与权限 |
| 运行时重载 | 插件管理页「重载」按钮（改脚本后无需禁用再启用） |
| `clv.media.saveVideo` | 与 `saveImage` 对称，支持 base64 写入（扩展名白名单 + 70MB 上限） |
| 前端 Slot 补齐 | `post.detail.actions.after`、`comments.after`、`compose.after`、`profile.after`，加上此前的 `head.end` / `body.end` / 后台各 Slot |

### 15.4 云端（插件社区）双形态支持

云端服务（`cloud/`，独立 Go 项目，部署于 clearlove.kazx.top）已完成升级，与客户端打通"上传 → 审核 → 市场 → 安装"全链路：

**数据层**
- `plugins` / `plugin_versions` 新增 `kind`、`permissions`、`runtime`、`requires`、`has_templates`、`hooks_count` 列（启动时自动 `ALTER TABLE` 迁移，幂等）
- 历史数据自动回退为 `declarative`；筛选使用 `NULLIF` 兼容空串，新旧数据一视同仁

**上传与校验**（开发者后台上传 zip 时）
- 自动识别形态：无 `kind` 字段 → 声明式；`kind=app` → 应用型
- 应用型强制校验：`runtime.engine`（当前仅 js）、`runtime.entry`（禁路径穿越）、`permissions` 白名单
- 声明式强制校验：至少一个 `hooks` 项
- 自动统计：`hooks_count`、是否含 `templates/` 模板覆盖
- 新版本上传会同步刷新插件主记录的形态快照

**市场与审核**
- 市场列表/详情展示「声明式 / 应用型」徽章、权限清单、运行时引擎、版本要求、模板覆盖标记
- 市场支持 `?kind=app|declarative` 按形态筛选（官网页面与开放 API 均支持）
- 开发者后台：插件管理页展示形态与权限；发布页提示两种形态要求
- 管理后台审核列表：展示形态与权限（应用型重点核对），支持按形态筛选
- 开发文档页新增「两种插件形态」章节，含 `plugin.json` 与 `main.js` 示例

**开放 API 新增字段**（老客户端忽略未知字段，完全兼容）
```json
{
  "kind": "app", "kind_label": "应用型",
  "permissions": ["db", "settings.read"],
  "requires": "2.1.0", "engine": "js",
  "has_templates": false, "hooks_count": 0
}
```

**客户端配合**
- 插件商店页支持按形态筛选（`/admin/store?kind=app`）
- 商店卡片与安装确认展示权限清单，安装应用型插件后需在插件管理页启用

### 15.5 暂未提供（可选的后续演进）

- WASM / Lua 引擎（`Runtime` 接口已预留，当前仅 goja）
- 插件签名与官方认证标记（当前依赖市场 SHA256 校验）
- 插件之间的依赖声明与加载顺序编排
- 后台可视化调试台（REPL / 单步）
- 脚本文件变更的自动热重载（当前是后台「重载」按钮或保存配置触发）

### 15.4 安装使用

1. 将 `plugins/signin/` 整个目录复制到站点的 `data/plugins/signin/`
2. 后台「插件管理」→ 点击**启用**（应用型插件启用后立即加载运行时并执行 `setup()`）
3. 前台访问 `/x/signin/`，后台左侧出现「签到管理」菜单

> 打包分发：把插件目录（`plugin.json` + `main.js` + 模板文件）压成 zip，后台「插件管理 → 安装插件」上传即可；
> `plugin.json` 必须在压缩包内（建议直接放根目录），且为 UTF-8 无 BOM。
