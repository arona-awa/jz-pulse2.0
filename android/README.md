# 姜中Pulse Android

一个只依赖 Android Framework 的 WebView 客户端，不依赖 AndroidX、Gradle 或 Google Play 服务。

## 功能

- 打开 `https://jz-wall.top`，完全复用网站的页面、CSS、JavaScript 和图片
- 网页静态资源使用 WebView 缓存
- HTML 页面和 `/api/` GET 数据额外缓存到本地 SQLite，离线时可查看上次内容
- 页面顶部显示“离线模式”提示
- App 前台每 30 秒轮询一次通知事件；App 在后台时由 JobScheduler 每 15 分钟轮询
- 通知类型：
  - 新帖发布
  - 自己的帖子被点赞
  - 自己的帖子有新评论
  - 自己的帖子收到投币
- 点击通知直接打开对应帖子
- 支持网页 `<input type="file">` 文件选择
- 通知权限适配 Android 13+

## 目录

```text
android/
├── AndroidManifest.xml
├── build.sh
├── res/
├── src/top/jzwall/pulse/
│   ├── MainActivity.java
│   ├── SiteCache.java
│   ├── SiteClient.java
│   ├── EventPoller.java
│   ├── NotificationHelper.java
│   ├── NotificationJobService.java
│   └── BootReceiver.java
└── README.md
```

## 构建

环境要求：

- JDK 17
- Android SDK Platform 36（`android.jar`）
- Android Build Tools（`aapt2`、`d8`、`apksigner`）
- `ANDROID_SDK_ROOT` 指向 SDK；默认 `/opt/android-sdk`

```bash
cd android
./build.sh
```

输出：

```text
android/jz-pulse.apk
```

脚本会生成调试签名用的 `debug.keystore`；该文件和 APK 已在 `.gitignore` 中排除。

## 安装

把 `jz-pulse.apk` 传到手机后用文件管理器安装，或在配置好 ADB 的机器上：

```bash
adb install -r jz-pulse.apk
```

首次打开请允许通知权限，并在网页中登录。未登录时只缓存公开内容，不会产生个人通知。

## 通知架构

服务端从 2.1.59 起提供：

```text
GET /api/v1/events?after=<cursor>&limit=20
GET /api/v1/events?latest=1
```

事件来源：

- 新帖：全站用户
- 点赞、评论、投币：帖子作者
- 会过滤自己对自己的操作

首次登录/切换账号时客户端只取最大游标，不补发历史通知。之后每次轮询按游标增量获取。

## 已知限制

- 不使用 FCM，因此没有 Google 服务依赖，但后台通知不是服务器即时推送；Android JobScheduler 最短周期为 15 分钟，系统省电策略可能进一步延后。
- App 被强行停止后，定时任务不会继续运行。
- 离线缓存只覆盖 HTML 和 API JSON；首次打开且从未联网时没有内容可展示。
- 离线状态不能提交帖子、评论、点赞或投币。
