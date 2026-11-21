// 安装向导：首次启动自动进入，三步完成（数据库 -> 站点信息 -> 管理员），
// 完成后写入 data/config.json 标记安装状态并自动建表。
package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

// InstallPage 渲染安装引导页
func InstallPage(w http.ResponseWriter, r *http.Request) {
	Render(w, r, "layout.html", "pg_install", map[string]any{"version": config.Version})
}

// InstallSubmit 处理安装表单：校验数据库连接、建表、写入管理员与默认设置
func InstallSubmit(w http.ResponseWriter, r *http.Request) {
	dbType := r.FormValue("db_type")
	siteName := strings.TrimSpace(r.FormValue("site_name"))
	adminUser := strings.TrimSpace(r.FormValue("admin_user"))
	adminPass := r.FormValue("admin_pass")

	if siteName == "" || len([]rune(siteName)) > 30 { // 站名必填且不超过30字
		fail(w, 400, "请填写网站名称（30字以内）")
		return
	}
	if len(adminUser) < 3 || len(adminPass) < 6 {
		fail(w, 400, "管理员用户名至少3位、密码至少6位")
		return
	}

	// 构造 DSN 并测试连接
	var dsn string
	switch dbType {
	case "mysql":
		host := strings.TrimSpace(r.FormValue("mysql_host"))
		port := strings.TrimSpace(r.FormValue("mysql_port"))
		name := strings.TrimSpace(r.FormValue("mysql_name"))
		user := strings.TrimSpace(r.FormValue("mysql_user"))
		pass := r.FormValue("mysql_pass")
		if host == "" || port == "" || name == "" || user == "" {
			fail(w, 400, "请完整填写 MySQL 连接信息")
			return
		}
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=false&loc=UTC&charset=utf8mb4", user, pass, host, port, name)
		db, err := sql.Open("mysql", dsn)
		if err != nil || db.Ping() != nil {
			fail(w, 400, "MySQL 连接失败，请检查主机、端口、库名与账号密码")
			return
		}
		db.Close()
	default:
		dbType = "sqlite"
		// SQLite 库文件放在数据目录下，跟随 CLEARLOVE_DATA_DIR 设置
		dsn = filepath.Join(config.Cfg.DataDir, "clearlove.db")
	}

	// 持久化配置
	config.Cfg.DBType = dbType
	config.Cfg.SQLitePath = dsn
	config.Cfg.MySQLDsn = dsn
	config.Cfg.Secret = util.RandomHex(32)
	config.Cfg.Version = config.Version
	if err := database.Connect(); err != nil {
		fail(w, 500, "数据库初始化失败: "+err.Error())
		return
	}
	if err := database.Migrate(); err != nil {
		fail(w, 500, "数据表创建失败: "+err.Error())
		return
	}

	// 默认设置
	defaults := map[string]string{
		"site_name": siteName, "theme": "default", "allow_register": "1",
		"require_login_post": "0", "ai_enabled": "0", "ai_autopatrol": "0",
		"ai_precheck": "0", "community_rules": "禁止发布违法违规、暴力、色情、辱骂及广告刷屏内容。",
		"ai_vision": "0", "ai_precheck_fail": "open", "ai_auto_delete": "0", "ai_auto_ban": "0",
		"update_url":      "https://clearlove.kazx.top/update",
		"donate_img":      "https://clearlove.kazx.top/%E6%8D%90%E8%B5%A0.webp",
		"enabled_plugins": "[]",
		"cloud_api":       "https://clearlove.kazx.top",
	}
	for k, v := range defaults {
		_ = models.SetSetting(k, v)
	}

	// 创建超级管理员
	if _, err := database.DB.Exec(
		"INSERT INTO admins(username,password,role,perms,created_at) VALUES(?,?,?,?,?)",
		adminUser, util.HashPassword(adminPass), "super", "[]", models.Now()); err != nil {
		fail(w, 500, "管理员创建失败: "+err.Error())
		return
	}

	// 标记安装完成并重启协程不需要：配置已生效
	config.Cfg.Installed = true
	if err := config.Save(); err != nil {
		fail(w, 500, "配置写入失败: "+err.Error())
		return
	}
	util.Log("info", "安装完成，站点: %s，数据库: %s", siteName, dbType)
	okJSON(w, map[string]any{"redirect": "/"})
}
