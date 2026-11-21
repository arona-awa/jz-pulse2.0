# ClearLove 插件开发总览

ClearLove 的插件**不需要改源码、不需要重新编译**：把 zip 传到后台即可上线，停用即回退。

内核提供**两种插件形态**，能力与门槛不同，但共用同一套安装、配置、权限与安全机制。

| 对比项 | 声明式插件（A 型） | 应用型插件（B 型） |
|---|---|---|
| 一句话 | 用清单描述"注入什么" | 用脚本注册"实现什么" |
| 组成 | `plugin.json`（`hooks`） | `plugin.json` + `main.js` |
| 能力来源 | 内核预置的 5 类钩子（白名单） | `clv.*` 运行时 API（路由 / 数据表 / 后台菜单 / 任务 / 事件 / 过滤器…） |
| 能实现 | 注入 HTML、正则改写、Webhook 回调、发帖守卫、帖子标识 | 新页面、新数据表、后台管理页、定时任务、请求拦截、改写卡片与响应… |
| 做不到 | 新增页面 / 数据表 / 后台菜单 | 替换内核页面流程（可用"模板覆盖"逼近）、访问操作系统 |
| 门槛 | 会写 JSON | 会写 JavaScript |
| 权限 | 无需声明（能力即白名单） | 必须在 `permissions` 中声明，内核逐项校验 |
| 运行方式 | 无运行时，每次请求解释清单 | goja JS 引擎，每插件独立沙箱（内部串行） |
| 适合 | 换皮肤、加提示条、敏感词替换、转发通知 | 签到积分、抽奖活动、第三方登录、数据报表 |

两种形态**可以混用**：同一个插件既能写 `hooks`（静态注入，零开销），又能用脚本注册路由与任务。参考示例 `plugins/ai-polish/`。

---

## 一、怎么选

| 需求 | 选择 |
|---|---|
| 往页面塞一段 HTML / CSS / JS | A 型 |
| 正则替换敏感词、发帖时转发 Webhook | A 型 |
| 给某些昵称加标识 / 要求验证管理员身份 | A 型 |
| 需要独立页面、需要存自己的数据 | B 型 |
| 需要后台管理界面、定时任务、拦截请求 | B 型 |
| 拿不准 | **B 型**（它是 A 型的超集，还能同时写 `hooks`） |

- A 型完整指南 → [PLUGIN-DECLARATIVE.md](PLUGIN-DECLARATIVE.md)
- B 型完整指南 → [PLUGIN-APP.md](PLUGIN-APP.md)

---

## 二、插件包与安装（两种形态通用）

### 2.0 零基础上手：不碰源码，从想法到上线

开发插件**不需要 Go 环境、不需要站点源码、不需要登录服务器**，只需要：

| 需要什么 | 说明 |
|---|---|
| 一个能进后台的站点 | 建议先在本机调试：从[发布页](https://git.kazx.top/fqh/Clearlove2.0/releases)下载二进制（Windows 为 `clearlove.exe`），双击运行后访问 `http://127.0.0.1:16868/install` 完成三步安装；直接在正式站点上开发也可以，但插件会即时影响访客，请先在测试站验证 |
| 一个文本编辑器 | VS Code / Notepad++ / 记事本均可；`plugin.json` 必须保存为 **UTF-8 无 BOM**（Windows 记事本右下角确认编码为"UTF-8"而非"UTF-8 带 BOM"） |
| 后台管理员账号 | 需要「插件管理」权限（超级管理员默认拥有） |

标准开发循环（全程只用浏览器 + 编辑器）：

```
写 plugin.json（A 型到此为止；B 型再写 main.js 与页面模板）
   ↓ 打包成 zip
后台「插件管理 → 上传并安装」
   ↓ 点「启用」
前台 / 后台验证效果，出问题查「插件日志」页
   ↓ 改代码
重新打 zip → 再次上传（同名文件被覆盖）→ B 型需重新「停用 → 启用」才会加载新代码
```

把插件目录打包成 zip（任选其一）：

| 环境 | 做法 |
|---|---|
| Windows 10/11（自带命令） | 在插件目录打开终端执行 `tar -a -c -f 你的插件.zip *` |
| Windows（图形界面） | 用 7-Zip / WinRAR「压缩为 zip」；右键"发送到压缩文件夹"也可以 |
| macOS / Linux | 在插件目录执行 `zip -r ../你的插件.zip .` |

> ⚠ 旧版 PowerShell 的 `Compress-Archive` 生成的包内路径以反斜杠分隔，在 Linux 站点上**子目录（`assets/`、`templates/`）会失效**；
> zip 根目录平铺文件（只有 `plugin.json` / `main.js` / 页面模板）时不受影响。含子目录请改用 `tar -a` 或 7-Zip。

更新插件的两个细节：

- 重新上传 zip 只会**覆盖同名文件**，从新版里删掉的旧文件会残留在插件目录；正式升级建议先「卸载」再上传（B 型数据表默认保留，见 2.3）
- A 型清单有 5 秒缓存、`content_filter` 规则有 30 秒缓存，改完稍等片刻即可生效；B 型脚本载入内存运行，**必须重新启用**（或后台点「重载」）才生效

### 2.1 目录结构

```
your-plugin.zip
├── plugin.json          # 必需：插件清单（可位于 zip 内任意目录）
├── main.js              # B 型必需：脚本入口
├── assets/              # 可选：静态资源，经 /x/<slug>/assets/ 访问
├── templates/           # 可选：模板覆盖，见 PLUGIN-APP.md 第 9.6 节
└── （可选）其他文件，如 A 型的 @片段.html
```

打包要求：

- 必须是 zip 格式，`plugin.json` 必须为 **UTF-8 无 BOM**（带 BOM 会明确报错）
- 单包不超过 50MB，`plugin.json` 不超过 1MB
- 插件名（`name`）会作为安装目录名，避免 `/`、`\`

### 2.2 安装方式

| 方式 | 操作 |
|---|---|
| 后台上传 | 后台「插件管理 → 安装插件」选择 zip |
| 插件商店 | 后台「插件商店」一键安装（自动下载并校验 SHA256） |
| 手动部署 | 解压到 `data/plugins/<插件名>/` 后刷新后台插件列表 |

安装 ≠ 启用。安装后在「插件管理」点**启用**才生效：

- **A 型**：启用后立即生效，无需加载（每次请求读取清单）
- **B 型**：启用时执行 `setup()` 完成全部注册，后台状态显示「运行中」

### 2.3 卸载与数据

- 卸载（删除插件）会删除目录、从启用列表移除、**停止 B 型的任务与路由**
- **数据表默认保留**（`pl_<slug>_*`），重新安装后数据还在；如需清除请手动删表
- 已写入帖子的 `badges` 标识不会因卸载消失

### 2.4 插件清单常见字段

```json
{
  "name": "插件名（必需，同时作为目录名）",
  "version": "1.0.0",
  "author": "作者",
  "description": "一句话说明",
  "requires": "2.1.0",
  "config": [ ... ]
}
```

A 型额外要求 `hooks`；B 型额外要求 `kind: "app"`、`runtime`、`permissions`。

> `requires` 仅作提示：版本不满足时后台列表显示 ⚠，不会阻止启用。

---

## 三、配置项（两种形态通用）

在清单里声明 `config`，后台「插件管理 → 插件设置」会**自动渲染表单**，保存后立即生效，无需重启。

```json
"config": [
  { "key": "enable", "label": "启用提示", "type": "switch", "default": "1" },
  { "key": "text",   "label": "提示文案", "type": "text", "default": "欢迎" },
  { "key": "badge",  "label": "标识文字", "type": "text", "help": "显示在昵称后" },
  { "key": "pos",    "label": "显示位置", "type": "select", "default": "bottom",
    "options": ["top", "bottom"] },
  { "key": "num",    "label": "次数上限", "type": "number", "default": "3" },
  { "key": "music",  "label": "背景音乐", "type": "audio", "default": "",
    "help": "支持 mp3 / m4a / aac / ogg / opus / wav / flac，不超过 20MB" },
  { "key": "big",    "label": "长文本", "type": "textarea", "default": "" }
]
```

| 字段 | 说明 |
|---|---|
| `key` | 字段名，A 型用 `{config:key}` 引用，B 型用 `clv.plugin.config("key")` 读取 |
| `label` | 后台表单显示的名称 |
| `type` | `text` / `textarea` / `switch`（值 `1`/`0`）/ `select` / `number` / `audio` / `file` |
| `default` | 未配置时的默认值 |
| `help` | 表单下方说明文字 |
| `options` | 仅 `select` 使用 |

- 存储位置：`settings` 表的 `plugin.<插件名>.<字段名>`，随站点一起备份
- `audio` / `file` 上传型：管理员上传后，配置值即为可访问地址（`/uploads/plugins/<插件名>/xxx`），文件名由服务端生成；重新上传或勾选删除会清理旧文件
- 值会被去除 HTML 与危险字符（因为它可能被原样注入页面）

---

## 四、权限与安全（B 型重点）

B 型插件的每一项能力都要在清单里**声明权限**，运行时逐项校验；A 型不需要（只会注入内容，能力受限）。

```json
"permissions": ["db", "settings.read", "site.read"]
```

| 权限 | 授予的能力 |
|---|---|
| `db` | 数据能力：`clv.db.*`、`clv.table`（写操作仅限本插件表） |
| `db.admin` | 放开全库写（内核表也可写，谨慎） |
| `settings.read` | 读站点设置（`clv.setting.get/all`） |
| `settings.write` | 写站点设置（`clv.setting.set`） |
| `site.read` | 读帖子 / 评论 / 用户 / 统计（`clv.post.list/get`、`clv.user.*`、`clv.stats.*`） |
| `site.write` | 写站点内容（发帖 / 改帖 / 删帖 / 评论） |
| `net` | 出网请求（`clv.http.request`） |
| `net.local` | 允许访问内网 / 本机地址（如本机 Ollama） |
| `cache` | 内存缓存（`clv.cache.*`） |
| `mail` | 发邮件（`clv.mail.send`） |
| `media.write` | 保存图片 / 视频到上传目录（`clv.media.*`） |

安全机制（内核强制，插件无法绕过）：

| 机制 | 说明 |
|---|---|
| 权限校验 | 未声明的权限调用即抛异常，插件可 `try/catch` |
| 数据隔离 | 写 SQL 只能落在 `pl_<slug>_*` 表；查询结果上限 1000 行 |
| 执行超时 | 单次脚本调用默认 1500ms（`runtime.timeout_ms` 可放宽，硬上限 30s）；死循环会被中断，中断后插件仍可继续服务 |
| 失败熔断 | 连续失败 20 次自动禁用并卸载插件，避免拖垮站点 |
| 出网防护 | 默认拒绝内网 / 环回地址（SSRF），响应体默认 1MB、上限 2MB |
| 请求限额 | 插件路由读取请求体上限 1MB |
| 运行审计 | 关键动作写入 `plugin_logs` 表（后台可查） |

> 插件脚本运行在**进程内沙箱**（goja），不是操作系统级隔离。请只安装可信来源的插件。

---

## 五、数据与存储位置

| 内容 | 位置 |
|---|---|
| 插件目录 | `data/plugins/<插件名>/` |
| 配置值 | `settings` 表，键 `plugin.<插件名>.<字段名>` |
| B 型数据表 | `pl_<slug>_<逻辑名>`（`slug` 中的 `-` 转 `_`） |
| 上传素材 | `uploads/plugins/<插件名>/` |
| 运行日志 | `plugin_logs` 表（`clv.log.*`、加载/失败/任务记录） |
| 静态资源 URL | `/x/<slug>/assets/<文件>` |

`slug` 是 URL 与表名的安全标识：清单里显式写 `slug` 优先，否则由 `name` 归一化（中文名会退化为 `plugin-<hash>`，**建议显式声明 `slug`**）。

---

## 六、调试速查

| 现象 | 排查方向 |
|---|---|
| A 型插件装了没反应 | ① 是否已**启用**；② 后台列表是否有 ⚠（钩子类型不支持 / 版本不满足）；③ 浏览器查看页面源码确认注入内容是否存在 |
| B 型插件状态「未运行」 | 看 `plugin_logs` 表中的 `load_error` 记录；常见原因：`setup()` 报错、`runtime.entry` 文件缺失、语法错误 |
| B 型路由 404 | 实际前缀是 `/x/<slug>/`（后台页是 `/admin/plugins/<slug>/`）；确认插件状态为「运行中」 |
| 调用 API 报"未声明权限" | 在 `plugin.json` 的 `permissions` 中补上，然后重新启用（权限在加载时确定） |
| 改了 main.js 不生效 | B 型脚本在启用时载入内存。重新点一次「停用 → 启用」（或调用重载），无需重启服务 |
| 改了 plugin.json 不生效 | A 型清单有 5 秒短缓存，`content_filter` 有 30 秒缓存；B 型需重新启用 |
| 配置改了没生效 | 配置读取带内存缓存，保存即刷新；B 型插件读到的是最新值 |
| 插件被自动停用 | 触发熔断（连续 20 次失败），去 `plugin_logs` 查失败原因 |
| 保存 plugin.json 报解析失败 | 必须 UTF-8 **无 BOM**；可用 `jq . plugin.json` 验证 |
| 模板覆盖不生效 | 覆盖文件放在 `templates/` 下且与内核模板同名；启用/停用插件后会自动重载模板 |
| 想看插件运行情况 | 后台「插件管理」列表（路由/菜单/slot/任务数量、失败计数、权限）与日志页 |

---

## 七、发布到插件市场

1. 在插件社区注册开发者账号（邮箱验证）：<https://clearlove.kazx.top/dev/register>
2. 开发者后台「发布插件」上传 zip，填写分类、说明与标签
3. 管理员审核上架后，所有站点都能在后台「插件商店」一键安装
4. 之后可在开发者后台查看下载量与安装量，并上传新版本

---

## 八、深入阅读

| 文档 | 面向 | 内容 |
|---|---|---|
| [PLUGIN-DECLARATIVE.md](PLUGIN-DECLARATIVE.md) | A 型作者 | `hooks` 五类钩子的全部字段、`@文件` 片段、守卫与标识、示例解读 |
| [PLUGIN-APP.md](PLUGIN-APP.md) | B 型作者 | 清单与生命周期、注册 API、Host API 全集、Slot / 事件 / 过滤器 / 中间件清单、模板覆盖、完整示例 |
| [PLUGIN-V3.md](PLUGIN-V3.md) | 内核开发者 | 插件系统设计与实现说明（两种形态的架构决策、扩展点接入方式、路线图） |

两种形态都有**完整可复制的源码示例**内嵌在本套文档中，无需访问源码仓库：A 型见 [PLUGIN-DECLARATIVE.md](PLUGIN-DECLARATIVE.md) 第 6 节（官方身份守卫、每日一言），B 型见 [PLUGIN-APP.md](PLUGIN-APP.md) 第 10 节（每日签到，四个文件全文）。仓库 `plugins/` 目录（`bgm`、`official-guard`、`rules-gate`、`ai-polish`、`signin`）与插件市场中的现成 zip 仅供对照下载。
