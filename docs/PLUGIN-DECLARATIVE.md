# 声明式插件开发指南（A 型）

> 本文档面向**声明式插件**：只有一份 `plugin.json`，用 `hooks` 描述"要往哪里注入什么"，
> 由内核解释执行。**无需写代码、无需编译**，适合轻量扩展。
>
> 需要独立页面、数据表、后台菜单、定时任务？请改用**应用型插件** → [PLUGIN-APP.md](PLUGIN-APP.md)。
> 通用部分（打包、安装、配置项、权限、调试）见 [PLUGIN.md](PLUGIN.md)。

---

## 1. 插件包结构

```
your-plugin.zip
├── plugin.json          # 必需：插件清单（放在根目录或任意子目录均可）
└── （可选）其他静态资源，如 @片段.html
```

打包要求：

- 必须是 zip 格式，`plugin.json` 需为 **UTF-8 无 BOM**
- 插件名（`name`）会被用作目录名，建议避免 `/`、`\` 等字符
- 单包不超过 50MB
- 各系统打包命令、开发调试循环（全程无需源码）见 [PLUGIN.md](PLUGIN.md) 2.0 节

安装后在后台「插件管理」点击**启用**才生效。

---

## 2. plugin.json 完整字段

```json
{
  "name": "插件名（必需，唯一）",
  "version": "1.0.0",
  "author": "作者",
  "description": "一句话说明",
  "requires": "2.0.0",
  "config": [ ... ],
  "hooks": [ ... ]
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | ✅ | 插件名，同时作为安装目录名 |
| `version` | | 版本号，展示用 |
| `author` | | 作者名 |
| `description` | | 简介，展示在插件列表与插件市场 |
| `requires` | | 建议的最低表白墙版本（不满足时后台显示 ⚠，不阻止启用） |
| `config` | | 配置项声明，见第 3 节 |
| `hooks` | ✅ | 钩子列表，见第 4 节 |

> 清单里**不要**写 `kind` 字段（那是应用型插件的标识）；写了 `"kind": "app"` 会走另一套运行时。

---

## 3. 配置项（config）

插件可以声明若干配置项，表白墙会**自动在后台「插件管理 → 插件设置」渲染表单**，
管理员填写后立即生效，无需重新安装插件。

```json
"config": [
  { "key": "nicknames", "label": "专享昵称", "type": "textarea",
    "default": "官方,公告", "help": "多个昵称用逗号或换行分隔" },
  { "key": "badge",     "label": "帖子标识", "type": "text", "default": "官方" },
  { "key": "enable",    "label": "启用提示", "type": "switch", "default": "1" },
  { "key": "position",  "label": "显示位置", "type": "select",
    "default": "bottom", "options": ["top", "bottom"] }
]
```

| 字段 | 说明 |
|---|---|
| `key` | 字段名，供 `{config:key}` 引用 |
| `label` | 后台表单里显示的名称 |
| `type` | `text` / `textarea` / `switch`（值 `1`/`0`）/ `select` / `number` / `audio`（音频上传）/ `file` |
| `default` | 未配置时的默认值 |
| `help` | 表单下方的说明文字 |
| `options` | 仅 `select` 使用，候选项数组 |

### 上传型字段（audio / file）

`type: "audio"` 会渲染成「试听 + 选择文件」的上传控件，保存后配置值即为文件的可访问地址
（形如 `/uploads/plugins/<插件名>/audio-xxxxxxxx.mp3`），可直接用于 `{config:key}`：

```json
{ "key": "music", "label": "背景音乐文件", "type": "audio",
  "default": "", "help": "支持 mp3 / m4a / aac / ogg / opus / wav / flac，单个文件不超过 20MB" }
```

- 文件名由服务端生成，不使用上传时的原始文件名
- 重新上传或勾选「删除」会自动清理旧文件
- 配置值会被清除引号与尖括号等危险字符 —— 因为它会被原样注入页面

### 在钩子里引用配置

任意字符串字段都支持 `{config:字段名}` 占位符，运行时替换为管理员填写的值：

```json
{ "hook": "compose_guard", "type": "guard",
  "nicknames": "{config:nicknames}",
  "label": "{config:badge}" }
```

```json
{ "hook": "footer_html", "type": "html",
  "html": "<audio src=\"{config:music}\" autoplay loop></audio>" }
```

配置值保存在 `settings` 表（键名 `plugin.<插件名>.<字段名>`），随站点一起备份。

---

## 4. 钩子一览

`hooks` 是一个数组，每个元素至少包含 `hook` 与 `type`：

| type | hook | 作用 | 关键字段 |
|---|---|---|---|
| `html` | `header_html` | 前台页面 `<head>` 后（body 起始处）注入 HTML | `html` |
| `html` | `footer_html` | 前台页面页脚注入 HTML | `html` |
| `html` | `admin_plugins_top` | 后台「插件管理」页顶部注入 HTML | `html` |
| `filter` | `content_filter` | 用正则改写帖子 / 评论内容 | `match`、`replace` |
| `http` | `post_created` | 发帖成功后回调 Webhook | `url` |
| `http` | `comment_created` | 评论后回调 Webhook | `url` |
| `http` | `report_created` | 举报后回调 Webhook | `url` |
| `guard` | `compose_guard` | **发帖前置守卫**：命中昵称时强制验证后台账号 | `nicknames`、`require`、`label`、`message` |
| `badge` | `post_badge` | 给指定昵称的帖子打上标识 | `nicknames`、`label` |

> 内核遇到**不认识的钩子类型会跳过该钩子而不报错**，详见第 8 节"能力边界"。

### 4.1 html：注入片段

```json
{ "hook": "footer_html", "type": "html",
  "html": "<div class='daily-quote'>今天也要加油鸭</div>" }
```

注入内容会被原样输出到页面，因此**可以包含 `<style>` 与 `<script>`** —— 这也是插件实现
自定义交互（弹窗、动效、统计）的主要方式。请勿注入恶意代码。

三个注入点的位置：

| hook | 渲染位置 | 适用 |
|---|---|---|
| `header_html` | `layout.html` 中 `<body>` 起始处 | 顶部公告条、全局横幅 |
| `footer_html` | `layout.html` 页脚区域（脚本之前） | 悬浮按钮、播放器、统计脚本 |
| `admin_plugins_top` | 后台插件管理页顶部 | 插件自检提示、批量操作入口 |

> 需要更细的注入点（`head.end`、`body.end`、`post.detail.after`、`admin.sidebar.after` 等 14 处）？
> 这些是**应用型插件**的 Slot（`clv.slot`），见 [PLUGIN-APP.md](PLUGIN-APP.md) 第 9.1 节。
> 应用型插件注册同名 `footer_html` / `header_html` 时，输出会与 A 型片段拼接在一起。
>
> 注入到页面的脚本（不管是 A 型还是 B 型）都可以使用内核前端 API —— `CLV.api` 发请求、
> `CLV.hooks.cardHtml` 给首页卡片追加内容等，见 [PLUGIN-APP.md](PLUGIN-APP.md) 第 9.7 节。

### 4.2 片段较长时用 `@文件`

在 JSON 里转义大段 HTML / JS 很痛苦。把片段放在插件目录下的独立文件里，
`html` 字段写成 `@文件名` 即可，安装（zip）时会随插件一起落盘：

```json
{ "hook": "footer_html", "type": "html", "html": "@player.html" }
```

```
插件 zip 根目录/
├── plugin.json
└── player.html      ← 与 plugin.json 同级，整段 HTML/CSS/JS 原样放在这里
```

- 只读取插件目录下的**直接文件名**，`@../xxx` 这类路径会被无视
- 片段文件缺失时该钩子被跳过（不会把 `@player.html` 原样输出到页面）
- 官方「背景音乐」插件就是这么写的，可直接参考 `plugins/bgm/`

### 4.3 filter：内容正则改写

```json
{ "hook": "content_filter", "type": "filter", "match": "广告|加群|代刷", "replace": "***" }
```

- `match` 是 Go 正则表达式；`replace` 支持 `$1` 捕获组引用
- 对**表单发帖、API 发帖、评论**三类内容生效（均在 `StripHTML` 之后执行）
- 规则有 30 秒缓存；插件启用后会自动热加载
- 多个插件的规则按顺序依次应用
- 应用型插件的 `filter.content` 会在此基础上继续链式处理

### 4.4 http：事件 Webhook

```json
{ "hook": "post_created", "type": "http", "url": "https://example.com/hook" }
```

回调以 `POST` + JSON 发送，请求头带 `X-ClearLove-Event`，超时 10 秒，
**失败只记录日志、不影响发帖**。回调体包含事件名、时间与业务字段：

```json
{ "event": "post_created", "post_id": 12, "nickname": "匿名", "content": "...", "time": "2026-09-12T15:00:00Z" }
```

| 事件 | 触发时机 | 载荷字段 |
|---|---|---|
| `post_created` | 发帖成功（表单 / API） | `post_id`、`nickname`、`content` |
| `comment_created` | 评论成功 | `post_id`、`content` |
| `report_created` | 提交举报 | `post_id`、`reason` |

> 需要订阅更多事件（点赞、注册、登录、改帖、删帖…）或做进程内处理？用应用型插件的 `clv.on`。

---

## 5. guard：发帖守卫（需要验证后台账号）

`compose_guard` 用于**防止他人冒充官方身份**：当发帖昵称命中名单时，
表白墙会在提交前弹出「管理员身份验证」弹窗，要求输入后台管理员账号与密码，
**服务端校验通过后才会发布**（不通过直接拒绝，无法绕过），并可给帖子打上标识。

| 字段 | 说明 |
|---|---|
| `nicknames` | 触发的昵称名单，逗号 / 分号 / 换行分隔，支持 `{config:key}` |
| `require` | `admin` = 必须验证后台管理员账号密码；留空表示仅打标识、不校验 |
| `label` | 校验通过后写入帖子的标识文字（前台显示为昵称后的彩色徽章） |
| `message` | 校验失败时返回给用户的提示语 |

```json
{
  "hook": "compose_guard",
  "type": "guard",
  "nicknames": "官方,公告板",
  "require": "admin",
  "label": "官方",
  "message": "该昵称为官方专享，请验证管理员账号后发布"
}
```

行为说明：

- 前台发帖页会在昵称命中时**自动弹出验证弹窗**（昵称名单由插件配置实时提供）
- 表单发帖（`POST /compose`）与 API 发帖（`POST /api/v1/posts`）都会强制校验
- API 发帖需在 JSON 中额外提供 `admin_user` 与 `admin_pass`
- 同一 IP 连续失败 5 次会被锁定 10 分钟，防止暴力破解后台密码
- 多个插件同时命中时，标识会合并、校验要求取并集
- 通过验证的帖子会同时带上内置的「**管理员**」标识，插件自定义的 `label` 追加在后面，例如 `["管理员", "官方认证"]`

### badge：帖子标识（无需验证）

只想给某些昵称的帖子加个标记时使用，不涉及验证：

```json
{ "hook": "post_badge", "type": "badge", "nicknames": "小编,编辑", "label": "编辑" }
```

标识在发帖时写入帖子记录，**不随插件卸载而消失**（已发布的历史帖子仍保留标识）。

---

## 6. 完整示例：官方身份守卫插件

`plugin.json`：

```json
{
  "name": "官方身份守卫",
  "version": "1.0.0",
  "author": "yourname",
  "description": "使用指定昵称发帖时必须验证后台管理员账号，帖子显示自定义标识，防止冒充官方身份",
  "requires": "2.0.0",
  "config": [
    { "key": "nicknames", "label": "专享昵称", "type": "textarea", "default": "官方,公告",
      "help": "使用这些昵称发帖时需验证后台账号，多个用逗号或换行分隔" },
    { "key": "badge", "label": "帖子标识", "type": "text", "default": "官方",
      "help": "验证通过的帖子在昵称后显示的徽章文字" },
    { "key": "message", "label": "失败提示", "type": "text",
      "default": "该昵称为官方专享，请验证管理员账号后发布" }
  ],
  "hooks": [
    { "hook": "compose_guard", "type": "guard",
      "nicknames": "{config:nicknames}",
      "require": "admin",
      "label": "{config:badge}",
      "message": "{config:message}" },
    { "hook": "footer_html", "type": "html",
      "html": "<style>.post-badge{box-shadow:0 1px 6px rgba(255,123,169,.45)}</style>" }
  ]
}
```

打包与安装（三平台打包命令见 [PLUGIN.md](PLUGIN.md) 2.0 节）：

```bash
cd your-plugin
zip -r official-guard.zip .            # macOS / Linux
tar -a -c -f official-guard.zip *      # Windows 10/11 自带命令
```

后台「插件管理 → 上传并安装」选择 zip → 点「启用」→ 在「插件设置」里填写专享昵称
→ 用该昵称发帖，验证弹出管理员验证弹窗、通过后帖子带上标识。

> 表白墙内置了「管理员专享昵称」（网站设置），覆盖最常见的官方身份保护场景；
> 这个插件演示的是如何用插件实现同类能力，并支持自定义标识文字与多插件组合。

### 6.1 另一个完整例子：每日一言（注入 + 配置 + 前端 JS）

只需两个文件，演示 `footer_html` 注入、`{config:key}` 引用与注入脚本中的原生 JS：

`plugin.json`：

```json
{
  "name": "每日一言",
  "version": "1.0.0",
  "author": "yourname",
  "description": "在每个前台页面底部随机展示一句语录，语录可在后台配置",
  "config": [
    { "key": "quotes", "label": "语录列表", "type": "textarea",
      "default": "今天也要加油鸭\n慢慢来，比较快\n你笑起来真好看",
      "help": "每行一句，访客每次刷新随机显示其中一条" }
  ],
  "hooks": [
    { "hook": "footer_html", "type": "html", "html": "@quote.html" }
  ]
}
```

`quote.html`（与 plugin.json 同级，避免在 JSON 里转义大段内容，见 4.2 节）：

```html
<script type="text/plain" id="clv-quotes">{config:quotes}</script>
<div id="clv-quote" style="text-align:center;padding:10px;font-size:13px;opacity:.75"></div>
<script>
(function () {
  var raw = document.getElementById("clv-quotes").textContent;
  var list = raw.split(/\r?\n/).map(function (s) { return s.trim(); }).filter(Boolean);
  var el = document.getElementById("clv-quote");
  if (el && list.length) el.textContent = list[Math.floor(Math.random() * list.length)];
})();
</script>
```

说明：

- `{config:quotes}` 在**服务端**就替换为管理员填写的文本（`@文件` 片段同样支持占位符展开）
- 多行文本放进 `type="text/plain"` 的脚本块而不是 JS 字符串字面量——字符串里出现真实换行会导致语法错误，这是注入多行配置的推荐写法
- 注入内容原样输出到页面，可以写 `<style>` / `<script>`；请只注入可信内容
- 打包上传 → 启用 → 刷新前台首页，页脚出现随机语录；后台「插件设置」改语录即时生效

---

## 7. 能力边界：什么时候该换应用型插件

声明式插件的钩子是**固定白名单**，它只能"往已有位置注入内容"，不能新增功能载体：

| 你想做 | A 型能做吗 | 应该用什么 |
|---|---|---|
| 往页面注入 HTML/JS/CSS | ✅ | `html` 钩子 |
| 正则替换敏感词 | ✅ | `content_filter` |
| 事件推送到外部系统 | ✅ | `http` 钩子 |
| 给昵称加标识 / 要求验证身份 | ✅ | `guard` / `badge` |
| 新增一个页面（如 /x/signin/） | ❌ | B 型 `clv.route` |
| 存自己的数据（如表、记录） | ❌ | B 型 `clv.table` |
| 后台管理菜单 / 管理页 | ❌ | B 型 `clv.adminMenu` / `clv.adminPage` |
| 定时任务 | ❌ | B 型 `clv.job` |
| 拦截 / 改写请求与响应 | ❌ | B 型 `clv.middleware` / `clv.filter` |
| 读取或写入站点数据 | ❌ | B 型 `clv.post` / `clv.setting` 等 |

### 从 A 型迁移到 B 型

两种形态可以平滑过渡，常见对应关系：

| A 型 | B 型等价做法 |
|---|---|
| `{ "hook": "footer_html", "html": "..." }` | `clv.slot("footer_html", fn)` 返回同一段 HTML（可动态生成） |
| `{ "hook": "content_filter", "match": ..., "replace": ... }` | `clv.filter("filter.content", fn)` 用 JS 做更复杂的改写 |
| `{ "hook": "post_created", "type": "http", "url": ... }` | `clv.on("post_created", fn)` 内用 `clv.http.request`（可加签名、重试、条件判断） |
| `{ "hook": "compose_guard", ... }` | `clv.filter("filter.compose.guard", fn)` 动态决定是否需要验证 |
| `{config:key}` 占位符 | `clv.plugin.config("key")` |
| 配置项声明 `config` | 完全相同，照搬即可 |

迁移不必推倒重来：在 `plugin.json` 里加 `"kind": "app"`、`runtime` 与 `permissions`，
保留原有 `hooks` 数组，再补 `main.js` —— 内核会同时处理两者（参考 `plugins/ai-polish/`）。

---

## 8. 调试建议

| 场景 | 建议 |
|---|---|
| **插件装了但完全没反应** | ① 是否已在「插件管理」点**启用**（安装 ≠ 启用）；② 后台列表里插件名下是否出现 ⚠ —— 有提示说明正在运行的内核版本过旧，不认识插件的钩子类型，**升级二进制即可**；③ 浏览器打开页面查看源码，确认注入片段是否存在 |
| 改了 plugin.json 不生效 | A 型清单有 5 秒短缓存；`content_filter` 规则有 30 秒缓存 |
| 配置改了没生效 | 配置存在 `settings` 表并带内存缓存，保存后立即刷新，无需重启 |
| 钩子没触发 | 确认插件已启用；`guard` / `badge` 依赖昵称精确匹配（忽略大小写） |
| 前台注入的 JS 报错 | 打开浏览器控制台查看；注入内容原样输出，注意转义引号 |
| 保存 plugin.json 后插件不出现 | 确认文件为 **UTF-8 无 BOM**，可用 `jq . plugin.json` 验证 |
| 想看当前插件与配置 | 后台「插件管理」页可查看全部插件、状态、兼容性提示与配置表单 |

> 兼容性原则：插件使用了当前内核不支持的钩子类型时，**该钩子会被跳过而不是报错**，
> 因此请务必留意后台插件列表中的 ⚠ 兼容性提示。
