// 后台管理：登录、仪表盘、帖子管理（封禁）、网站设置、主题、举报与AI巡查、
// 用户、管理员（角色权限组）、公告与话题与守则、插件、API Key、关于、检查更新。
package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"clearlove/internal/ai"
	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/models"
	"clearlove/internal/plugin"
	"clearlove/internal/util"
)

// ---------- 登录 ----------

// AdminLoginPage 后台登录页
func AdminLoginPage(w http.ResponseWriter, r *http.Request) {
	if CurrentAdmin(r) != nil {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	Render(w, r, "admin_layout.html", "pg_admin_login", map[string]any{"err": r.URL.Query().Get("err")})
}

// AdminLoginSubmit 管理员登录（12 小时会话）
func AdminLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := util.ClientIP(r)
	// 与发帖守卫共用失败锁定：同一 IP 连续失败 5 次锁 10 分钟，防止后台账号被在线爆破
	if adminTryBlocked("login:" + ip) {
		Render(w, r, "admin_layout.html", "pg_admin_login", map[string]any{"err": "失败次数过多，请 10 分钟后再试"})
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	var id int64
	var hash string
	err := database.DB.QueryRow("SELECT id,password FROM admins WHERE username=?", username).Scan(&id, &hash)
	if err != nil || !util.CheckPassword(hash, password) {
		adminTryFail("login:" + ip)
		Render(w, r, "admin_layout.html", "pg_admin_login", map[string]any{"err": "用户名或密码错误"})
		return
	}
	adminTryReset("login:" + ip)
	http.SetCookie(w, &http.Cookie{
		Name: "clv_admin", Value: util.SignSession(id, 12*time.Hour), Path: "/",
		MaxAge: 12 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// AdminLogout 管理员登出
func AdminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "clv_admin", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// ---------- 仪表盘 ----------

// AdminDashboard 统计：用户数、帖子数、今日新增、近7天活跃（发帖/评论去重身份）
func AdminDashboard(w http.ResponseWriter, r *http.Request) {
	a := requireAdmin(w, r, "")
	if a == nil {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	week := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	// 近 7 天活跃：按 (user_id, ip) 组合去重（UNION 语法两种数据库通用）
	active := models.QueryInt(`
		SELECT COUNT(1) FROM (
			SELECT user_id, ip FROM posts WHERE created_at >= ?
			UNION
			SELECT user_id, ip FROM comments WHERE created_at >= ?
		) t`, week, week)
	data := map[string]any{
		"users":    models.QueryInt("SELECT COUNT(1) FROM users"),
		"posts":    models.QueryInt("SELECT COUNT(1) FROM posts"),
		"today":    models.QueryInt("SELECT COUNT(1) FROM posts WHERE created_at >= ?", today+"T00:00:00Z"),
		"active7":  active,
		"comments": models.QueryInt("SELECT COUNT(1) FROM comments"),
		"reports":  models.QueryInt("SELECT COUNT(1) FROM reports WHERE status=0"),
	}
	// 应用型插件可改写仪表盘统计（filter.admin.stats）
	if plugin.HasApps() {
		if out := plugin.ApplyFilterValue("filter.admin.stats", data, nil); out != nil {
			if m, ok := out.(map[string]any); ok {
				data = m
			}
		}
	}
	Render(w, r, "admin_layout.html", "pg_dashboard", data)
}

// ---------- 帖子管理 ----------

// AdminPosts 帖子列表（分页 + 封禁操作入口）
func AdminPosts(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPosts); a == nil {
		return
	}
	page := int(formInt64(r, "page"))
	if page < 1 {
		page = 1
	}
	const size = 20
	total := models.QueryInt("SELECT COUNT(1) FROM posts")
	rows, err := database.DB.Query(`
		SELECT p.id, IFNULL(p.nickname,'匿名'), IFNULL(p.content,''), IFNULL(p.ip,''), IFNULL(p.fingerprint,''), p.status,
		       p.like_count, p.comment_count, IFNULL(p.created_at,''), IFNULL(t.name,'')
		FROM posts p LEFT JOIN topics t ON t.id=p.topic_id
		ORDER BY p.id DESC LIMIT ? OFFSET ?`, size, (page-1)*size)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type row struct {
		ID           int64
		Nickname     string
		Content      string
		IP           string
		Fingerprint  string
		Status       int64
		LikeCount    int64
		CommentCount int64
		CreatedAt    string
		Topic        string
	}
	var posts []row
	for rows.Next() {
		var it row
		_ = rows.Scan(&it.ID, &it.Nickname, &it.Content, &it.IP, &it.Fingerprint, &it.Status,
			&it.LikeCount, &it.CommentCount, &it.CreatedAt, &it.Topic)
		posts = append(posts, it)
	}
	pages := int(total)/size + 1
	Render(w, r, "admin_layout.html", "pg_admin_posts", map[string]any{
		"posts": posts, "page": page, "pages": pages, "total": total,
	})
}

// AdminPostDelete 删除指定帖子
func AdminPostDelete(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPosts); a == nil {
		return
	}
	deletePostCascade(formInt64(r, "id"))
	if isAJAX(r) {
		okJSON(w, nil)
		return
	}
	http.Redirect(w, r, "/admin/posts", http.StatusSeeOther)
}

// AdminPostBan 按 IP / 浏览器指纹封禁发帖者
func AdminPostBan(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPosts); a == nil {
		return
	}
	id := formInt64(r, "id")
	kind := r.FormValue("btype") // ip | fingerprint | both
	var ip, fp string
	_ = database.DB.QueryRow("SELECT ip, IFNULL(fingerprint,'') FROM posts WHERE id=?", id).Scan(&ip, &fp)
	if kind == "ip" || kind == "both" {
		if ip != "" {
			addBan("ip", ip)
		}
	}
	if kind == "fingerprint" || kind == "both" {
		if fp != "" {
			addBan("fingerprint", fp)
		}
	}
	if isAJAX(r) {
		okJSON(w, nil)
		return
	}
	http.Redirect(w, r, "/admin/posts", http.StatusSeeOther)
}

func addBan(btype, value string) {
	_, _ = database.DB.Exec(
		"INSERT INTO bans(btype,bvalue,created_at) SELECT ?,?,? WHERE NOT EXISTS"+
			" (SELECT 1 FROM bans WHERE btype=? AND bvalue=?)",
		btype, value, models.Now(), btype, value)
}

// ---------- 网站设置 ----------

// AdminSettings 设置页（基本信息/主题/开关/SMTP/AI）
func AdminSettings(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	keys := []string{"site_name", "site_url", "new_post_mail", "theme", "theme_bg", "allow_register", "require_login_post",
		"smtp_host", "smtp_port", "smtp_user", "smtp_pass", "smtp_from",
		"ai_enabled", "ai_base", "ai_key", "ai_model", "ai_autopatrol", "ai_precheck",
		"ai_vision", "ai_precheck_fail", "ai_auto_delete", "ai_auto_ban",
		"update_url", "donate_img", "cloud_api", "admin_nicknames"}
	s := map[string]string{}
	for _, k := range keys {
		s[k] = models.GetSetting(k)
	}
	Render(w, r, "admin_layout.html", "pg_admin_settings", map[string]any{
		"s": s, "themes": themeList(),
	})
}

// AdminSettingsSave 保存设置表单
func AdminSettingsSave(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	textKeys := []string{"site_name", "site_url", "theme_bg", "smtp_host", "smtp_port", "smtp_user",
		"smtp_pass", "smtp_from", "ai_base", "ai_key", "ai_model", "update_url", "donate_img", "cloud_api", "admin_nicknames"}
	vals := map[string]string{}
	for _, k := range textKeys {
		// ai_key 允许清空（清空后 AI 即不可用，需重新填写）
		if v := r.FormValue(k); v != "" || k == "theme_bg" || k == "site_url" || k == "smtp_pass" || k == "ai_key" {
			vals[k] = strings.TrimSpace(v)
		}
	}
	// 应用型插件可校验/改写即将写入的设置（filter.settings.save）
	if plugin.HasApps() {
		if out := plugin.ApplyFilterValue("filter.settings.save", vals, nil); out != nil {
			if m, ok := out.(map[string]any); ok {
				vals = map[string]string{}
				for k, v := range m {
					vals[k] = strOfAny(v)
				}
			}
		}
	}
	for k, v := range vals {
		_ = models.SetSetting(k, v)
	}
	for _, k := range []string{"allow_register", "require_login_post", "new_post_mail", "ai_enabled", "ai_autopatrol", "ai_precheck",
		"ai_vision", "ai_auto_delete", "ai_auto_ban"} {
		v := "0"
		if r.FormValue(k) == "1" {
			v = "1"
		}
		_ = models.SetSetting(k, v)
	}
	// 预审失败策略：勾选=拦截发布（closed），默认放行（open）
	failMode := "open"
	if r.FormValue("ai_precheck_fail") == "closed" {
		failMode = "closed"
	}
	_ = models.SetSetting("ai_precheck_fail", failMode)
	if v := r.FormValue("theme"); v != "" {
		_ = models.SetSetting("theme", v)
	}
	if isAJAX(r) {
		okJSON(w, nil)
		return
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// ---------- 主题 ----------

// themeList 内置 default + data/themes 下的主题目录
func themeList() []map[string]any {
	out := []map[string]any{{"name": "default", "label": "默认主题"}}
	entries, err := os.ReadDir(filepath.Join(config.Cfg.DataDir, "themes"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, map[string]any{"name": e.Name(), "label": e.Name()})
		}
	}
	return out
}

// ---------- 背景 ----------

// AdminBackground 背景设置页：上传后写入 theme_bg，全站立即生效。
func AdminBackground(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	adminBackgroundPage(w, r, "")
}

// AdminBackgroundUpload 上传背景图并应用到全站
func AdminBackgroundUpload(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		adminBackgroundPage(w, r, "图片过大或上传表单无效")
		return
	}
	f, fh, err := r.FormFile("image")
	if err != nil {
		adminBackgroundPage(w, r, "请选择要上传的背景图片")
		return
	}
	defer f.Close()
	path, err := util.SaveImage(fh)
	if err != nil {
		adminBackgroundPage(w, r, "上传失败："+err.Error())
		return
	}
	old := models.GetSetting("theme_bg")
	if err := models.SetSetting("theme_bg", path); err != nil {
		util.RemoveUpload(path)
		adminBackgroundPage(w, r, "保存失败，请稍后重试")
		return
	}
	// 旧背景若是本站上传文件则清理，外链保持不变（已不再使用）
	if strings.HasPrefix(old, "/uploads/") && old != path {
		util.RemoveUpload(old)
	}
	http.Redirect(w, r, "/admin/background", http.StatusSeeOther)
}

// AdminBackgroundReset 移除当前背景
func AdminBackgroundReset(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	old := models.GetSetting("theme_bg")
	_ = models.SetSetting("theme_bg", "")
	if strings.HasPrefix(old, "/uploads/") {
		util.RemoveUpload(old)
	}
	http.Redirect(w, r, "/admin/background", http.StatusSeeOther)
}

func adminBackgroundPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	Render(w, r, "admin_layout.html", "pg_admin_background", map[string]any{
		"bg":  models.GetSetting("theme_bg"),
		"err": errMsg,
	})
}

// AdminThemeUpload 上传主题 zip（内含 theme.json 与 style.css）
func AdminThemeUpload(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "请选择主题压缩包", http.StatusBadRequest)
		return
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 50<<20))
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		http.Error(w, "无效的 zip 包", http.StatusBadRequest)
		return
	}
	// 读取 theme.json 中的 name 作为目录名
	name := ""
	for _, zf := range zr.File {
		if filepath.Base(zf.Name) == "theme.json" {
			rc, _ := zf.Open()
			b, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			var tj struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(b, &tj) == nil {
				name = tj.Name
			}
			break
		}
	}
	if name == "" {
		http.Error(w, "theme.json 缺失或缺少 name 字段", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(config.Cfg.DataDir, "themes", filepath.Base(name))
	_ = os.MkdirAll(dir, 0o755)
	for _, zf := range zr.File {
		clean := filepath.Clean(zf.Name)
		if strings.HasPrefix(clean, "..") {
			continue
		}
		dst := filepath.Join(dir, clean)
		if zf.FileInfo().IsDir() {
			_ = os.MkdirAll(dst, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(dst)
		if err == nil {
			_, _ = io.Copy(out, io.LimitReader(rc, 50<<20))
			out.Close()
		}
		rc.Close()
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// AdminThemeSwitch 切换主题
func AdminThemeSwitch(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	_ = models.SetSetting("theme", filepath.Base(r.FormValue("name")))
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// AdminThemeDelete 删除自定义主题
func AdminThemeDelete(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	name := filepath.Base(r.FormValue("name"))
	// 防路径穿越：拒绝 . / .. 等非法目录名，并确保解析后仍在 themes 目录内
	if name != "default" && name != "." && name != ".." && !strings.HasPrefix(name, ".") {
		themesDir := filepath.Clean(filepath.Join(config.Cfg.DataDir, "themes"))
		target := filepath.Clean(filepath.Join(themesDir, name))
		if target != themesDir && strings.HasPrefix(target, themesDir+string(os.PathSeparator)) {
			_ = os.RemoveAll(target)
			if models.GetSetting("theme") == name {
				_ = models.SetSetting("theme", "default")
			}
		}
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// ThemeCSS 动态输出当前主题 CSS（变量 + 样式文件）
func ThemeCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	name := models.GetSetting("theme")
	if name == "" {
		name = "default"
	}
	var css string
	if name != "default" {
		if b, err := os.ReadFile(filepath.Join(config.Cfg.DataDir, "themes", name, "style.css")); err == nil {
			css = string(b)
		}
	}
	if css == "" {
		css = embeddedThemeCSS() // 主题缺失/损坏时回退默认主题
	}
	// 主题背景：后台设置的主题背景图优先
	if bg := models.GetSetting("theme_bg"); bg != "" {
		css = fmt.Sprintf(":root{--clv-bg-image:url('%s');}\n", bg) + css
	}
	// 应用型插件可追加/改写全站样式（filter.theme.css）
	css = plugin.ApplyFilterStr("filter.theme.css", css)
	_, _ = w.Write([]byte(css))
}

// embeddedThemeCSS 内置默认主题
func embeddedThemeCSS() string {
	if StaticFS == nil {
		return ""
	}
	b, err := fs.ReadFile(StaticFS, "theme-default.css")
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------- 举报管理与 AI ----------

// AdminReports 举报列表 + AI 日志 + 巡查入口
func AdminReports(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermReports); a == nil {
		return
	}
	// 注意：巡查只能由 POST /admin/reports/patrol 触发。
	// 历史版本的 ?run=1 会在每次刷新时重复触发，已移除该行为。
	type rep struct {
		ID         int64
		PostID     int64
		Reason     string
		Status     int64
		AIResult   string
		CreatedAt  string
		Content    string
		PostExists bool
	}
	rows, err := database.DB.Query(`
		SELECT r.id, r.post_id, IFNULL(r.reason,''), r.status, IFNULL(r.ai_result,''), r.created_at,
		       IFNULL(p.content,''), (p.id IS NOT NULL)
		FROM reports r LEFT JOIN posts p ON p.id=r.post_id ORDER BY r.id DESC LIMIT 200`)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	var reports []rep
	for rows.Next() {
		var it rep
		_ = rows.Scan(&it.ID, &it.PostID, &it.Reason, &it.Status, &it.AIResult, &it.CreatedAt, &it.Content, &it.PostExists)
		reports = append(reports, it)
	}
	rows.Close()
	type logRow struct {
		ID        int64
		PostID    int64
		Action    string
		Reason    string
		Target    string
		CreatedAt string
	}
	var logs []logRow
	lrows, err := database.DB.Query("SELECT id,post_id,action,IFNULL(reason,''),IFNULL(target,'post'),created_at FROM ai_logs ORDER BY id DESC LIMIT 100")
	if err == nil {
		for lrows.Next() {
			var it logRow
			_ = lrows.Scan(&it.ID, &it.PostID, &it.Action, &it.Reason, &it.Target, &it.CreatedAt)
			logs = append(logs, it)
		}
		lrows.Close()
	}
	// 非 JS 提交后的错误提示
	errMsg := ""
	switch r.URL.Query().Get("err") {
	case "noai":
		errMsg = "尚未接入 AI 模型，请先在「网站设置 → AI 模型接入」中完成配置"
	case "running":
		errMsg = "已有一轮 AI 巡查正在进行中，请等待完成后再试"
	}
	Render(w, r, "admin_layout.html", "pg_admin_reports", map[string]any{
		"reports": reports, "logs": logs,
		"aiOn":     models.GetSetting("ai_enabled") == "1",
		"patrol":   models.GetSetting("ai_autopatrol") == "1",
		"precheck": models.GetSetting("ai_precheck") == "1",
		"aiReady":  ai.Enabled(),          // AI 配置完整可用
		"progress": ai.ProgressSnapshot(), // 进入页面时若巡查已在跑，直接展示进度
		"errMsg":   errMsg,
	})
}

// AdminPatrolStart 手动启动一轮 AI 巡查（异步执行，前端轮询进度展示动画）。
// 支持两种调用方式：
//   - AJAX（前端 fetch）：返回 JSON，页面用动画展示进度
//   - 普通表单 POST（无 JS 兜底）：303 重定向回举报页，避免刷新页面重复触发
func AdminPatrolStart(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermReports); a == nil {
		return
	}
	back := func(errCode string) {
		target := "/admin/reports"
		if errCode != "" {
			target += "?err=" + errCode
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
	if !ai.Enabled() {
		if isAJAX(r) {
			fail(w, 400, "尚未接入 AI 模型，请先在「网站设置 → AI 模型接入」中完成配置")
			return
		}
		back("noai")
		return
	}
	if !ai.StartAsync() {
		if isAJAX(r) {
			fail(w, 409, "已有一轮巡查正在进行中，请稍候")
			return
		}
		back("running")
		return
	}
	if isAJAX(r) {
		okJSON(w, nil)
		return
	}
	back("")
}

// AdminPatrolStatus 巡查进度查询（前端加载动画轮询）
func AdminPatrolStatus(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermReports); a == nil {
		return
	}
	p := ai.ProgressSnapshot()
	percent := 0
	if p.Total > 0 {
		percent = p.Done * 100 / p.Total
	} else if !p.Running {
		percent = 100
	}
	okJSON(w, map[string]any{"progress": p, "percent": percent})
}

// AdminReportHandle 人工处理举报（keep/hide/delete）
func AdminReportHandle(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermReports); a == nil {
		return
	}
	id := formInt64(r, "id")
	action := r.FormValue("action")
	var postID int64
	if err := database.DB.QueryRow("SELECT post_id FROM reports WHERE id=?", id).Scan(&postID); err != nil {
		http.Error(w, "举报不存在", http.StatusNotFound)
		return
	}
	switch action {
	case "hide":
		_, _ = database.DB.Exec("UPDATE posts SET status=0 WHERE id=?", postID)
	case "delete":
		deletePostCascade(postID)
	}
	_, _ = database.DB.Exec("UPDATE reports SET status=1, ai_result=? WHERE id=?", "人工处理: "+action, id)
	_, _ = database.DB.Exec("INSERT INTO ai_logs(post_id,action,reason,created_at) VALUES(?,?,?,?)",
		postID, action, "管理员人工处理", models.Now())
	http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
}

// ---------- 用户管理 ----------

// AdminUsers 用户列表与帖子数
func AdminUsers(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermUsers); a == nil {
		return
	}
	rows, err := database.DB.Query(`
		SELECT u.id, u.username, u.email, u.status, IFNULL(u.created_at,''),
		       (SELECT COUNT(1) FROM posts p WHERE p.user_id=u.id)
		FROM users u ORDER BY u.id DESC LIMIT 500`)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type row struct {
		ID        int64
		Username  string
		Email     string
		Status    int64
		CreatedAt string
		Posts     int64
	}
	var users []row
	for rows.Next() {
		var it row
		_ = rows.Scan(&it.ID, &it.Username, &it.Email, &it.Status, &it.CreatedAt, &it.Posts)
		users = append(users, it)
	}
	Render(w, r, "admin_layout.html", "pg_admin_users", map[string]any{"users": users})
}

// AdminUserStatus 禁用/启用用户
func AdminUserStatus(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermUsers); a == nil {
		return
	}
	id := formInt64(r, "id")
	status := int64(1)
	if r.FormValue("status") == "0" {
		status = 0
	}
	_, _ = database.DB.Exec("UPDATE users SET status=? WHERE id=?", status, id)
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// AdminUserDelete 删除用户及其全部帖子
func AdminUserDelete(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermUsers); a == nil {
		return
	}
	id := formInt64(r, "id")
	avatar := models.QueryStr("SELECT IFNULL(avatar,'') FROM users WHERE id=?", id)
	rows, err := database.DB.Query("SELECT id FROM posts WHERE user_id=?", id)
	if err == nil {
		var ids []int64
		for rows.Next() {
			var pid int64
			_ = rows.Scan(&pid)
			ids = append(ids, pid)
		}
		rows.Close()
		for _, pid := range ids {
			deletePostCascade(pid)
		}
	}
	if _, err := database.DB.Exec("DELETE FROM users WHERE id=?", id); err == nil {
		util.RemoveAvatar(avatar) // 用户删除后清理头像文件
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// ---------- 管理员管理 ----------

// AdminAdmins 管理员列表
func AdminAdmins(w http.ResponseWriter, r *http.Request) {
	a := requireAdmin(w, r, models.PermAdmins)
	if a == nil {
		return
	}
	rows, err := database.DB.Query(
		"SELECT id,username,role,IFNULL(perms,'[]'),IFNULL(created_at,'') FROM admins ORDER BY id")
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type row struct {
		ID        int64
		Username  string
		Role      string
		Perms     string
		CreatedAt string
	}
	var admins []row
	for rows.Next() {
		var it row
		_ = rows.Scan(&it.ID, &it.Username, &it.Role, &it.Perms, &it.CreatedAt)
		admins = append(admins, it)
	}
	Render(w, r, "admin_layout.html", "pg_admin_admins", map[string]any{"admins": admins, "me": a.ID})
}

// AdminCreate 添加管理员（custom 角色按勾选的权限组授权）
func AdminCreate(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermAdmins); a == nil {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	role := r.FormValue("role")
	if !usernameRe.MatchString(username) || len(password) < 6 {
		http.Error(w, "用户名或密码不符合要求", http.StatusBadRequest)
		return
	}
	if role != "super" {
		role = "custom"
	}
	perms := "[]"
	if role == "custom" {
		var list []string
		for _, p := range []string{models.PermPosts, models.PermReports, models.PermUsers,
			models.PermSetting, models.PermNotice, models.PermPlugin, models.PermAPI, models.PermAdmins} {
			if r.FormValue("perm_"+p) == "1" {
				list = append(list, p)
			}
		}
		b, _ := json.Marshal(list)
		perms = string(b)
	}
	if _, err := database.DB.Exec(
		"INSERT INTO admins(username,password,role,perms,created_at) VALUES(?,?,?,?,?)",
		username, util.HashPassword(password), role, perms, models.Now()); err != nil {
		http.Error(w, "创建失败（用户名可能重复）", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

// AdminDelete 删除管理员（不可删除自己）
func AdminDelete(w http.ResponseWriter, r *http.Request) {
	a := requireAdmin(w, r, models.PermAdmins)
	if a == nil {
		return
	}
	id := formInt64(r, "id")
	if id != a.ID {
		_, _ = database.DB.Exec("DELETE FROM admins WHERE id=?", id)
	}
	http.Redirect(w, r, "/admin/admins", http.StatusSeeOther)
}

// ---------- 公告 / 话题 / 守则 ----------

// AdminNotices 公告、话题、社区守则管理页
func AdminNotices(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermNotice); a == nil {
		return
	}
	var notices []map[string]any
	rows, err := database.DB.Query("SELECT id,IFNULL(title,''),IFNULL(content,''),IFNULL(created_at,'') FROM notices ORDER BY id DESC")
	if err == nil {
		for rows.Next() {
			var id int64
			var t, c, at string
			_ = rows.Scan(&id, &t, &c, &at)
			notices = append(notices, map[string]any{"id": id, "title": t, "content": c, "created_at": at})
		}
		rows.Close()
	}
	Render(w, r, "admin_layout.html", "pg_admin_notices", map[string]any{
		"notices": notices, "topics": listTopics(),
		"rules": models.GetSetting("community_rules"),
	})
}

// AdminNoticesSave 按操作类型分发：公告/话题/守则
func AdminNoticesSave(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermNotice); a == nil {
		return
	}
	switch r.FormValue("op") {
	case "notice_create":
		title := util.StripHTML(r.FormValue("title"))
		content := util.StripHTML(r.FormValue("content"))
		if title != "" {
			_, _ = database.DB.Exec("INSERT INTO notices(title,content,created_at) VALUES(?,?,?)",
				title, content, models.Now())
		}
	case "notice_delete":
		_, _ = database.DB.Exec("DELETE FROM notices WHERE id=?", formInt64(r, "id"))
	case "topic_create":
		if name := util.StripHTML(r.FormValue("name")); name != "" && len([]rune(name)) <= 30 {
			ensureTopic(name)
		}
	case "topic_delete":
		_, _ = database.DB.Exec("DELETE FROM topics WHERE id=?", formInt64(r, "id"))
	case "rules_save":
		_ = models.SetSetting("community_rules", util.StripHTML(r.FormValue("rules")))
	}
	http.Redirect(w, r, "/admin/notices", http.StatusSeeOther)
}

// ---------- 插件管理 ----------

// AdminPlugins 插件列表（含各插件声明的配置表单）
func AdminPlugins(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	list := plugin.List()
	configs := map[string]map[string]string{}
	infos := map[string]plugin.AppInfo{}
	for i := range list {
		if len(list[i].Config) > 0 {
			p := list[i].Plugin
			configs[p.Name] = plugin.GetConfig(&p)
		}
		if list[i].App {
			infos[list[i].Name] = plugin.AppInfoOf(list[i].Name)
		}
	}
	Render(w, r, "admin_layout.html", "pg_admin_plugins", map[string]any{
		"plugins": list,
		"configs": configs,
		"infos":   infos,
		// 插件可通过 {"hook":"admin_plugins_top","type":"html"} 往本页顶部注入内容
		"adminTop": template.HTML(plugin.CallHTML("admin_plugins_top")),
	})
}

// AdminPluginReload 手动重载应用型插件运行时（修改脚本后无需重启站点）
func AdminPluginReload(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	name := filepath.Base(r.FormValue("name"))
	if err := plugin.ReloadApp(name); err != nil {
		util.Log("error", "插件 %s 重载失败: %v", name, err)
	}
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// AdminPluginLogs 插件运行日志页
func AdminPluginLogs(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	Render(w, r, "admin_layout.html", "pg_admin_plugin_logs", map[string]any{
		"logs": plugin.PluginLogs(slug, 200),
		"slug": slug,
	})
}

// AdminPluginLogsClear 清空插件运行日志
func AdminPluginLogsClear(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	if err := plugin.ClearPluginLogs(); err != nil {
		util.Log("error", "清空插件日志失败: %v", err)
	}
	http.Redirect(w, r, "/admin/plugin-logs", http.StatusSeeOther)
}

// AdminPluginConfig 保存插件配置（仅接受该插件声明过的字段）
func AdminPluginConfig(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	// 音频上传会把请求变成 multipart，这里限制总大小并显式解析
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "表单提交失败：文件超过 20MB 或格式错误", http.StatusBadRequest)
		return
	}
	name := filepath.Base(r.FormValue("plugin"))
	p, ok := plugin.Load()[name]
	if !ok {
		http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
		return
	}
	old := plugin.ConfigOf(name)
	values := map[string]string{}
	for _, f := range p.Config {
		if f.Key == "" {
			continue
		}
		switch f.Type {
		case "switch":
			values[f.Key] = "0"
			if r.FormValue("cfg_"+f.Key) == "1" {
				values[f.Key] = "1"
			}
		case "audio", "file":
			// 优先用新上传的文件，其次是"删除"，都没有则保留原值
			var fh *multipart.FileHeader
			if r.MultipartForm != nil {
				if list := r.MultipartForm.File["cfg_"+f.Key+"__file"]; len(list) > 0 {
					fh = list[0]
				}
			}
			switch {
			case fh != nil && fh.Size > 0:
				url, err := savePluginAsset(name, fh)
				if err != nil {
					http.Error(w, "上传失败："+err.Error(), http.StatusBadRequest)
					return
				}
				removePluginAsset(old[f.Key])
				values[f.Key] = url
			case r.FormValue("cfg_"+f.Key+"__clear") == "1":
				removePluginAsset(old[f.Key])
				values[f.Key] = ""
			default:
				values[f.Key] = sanitizePluginAsset(r.FormValue("cfg_" + f.Key))
			}
		default:
			values[f.Key] = r.FormValue("cfg_" + f.Key)
		}
	}
	if err := plugin.SaveConfig(p, values); err != nil {
		http.Error(w, "保存失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	util.Log("info", "插件 %s 配置已更新", name)
	// 应用型插件：配置变更后重载，确保 setup() 读到新配置
	if p, ok := plugin.Load()[name]; ok && p.IsApp() {
		if err := plugin.ReloadApp(name); err != nil {
			util.Log("error", "应用型插件 %s 重载失败: %v", name, err)
		}
	}
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// pluginAssetExts 插件素材允许的音频扩展名
var pluginAssetExts = map[string]bool{
	".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".oga": true,
	".opus": true, ".wav": true, ".flac": true, ".webm": true,
}

// savePluginAsset 保存插件上传的素材文件，返回可直接访问的 URL。
// 文件名由服务端生成，不使用用户提交的文件名，避免路径与注入问题。
func savePluginAsset(pluginName string, fh *multipart.FileHeader) (string, error) {
	if fh.Size > 20<<20 {
		return "", fmt.Errorf("文件不能超过 20MB")
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !pluginAssetExts[ext] {
		return "", fmt.Errorf("不支持的音频格式（支持 mp3 / m4a / aac / ogg / opus / wav / flac）")
	}
	sub := filepath.Base(pluginName)
	dir := filepath.Join(config.Cfg.UploadDir, "plugins", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	name := "audio-" + util.RandomHex(8) + ext
	dst, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, io.LimitReader(src, 20<<20)); err != nil {
		return "", err
	}
	return "/uploads/plugins/" + sub + "/" + name, nil
}

// removePluginAsset 删除本插件此前上传的素材（重新上传或清除时调用）。
// 只处理 /uploads/plugins/ 下的路径，杜绝借配置值删除其他文件。
func removePluginAsset(url string) {
	const prefix = "/uploads/plugins/"
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, prefix) || strings.Contains(url, "..") {
		return
	}
	// /uploads/plugins/bgm/x.mp3 -> <UploadDir>/plugins/bgm/x.mp3
	path := filepath.Join(config.Cfg.UploadDir, "plugins",
		filepath.FromSlash(strings.TrimPrefix(url, prefix)))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		util.Log("warn", "清理旧素材失败 %s: %v", path, err)
	}
}

// sanitizePluginAsset 清理素材地址中的危险字符。
// 该值会被插件以 {config:key} 原样注入页面，必须保证不含引号与尖括号。
func sanitizePluginAsset(v string) string {
	v = strings.TrimSpace(util.StripHTML(v))
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\'', '<', '>', '\\', '`':
			return -1
		}
		return r
	}, v)
}

// AdminPluginUpload 上传安装插件 zip
func AdminPluginUpload(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "请选择插件压缩包", http.StatusBadRequest)
		return
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 50<<20))
	if err := plugin.Install("upload.zip", data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// AdminPluginToggle 启用/禁用插件。
// 应用型插件（kind=app）在启用时加载运行时、禁用时卸载；
// 声明式插件无需加载，按请求即时解析。
func AdminPluginToggle(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	name := filepath.Base(r.FormValue("name"))
	on := r.FormValue("on") == "1"
	plugin.SetEnabled(name, on)
	if on {
		if p, ok := plugin.Load()[name]; ok && p.IsApp() {
			if err := plugin.LoadApp(name); err != nil {
				util.Log("error", "应用型插件 %s 加载失败: %v", name, err)
			}
		}
	} else {
		plugin.UnloadApp(name)
	}
	ReloadTemplates() // 插件可能带有模板覆盖
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// AdminPluginDelete 卸载插件
func AdminPluginDelete(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermPlugin); a == nil {
		return
	}
	name := filepath.Base(r.FormValue("name"))
	plugin.UnloadApp(name) // 应用型插件先停止运行时再删除目录
	_ = plugin.Remove(name)
	ReloadTemplates()
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// ---------- API Key ----------

// AdminAPIKeys API 密钥列表
func AdminAPIKeys(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermAPI); a == nil {
		return
	}
	rows, err := database.DB.Query("SELECT id,name,api_key,status,IFNULL(last_used,''),created_at FROM apikeys ORDER BY id DESC")
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type row struct {
		ID        int64
		Name      string
		Key       string
		Status    int64
		LastUsed  string
		CreatedAt string
	}
	var keys []row
	for rows.Next() {
		var it row
		_ = rows.Scan(&it.ID, &it.Name, &it.Key, &it.Status, &it.LastUsed, &it.CreatedAt)
		keys = append(keys, it)
	}
	Render(w, r, "admin_layout.html", "pg_admin_apikeys", map[string]any{"keys": keys})
}

// AdminAPIKeyCreate 生成新密钥
func AdminAPIKeyCreate(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermAPI); a == nil {
		return
	}
	name := util.StripHTML(r.FormValue("name"))
	if name == "" {
		name = "未命名"
	}
	_, _ = database.DB.Exec("INSERT INTO apikeys(name,api_key,created_at) VALUES(?,?,?)",
		name, util.RandomHex(24), models.Now())
	http.Redirect(w, r, "/admin/apikeys", http.StatusSeeOther)
}

// AdminAPIKeyDelete 删除密钥
func AdminAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermAPI); a == nil {
		return
	}
	_, _ = database.DB.Exec("DELETE FROM apikeys WHERE id=?", formInt64(r, "id"))
	http.Redirect(w, r, "/admin/apikeys", http.StatusSeeOther)
}

// ---------- 关于与更新 ----------

// AdminAbout 关于页面
func AdminAbout(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, ""); a == nil {
		return
	}
	Render(w, r, "admin_layout.html", "pg_admin_about", nil)
}

// AdminUpdate 检查云端最新版本
func AdminUpdate(w http.ResponseWriter, r *http.Request) {
	a := requireAdmin(w, r, models.PermSetting)
	if a == nil {
		return
	}
	url := models.GetSetting("update_url")
	result := map[string]any{"current": config.Version, "url": url}
	if url != "" {
		client := &http.Client{Timeout: 10 * time.Second}
		if resp, err := client.Get(url); err == nil {
			var info struct {
				Version  string `json:"version"`
				Notes    string `json:"notes"`
				Download string `json:"download"`
				SHA256   string `json:"sha256"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info) == nil {
				result["latest"] = info.Version
				result["notes"] = info.Notes
				result["download"] = info.Download
				result["sha256"] = info.SHA256
				result["upToDate"] = info.Version == "" || info.Version <= config.Version
			}
			resp.Body.Close()
		} else {
			result["error"] = "无法连接更新服务器"
		}
	} else {
		result["error"] = "未配置更新接口地址"
	}
	Render(w, r, "admin_layout.html", "pg_admin_update", map[string]any{
		"result":        result,
		"platform":      runtime.GOOS + "/" + runtime.GOARCH,
		"canSelfUpdate": runtime.GOOS == "linux" && runtime.GOARCH == "amd64",
	})
}
