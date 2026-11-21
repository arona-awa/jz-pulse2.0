<div align="center">

<img src="web/static/dina-jz-logo.jpg" alt="dina-jz" width="120" />

# 姜中Pulse校园墙 2.0

**jz-pulse2.0** · 面向校园社区的轻量级内容与互动站点

单二进制部署 · SQLite / MySQL · 模板与静态资源内嵌 · 插件扩展

</div>

---

## 项目简介

本项目是基于 [ClearLove 2.0](https://git.kazx.top/fqh/Clearlove2.0)（MIT）二次开发的校园墙定制版，包含发帖、图片、评论、点赞、聊天室、必吃榜、等级经验、投币、邮件通知、开屏画面和文档页面等功能。

项目使用 Go 编写，编译后为单个可执行文件，模板、CSS、JS 和图片通过 `embed` 内嵌，运行时不需要 Node.js、PHP 或额外的静态文件服务器。

## 功能

- 瀑布流帖子：文字、图片、视频、话题、匿名昵称
- 评论、点赞、举报、内容审核
- 登录用户公共聊天室：文字、图片、自动拉取新消息
- 必吃榜：店铺、评分、评价
- 等级徽章与经验进度：Lv1–Lv6
- 硬币投币：每日硬币、作者收益、帖子投币数、投币特效
- 邮件通知：新帖提醒注册用户，SMTP 可配置
- 开屏画面、用户协议、隐私政策、使用手册
- 可视化后台：内容、用户、评论、公告、设置、插件、API Key、数据迁移
- 插件系统：声明式 / 应用型插件，支持模板覆盖
- Android 客户端：复用网站页面与素材，支持本地缓存、离线查看和动态通知（新帖、点赞、评论、投币）

## 技术栈

- Go 1.27.4+（包含 stdlib 安全修复）
- `html/template` + `embed`
- SQLite（默认）或 MySQL
- 原生 JavaScript / CSS，无前端构建步骤
- CGO 关闭即可编译

## 快速开始

```bash
# 1. 编译
go build -trimpath -ldflags="-s -w" -o jz-pulse .

# 2. 启动（首次访问 /install 完成安装向导）
./jz-pulse
```

默认监听端口 `16868`，可通过环境变量覆盖：

```bash
PORT=16868 \
CLEARLOVE_DATA_DIR=./data \
CLEARLOVE_UPLOAD_DIR=./uploads \
./jz-pulse
```

启动后访问：

- 前台：<http://localhost:16868/>
- 安装向导：<http://localhost:16868/install>
- 后台：<http://localhost:16868/admin>

首次安装会要求设置管理员账号、站点名称和数据库。项目不包含任何默认管理员密码。

## 构建与测试

```bash
# 运行测试
go test ./...

# 交叉编译 Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o jz-pulse-linux-amd64 .
```

## 目录结构

```text
.
├── main.go                 # 入口、路由、安装与启动
├── internal/
│   ├── handlers/           # 前台、后台、API、安装、文档页面
│   ├── models/             # 用户、等级、硬币、设置等模型
│   ├── database/           # 建表、迁移与 SQLite/MySQL 适配
│   ├── middleware/         # CSRF、会话、API Key、访问日志
│   ├── mailer/ notify/     # SMTP 与新帖邮件通知
│   ├── plugin/             # 插件运行时
│   └── ...
├── web/
│   ├── templates/          # 页面模板（embed）
│   └── static/             # CSS/JS/图片/图标（embed）
├── plugins/                # 插件示例源码
├── docs/                   # 插件与主题开发文档
├── go.mod
└── LICENSE
```

## 配置与数据

- 配置保存在 `data/config.json`
- SQLite 数据库默认在 `data/clearlove.db`
- 上传文件在 `uploads/`
- SMTP、AI 审核、站点域名、插件配置均在后台设置，**不写入源码**
- AI 使用 OpenAI 兼容接口。`ai_base` 只填 Base URL，例如 `https://api.deepseek.com`，不要填 `/v4` 或完整的 `/chat/completions`；当前 DeepSeek 可使用 `deepseek-flash`
- 启用中的 AI 配置在后台保存时会先做一次最小对话校验；校验失败不会写入数据库
- `ai_precheck_fail` 默认 `closed`：审核服务异常时阻止发布，避免审核静默失效

以下内容已在 `.gitignore` 中排除，不应提交到公开仓库：

```text
data/
uploads/
*.db
*.sqlite
*.log
.env
config.json
```

## 插件

`plugins/` 下保留了部分插件源码示例。将插件目录复制到运行时的 `data/plugins/`，或在后台插件页安装插件包后启用。

插件开发文档见 [docs/PLUGIN.md](docs/PLUGIN.md)、[docs/PLUGIN-APP.md](docs/PLUGIN-APP.md)、[docs/PLUGIN-V3.md](docs/PLUGIN-V3.md)。

本项目不包含线上站点使用的全部第三方/定制插件，部署者可按需安装。

## Android 客户端

`android/` 目录是一个不依赖 AndroidX/Google 服务的原生 WebView 客户端：

- 直接加载 `https://jz-wall.top`，复用网站 CSS、JS、图片和页面
- WebView 缓存静态资源；HTML 与 `/api/` JSON 另存本地 SQLite，断网时可查看缓存内容
- App 打开时每 30 秒、后台每 15 分钟轮询 `/api/v1/events`，通过系统通知提示新帖、点赞、评论和投币
- 通知点击后直接打开对应帖子
- 支持网页文件选择、返回键和离线状态提示
- Android 浏览器访问时显示“用 App 浏览更顺畅”推荐条，排除 App 自身 WebView、其他内嵌 WebView 和爬虫
- 关于页仅对 Android 网页显示“下载 Android App”按钮，iOS、桌面端和 App 内均不显示
- 站内下载地址：`/download/jz-pulse.apk`，APK 文件放在 `data/downloads/jz-pulse.apk`

构建需要 Android SDK Platform 36、Build Tools、JDK 17，并设置 `ANDROID_SDK_ROOT`：

```bash
cd android
./build.sh
```

详细说明见 [android/README.md](android/README.md)。

通知说明：当前使用系统 JobScheduler 轮询，不使用 FCM；后台通知最迟约 15 分钟到达，App 打开时约 30 秒轮询一次。若需要被系统杀死后即时推送，需要额外接入 FCM 或其他推送服务。

## 部署提示

- 生产环境建议使用 Nginx/Caddy 反向代理并启用 HTTPS；
- 使用 systemd/supervisor 守护进程；
- 定期备份 `data/` 与 `uploads/`；
- 首次部署后立即设置强管理员密码，并关闭不需要的注册/上传功能；
- 若使用 MySQL，请在安装向导中填写 DSN，不要将 DSN 提交到仓库。

### 日志轮转

如果 systemd 使用 `StandardOutput=append:<path>/server.log`，请安装仓库内的 logrotate 示例，避免日志无限增长：

```bash
install -m 644 deploy/clearlove-jyzx.logrotate /etc/logrotate.d/clearlove-jyzx
logrotate -d /etc/logrotate.d/clearlove-jyzx   # 检查配置
```

示例策略为每日轮转、单文件超过 50MB 提前轮转、保留 14 份并压缩，使用 `copytruncate` 适配 systemd 持有文件描述符。请按实际部署路径修改示例中的 `server.log` 路径。

## 开源说明

本项目为校园墙定制版本，代码以 MIT 许可证开源。上游项目为 **ClearLove 2.0**，其版权归原作者 fqh 所有，详见 [LICENSE](LICENSE)。

- 上游：<https://git.kazx.top/fqh/Clearlove2.0>
- 本仓库：<https://github.com/arona-awa/jz-pulse2.0>
- 站点：<https://jz-wall.top>

仓库中不包含真实用户数据、生产数据库、上传内容、SMTP/AI 密钥或服务器配置。
