# 主题开发文档

ClearLove 表白墙的主题机制基于「CSS 变量 + 样式覆盖」，可在后台 **网站设置 → 主题管理**
中一键切换，支持自定义背景图片，自定义程度高且无需修改任何 Go 代码。

## 1. 主题目录结构

主题以目录形式存放于 `data/themes/<主题名>/`：

```
data/themes/my-love/
├── theme.json     # 主题清单（必须）
└── style.css      # 主题样式（必须）
```

后台「主题安装」处直接上传 zip 包即可（zip 内需包含 `theme.json` 与 `style.css`）。

## 2. theme.json 清单

```json
{
  "name": "my-love",
  "version": "1.0.0",
  "author": "你的名字",
  "description": "粉色梦幻主题"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| name | 是 | 主题名（将作为安装目录名，建议英文/数字/短横线） |
| version | 否 | 版本号 |
| author | 否 | 作者 |
| description | 否 | 描述 |

## 3. style.css 覆盖方式

主题 CSS 会在 `/theme.css` 中输出，并在 `/static/app.css` 之后加载，因此可以直接覆盖任意样式。
推荐先通过 CSS 变量整体换肤：

```css
:root {
  --clv-accent: #6ea8ff;          /* 主色（按钮、悬停、点赞） */
  --clv-accent-2: #7ee0c3;        /* 辅助色（渐变另一端） */
  --clv-accent-soft: rgba(110,168,255,.12);
  --clv-bg: #f2f7ff;              /* 页面背景色 */
  --clv-bg-image: url('/uploads/2026/bg.webp');  /* 页面背景图 */
  --clv-card: #ffffff;            /* 卡片背景 */
  --clv-text: #1f2733;
  --clv-muted: #8a93a3;
  --clv-border: #e6ecf5;
  --clv-radius: 18px;             /* 卡片圆角 */
  --clv-radius-sm: 10px;
  --clv-shadow: 0 4px 20px rgba(31,39,51,.08);
  --clv-font: "PingFang SC", "Microsoft YaHei", sans-serif;
}
```

也可直接针对选择器做深度定制，例如：

```css
/* 顶部导航透明磨砂 */
.topbar { background: rgba(255,255,255,.7); }

/* 卡片悬浮上浮动效 */
.post-card:hover { transform: translateY(-6px); box-shadow: 0 12px 30px rgba(0,0,0,.12); }

/* 隐藏悬浮发帖按钮、改用固定底栏时 */
.fab { display: none; }
```

## 4. 可用选择器速查

| 选择器 | 说明 |
|---|---|
| `.topbar / .brand / .topnav` | 顶部导航 |
| `.masonry / .post-card / .card-head` | 瀑布流与卡片 |
| `.content / .media-grid / .video` | 帖子内容与媒体 |
| `.card-actions / .act / .like-btn` | 点赞与评论按钮 |
| `.preview-comments / .view-all` | 卡片内评论预览 |
| `.detail-card / .comments / .comment-item` | 详情页 |
| `.compose / .auth / .progress` | 表单与上传进度 |
| `.fab / .notice-bar / .site-footer` | 悬浮按钮、公告与页脚 |
| `.admin-body / .sidebar / .table / .stat` | 后台界面 |

## 5. 自定义背景图片

两种方式：

1. 后台 **网站设置 → 主题管理 → 自定义背景图地址** 填入图片地址（可上传后使用 `/uploads/...` 路径），
   会生成 `--clv-bg-image` 变量并应用于全站。
2. 在主题 `style.css` 中自行定义 `--clv-bg-image` 或直接写 `body { background-image: ... }`。

## 6. 注意事项

- 主题 CSS 中禁止引用国外 CDN（如 fonts.googleapis.com），如需字体请内联或使用系统字体。
- 图标请使用内联 SVG（可参考 `web/static/app.js` 中的写法）。
- 后台「删除主题」仅能删除 `data/themes` 下的自定义主题，内置 `default` 主题不可删除。
