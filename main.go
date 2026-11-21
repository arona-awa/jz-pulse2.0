// ClearLove 表白墙 2.0 —— 单二进制部署的表白墙 Web 应用
// 入口：加载配置 -> 连接数据库 -> 解析模板 -> 注册路由 -> 启动 HTTP 服务与 AI 巡查
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"clearlove/internal/ai"
	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/handlers"
	"clearlove/internal/middleware"
	"clearlove/internal/migrate"
	"clearlove/internal/plugin"
)

// webFS 嵌入全部模板与静态资源，保证单文件部署
//
//go:embed all:web/templates all:web/static
var webFS embed.FS

func main() {
	// 子命令：从 PHP 旧版迁移数据
	//   ./clearlove migrate /path/to/old/data.db [-uploads /path/to/old/uploads] [-dry]
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrate(os.Args[2:])
		return
	}

	config.Load()
	// 运行时目录（单二进制部署时自动创建，无需手工准备）
	if err := ensureDirs(); err != nil {
		log.Fatalf("目录初始化失败: %v\n提示：可通过环境变量 CLEARLOVE_DATA_DIR / CLEARLOVE_UPLOAD_DIR 指定可写目录", err)
	}

	if config.Cfg.Installed {
		if err := database.Connect(); err != nil {
			log.Fatalf("数据库连接失败: %v", err)
		}
		if err := database.Migrate(); err != nil {
			log.Fatalf("数据库迁移失败: %v", err)
		}
	}

	handlers.SetupTemplates(webFS)
	pluginFS, _ := fs.Sub(webFS, "web/static")
	handlers.StaticFS = pluginFS

	plugin.Boot() // 加载应用型插件（kind=app）：执行 setup() 并注册路由/事件/任务
	// 插件可能带有 templates/ 覆盖内核模板片段，需在加载完成后重建模板集
	handlers.RebuildTemplates()

	go ai.StartPatrol() // AI 自动巡查（每10分钟，仅在开启时工作）
	// 启动时对已启用的 AI 配置做一次连通性检查；失败只记录一条去重日志，不阻塞服务。
	go func() {
		if err := ai.ValidateStoredConfig(); err != nil {
			ai.LogFailure("启动校验", err)
		}
	}()

	// 监听端口：PORT 或 CLEARLOVE_PORT，默认 16868
	addr := ":16868"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	} else if p := os.Getenv("CLEARLOVE_PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("ClearLove 表白墙 2.0 | 数据目录: %s | 上传目录: %s", config.Cfg.DataDir, config.Cfg.UploadDir)
	if config.Cfg.Installed {
		log.Printf("服务已启动: http://localhost%s  (后台: /admin)", addr)
	} else {
		log.Printf("检测到未安装，请访问 http://localhost%s/install 完成安装向导", addr)
	}
	if err := http.ListenAndServe(addr, middleware.Use(buildMux())); err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}

// runMigrate 执行旧版（PHP 1.x）数据迁移，迁移前需先完成新版安装
func runMigrate(args []string) {
	config.Load()
	oldPath, uploadsDir := "", ""
	dryRun := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-uploads", "--uploads":
			if i+1 < len(args) {
				uploadsDir = args[i+1]
				i++
			}
		case "-dry", "--dry", "-dry-run":
			dryRun = true
		default:
			if oldPath == "" {
				oldPath = args[i]
			}
		}
	}
	if oldPath == "" {
		log.Fatalf("用法: %s migrate <旧版数据文件> [-uploads 旧版 uploads 目录] [-dry]\n"+
			"支持旧版 SQLite 的 data.db 与 MySQL 导出的 .sql（自动识别）\n"+
			"示例: %s migrate /www/wwwroot/old/data.db -uploads /www/wwwroot/old/uploads\n"+
			"也可以在后台「数据迁移」页面上传数据库文件完成迁移",
			os.Args[0], os.Args[0])
	}
	if err := ensureDirs(); err != nil {
		log.Fatalf("目录初始化失败: %v", err)
	}
	if !config.Cfg.Installed {
		log.Fatal("新版尚未完成安装：请先启动服务并访问 /install 完成安装向导，再执行数据迁移")
	}
	if err := database.Connect(); err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	if err := database.Migrate(); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	log.Printf("开始从旧版迁移数据：%s", oldPath)
	st, err := migrate.Run(oldPath, uploadsDir, dryRun)
	if err != nil {
		log.Fatalf("旧版数据迁移失败: %v", err)
	}
	prefix := ""
	if dryRun {
		prefix = "[试运行] "
	}
	log.Printf("%s迁移结果：帖子 %d、点赞补齐 %d、媒体 %d、评论 %d、话题 %d、公告 %d、用户 %d、管理员 %d、封禁 %d、举报 %d",
		prefix, st.Posts, st.Likes, st.Media, st.Comments, st.Topics, st.Notices,
		st.Users, st.Admins, st.Bans, st.Reports)
	if st.Skipped > 0 || st.Missing > 0 || st.NoPostRef > 0 {
		log.Printf("%s其中：跳过已存在记录 %d 条、媒体文件缺失 %d 个、关联帖子不存在的记录 %d 条",
			prefix, st.Skipped, st.Missing, st.NoPostRef)
	}
	if dryRun {
		log.Print("这是试运行，未写入任何数据；去掉 -dry 参数即可正式执行")
	} else {
		log.Print("迁移完成，重启服务后即可在新版看到数据")
	}
}

// ensureDirs 创建并校验运行时目录可写性（提前失败，避免运行中出错）
func ensureDirs() error {
	dirs := []string{
		config.Cfg.DataDir,
		config.Cfg.UploadDir,
		filepath.Join(config.Cfg.DataDir, "themes"),
		filepath.Join(config.Cfg.DataDir, "plugins"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		probe := filepath.Join(d, ".write_test")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			return fmt.Errorf("%s 不可写: %w", d, err)
		}
		_ = os.Remove(probe)
	}
	return nil
}

// buildMux 注册全部路由（Go 1.22+ 增强路由语法：方法 + 路径参数）
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// 静态资源与上传文件
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(handlers.StaticFS))))
	mux.HandleFunc("GET /favicon.svg", handlers.Favicon)
	mux.HandleFunc("GET /favicon.ico", handlers.FaviconICO)
	mux.HandleFunc("GET /theme.css", handlers.ThemeCSS)
	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(config.Cfg.UploadDir))))

	// 安装向导
	mux.HandleFunc("GET /install", handlers.InstallPage)
	mux.HandleFunc("POST /install", handlers.InstallSubmit)

	// 前台
	mux.HandleFunc("GET /{$}", handlers.Index)
	mux.HandleFunc("GET /post/{id}", handlers.PostDetail)
	mux.HandleFunc("GET /post/{id}/edit", handlers.PostEdit)
	mux.HandleFunc("POST /post/{id}/edit", handlers.PostEditSave)
	mux.HandleFunc("POST /post/{id}/delete", handlers.PostDelete)
	mux.HandleFunc("GET /compose", handlers.ComposePage)
	mux.HandleFunc("POST /compose", handlers.ComposeSubmit)
	mux.HandleFunc("GET /login", handlers.LoginPage)
	mux.HandleFunc("POST /login", handlers.LoginSubmit)
	mux.HandleFunc("GET /register", handlers.RegisterPage)
	mux.HandleFunc("POST /register", handlers.RegisterSubmit)
	mux.HandleFunc("POST /api/register-code", handlers.RegisterCode)
	mux.HandleFunc("GET /logout", handlers.Logout)
	mux.HandleFunc("GET /u/{id}", handlers.Profile)
	mux.HandleFunc("GET /about", handlers.About)
	mux.HandleFunc("GET /terms", handlers.TermsPage)
	mux.HandleFunc("GET /privacy", handlers.PrivacyPage)
	mux.HandleFunc("GET /manual", handlers.ManualPage)
	mux.HandleFunc("GET /chat", handlers.ChatPage)
	mux.HandleFunc("POST /chat/send", handlers.ChatSend)
	mux.HandleFunc("GET /food", handlers.FoodList)
	mux.HandleFunc("GET /food/new", handlers.FoodNewPage)
	mux.HandleFunc("POST /food/new", handlers.FoodCreate)
	mux.HandleFunc("GET /food/{id}", handlers.FoodDetail)
	mux.HandleFunc("POST /food/{id}/review", handlers.FoodReviewSubmit)
	mux.HandleFunc("POST /api/food/{id}/rate", handlers.FoodRate)
	mux.HandleFunc("POST /profile/avatar", handlers.AvatarUpload)

	// 通用 API（会话或 API Key 鉴权）
	mux.HandleFunc("GET /api/captcha", handlers.CaptchaJSON)
	mux.HandleFunc("GET /api/chat/messages", handlers.APIChatMessages)
	mux.HandleFunc("GET /api/prefs", handlers.APIPrefsGet)
	mux.HandleFunc("POST /api/prefs", handlers.APIPrefsSave)
	mux.HandleFunc("GET /api/card", handlers.APICardGet)
	mux.HandleFunc("POST /api/card", handlers.APICardSave)
	mux.HandleFunc("POST /api/card/cover", handlers.APICardCover)
	mux.HandleFunc("POST /api/card/cover/remove", handlers.APICardCoverDelete)
	mux.HandleFunc("GET /api/v1/posts", handlers.APIListPosts)
	mux.HandleFunc("GET /api/v1/posts/{id}", handlers.APIGetPost)
	mux.HandleFunc("POST /api/v1/posts", handlers.APICreatePost)
	mux.HandleFunc("POST /api/v1/upload", handlers.APIUpload)
	mux.HandleFunc("POST /api/v1/posts/{id}/like", handlers.APILike)
	mux.HandleFunc("GET /api/v1/coins", handlers.APICoins)
	mux.HandleFunc("POST /api/v1/posts/{id}/tip", handlers.APITip)
	mux.HandleFunc("POST /api/v1/comments", handlers.APIComment)
	mux.HandleFunc("GET /api/v1/topics", handlers.APITopics)
	mux.HandleFunc("POST /api/v1/reports", handlers.APIReport)

	// 后台管理
	mux.HandleFunc("GET /admin/login", handlers.AdminLoginPage)
	mux.HandleFunc("POST /admin/login", handlers.AdminLoginSubmit)
	mux.HandleFunc("GET /admin/logout", handlers.AdminLogout)
	mux.HandleFunc("GET /admin", handlers.AdminDashboard)
	mux.HandleFunc("GET /admin/posts", handlers.AdminPosts)
	mux.HandleFunc("POST /admin/posts/delete", handlers.AdminPostDelete)
	mux.HandleFunc("POST /admin/posts/ban", handlers.AdminPostBan)
	mux.HandleFunc("GET /admin/settings", handlers.AdminSettings)
	mux.HandleFunc("POST /admin/settings", handlers.AdminSettingsSave)
	mux.HandleFunc("GET /admin/background", handlers.AdminBackground)
	mux.HandleFunc("POST /admin/background/upload", handlers.AdminBackgroundUpload)
	mux.HandleFunc("POST /admin/background/reset", handlers.AdminBackgroundReset)
	mux.HandleFunc("POST /admin/theme/upload", handlers.AdminThemeUpload)
	mux.HandleFunc("POST /admin/theme/switch", handlers.AdminThemeSwitch)
	mux.HandleFunc("POST /admin/theme/delete", handlers.AdminThemeDelete)
	mux.HandleFunc("GET /admin/reports", handlers.AdminReports)
	mux.HandleFunc("POST /admin/reports/handle", handlers.AdminReportHandle)
	mux.HandleFunc("POST /admin/reports/patrol", handlers.AdminPatrolStart)
	mux.HandleFunc("GET /admin/reports/patrol/status", handlers.AdminPatrolStatus)
	mux.HandleFunc("GET /admin/users", handlers.AdminUsers)
	mux.HandleFunc("POST /admin/users/status", handlers.AdminUserStatus)
	mux.HandleFunc("POST /admin/users/delete", handlers.AdminUserDelete)
	mux.HandleFunc("GET /admin/admins", handlers.AdminAdmins)
	mux.HandleFunc("POST /admin/admins/create", handlers.AdminCreate)
	mux.HandleFunc("POST /admin/admins/delete", handlers.AdminDelete)
	mux.HandleFunc("GET /admin/notices", handlers.AdminNotices)
	mux.HandleFunc("POST /admin/notices", handlers.AdminNoticesSave)
	mux.HandleFunc("GET /admin/plugins", handlers.AdminPlugins)
	mux.HandleFunc("POST /admin/plugins/upload", handlers.AdminPluginUpload)
	mux.HandleFunc("POST /admin/plugins/toggle", handlers.AdminPluginToggle)
	mux.HandleFunc("POST /admin/plugins/delete", handlers.AdminPluginDelete)
	mux.HandleFunc("POST /admin/plugins/config", handlers.AdminPluginConfig)
	mux.HandleFunc("POST /admin/plugins/reload", handlers.AdminPluginReload)
	mux.HandleFunc("GET /admin/plugin-logs", handlers.AdminPluginLogs)
	mux.HandleFunc("POST /admin/plugin-logs/clear", handlers.AdminPluginLogsClear)
	// 旧版数据迁移（上传 data.db 或 MySQL 导出文件）
	mux.HandleFunc("GET /admin/migrate", handlers.AdminMigrate)
	mux.HandleFunc("POST /admin/migrate/start", handlers.AdminMigrateStart)
	mux.HandleFunc("GET /admin/migrate/status", handlers.AdminMigrateStatus)
	// 插件商店（对接云端插件社区）
	mux.HandleFunc("GET /admin/store", handlers.AdminStore)
	mux.HandleFunc("POST /admin/store/install", handlers.AdminStoreInstall)
	mux.HandleFunc("GET /admin/apikeys", handlers.AdminAPIKeys)
	mux.HandleFunc("POST /admin/apikeys/create", handlers.AdminAPIKeyCreate)
	mux.HandleFunc("POST /admin/apikeys/delete", handlers.AdminAPIKeyDelete)
	mux.HandleFunc("GET /admin/about", handlers.AdminAbout)
	mux.HandleFunc("GET /admin/update", handlers.AdminUpdate)
	mux.HandleFunc("POST /admin/update/apply", handlers.AdminUpdateApply)

	// 应用型插件（B 型）分发：前台 /x/<slug>/... 、后台 /admin/plugins/<slug>/...
	mux.Handle("/x/", plugin.AppHandler())
	mux.Handle("/admin/plugins/", plugin.AdminAppHandler())

	return mux
}
