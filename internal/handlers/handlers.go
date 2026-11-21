// Package handlers 全部 HTTP 处理器。
// 本文件提供：模板初始化与渲染、会话判定（用户/管理员）、JSON 输出等公共设施。
package handlers

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/middleware"
	"clearlove/internal/models"
	"clearlove/internal/plugin"
	"clearlove/internal/util"
)

// Tmpl 全局模板集（layout.html / admin_layout.html 为布局，其余为页面片段）
var Tmpl *template.Template

// tmplFuncs 模板函数表（插件模板渲染复用同一套函数）
var tmplFuncs template.FuncMap

// StaticFS 嵌入式静态资源（由 main 注入）
var StaticFS fs.FS

// webTemplatesFS 内嵌模板源（插件覆盖时用于重新解析）
var (
	webTemplatesFS  embed.FS
	hasWebTemplates bool
)

// SetupTemplates 解析嵌入式模板并注册模板函数
func SetupTemplates(efs embed.FS) {
	funcs := template.FuncMap{
		// nl2br 转义 HTML 后将换行转为 <br>（安全展示用户文本）
		"nl2br": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), "\n", "<br>"))
		},
		// datefmt RFC3339 -> 本地 "MM-DD HH:mm"
		"datefmt": func(s string) string {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return s
			}
			return t.Local().Format("01-02 15:04")
		},
		"snippet": func(s string, n int) string {
			r := []rune(s)
			if len(r) > n {
				return string(r[:n]) + "..."
			}
			return s
		},
		// substr0 取首字符用于生成头像占位
		"substr0": func(s string) string {
			r := []rune(s)
			if len(r) == 0 {
				return "匿"
			}
			return string(r[0])
		},
		// 分页辅助
		"coins":  models.FormatCoins,
		"plus1":  func(n int) int { return n + 1 },
		"minus1": func(n int) int { return n - 1 },
	}
	// pageContent 按页面名动态渲染子模板（布局中调用，等价于动态 template 包含）
	funcs["pageContent"] = func(name string, data any) template.HTML {
		var buf bytes.Buffer
		if Tmpl == nil {
			return ""
		}
		if err := Tmpl.ExecuteTemplate(&buf, name, data); err != nil {
			util.Log("error", "页面模板 %s 渲染失败: %v", name, err)
			return template.HTML("<p>页面渲染失败</p>")
		}
		return template.HTML(buf.String())
	}
	tmplFuncs = funcs
	webTemplatesFS = efs
	hasWebTemplates = true
	RebuildTemplates()
	// 把模板能力反向注入插件运行时（应用型插件的页面渲染依赖它）
	plugin.SetViewProvider(pluginViewProvider{})
}

// RebuildTemplates 重新解析内核模板，并叠加插件目录中的同名覆盖模板。
// Go 模板的特性：后解析的同名模板会覆盖先前的，因此插件可精准替换某个片段。
func RebuildTemplates() {
	if !hasWebTemplates {
		return
	}
	Tmpl = template.Must(template.New("clv").Funcs(tmplFuncs).ParseFS(webTemplatesFS, "web/templates/*.html"))
	for _, dir := range plugin.EnabledTemplateDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		hasHTML := false
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".html") {
				hasHTML = true
				break
			}
		}
		if !hasHTML {
			continue
		}
		if _, err := Tmpl.ParseFS(os.DirFS(dir), "*.html"); err != nil {
			util.Log("warn", "插件模板覆盖失败 %s: %v", dir, err)
			continue
		}
		util.Log("info", "已应用插件模板覆盖: %s", dir)
	}
}

// ReloadTemplates 插件启停后刷新模板（含覆盖），供 handlers 内部调用
func ReloadTemplates() { RebuildTemplates() }

// Render 渲染页面。公共数据（页面名、CSRF、站名、用户、插件注入）自动注入
func Render(w http.ResponseWriter, r *http.Request, layout, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Page"] = page
	data["csrf"] = middleware.CSRFToken(r)
	data["clvVersion"] = config.Version
	data["site"] = models.GetSetting("site_name")
	data["bg"] = models.GetSetting("theme_bg")
	data["donate"] = models.GetSetting("donate_img")
	u := CurrentUser(r)
	data["myLevel"] = models.LevelForExp(0)
	data["canFood"] = false
	data["tippedAuthors"] = []int64{}
	if u != nil {
		data["user"] = u
		data["myLevel"] = models.LevelForExp(u.Exp)
		data["canFood"] = data["myLevel"].(models.LevelInfo).Num >= 2
		// 已投过币的作者（同一用户对同一作者永久只允许一次）
		ids := []int64{}
		if rows, err := database.DB.Query("SELECT DISTINCT to_user_id FROM coin_tips WHERE from_user_id=?", u.ID); err == nil {
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
		}
		data["tippedAuthors"] = ids
	}
	// 个性化设置：登录用户从服务端读取，未登录用默认值（每次进入页面自动同步）
	prefs := models.DefaultPrefs()
	if u != nil {
		prefs = models.GetPrefs(u.ID)
	}
	data["prefs"] = prefs
	// 后台布局依赖 .admin 渲染侧边菜单与管理条，必须在此统一注入
	if a := CurrentAdmin(r); a != nil {
		data["admin"] = a
	}

	// 插件 UI 注入点：A 型静态片段 + B 型（应用型）动态渲染
	slotData := pluginSlotData(r, page, data)
	data["header_html"] = template.HTML(plugin.SlotHTML("header_html", slotData))
	data["footer_html"] = template.HTML(plugin.SlotHTML("footer_html", slotData))
	data["head_end"] = template.HTML(plugin.SlotHTML("head.end", slotData))
	data["body_end"] = template.HTML(plugin.SlotHTML("body.end", slotData))
	data["admin_head_end"] = template.HTML(plugin.SlotHTML("admin.head.end", slotData))
	data["admin_body_end"] = template.HTML(plugin.SlotHTML("admin.body.end", slotData))
	data["admin_sidebar_after"] = template.HTML(plugin.SlotHTML("admin.sidebar.after", slotData))
	data["admin_settings_after"] = template.HTML(plugin.SlotHTML("admin.settings.after", slotData))
	data["admin_dashboard_after"] = template.HTML(plugin.SlotHTML("admin.dashboard.after", slotData))
	data["comments_after"] = template.HTML(plugin.SlotHTML("comments.after", slotData))
	data["compose_after"] = template.HTML(plugin.SlotHTML("compose.after", slotData))
	data["profile_after"] = template.HTML(plugin.SlotHTML("profile.after", slotData))
	data["detail_actions_after"] = template.HTML(plugin.SlotHTML("post.detail.actions.after", slotData))
	data["detail_after"] = template.HTML(plugin.SlotHTML("post.detail.after", slotData))
	data["plugin_menus"] = plugin.AdminMenus()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := Tmpl.ExecuteTemplate(w, layout, data); err != nil {
		util.Log("error", "模板渲染失败 %s: %v", page, err)
	}
}

// JSON 输出 JSON 响应
func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fail 输出错误 JSON
func fail(w http.ResponseWriter, code int, msg string) {
	JSON(w, code, map[string]any{"ok": false, "msg": msg})
}

// okJSON 输出成功 JSON（应用型插件可通过 filter.api.response 改写响应体）
func okJSON(w http.ResponseWriter, extra map[string]any) {
	m := map[string]any{"ok": true}
	for k, v := range extra {
		m[k] = v
	}
	if plugin.HasApps() {
		if out := plugin.ApplyFilterValue("filter.api.response", m, nil); out != nil {
			if mm, ok := out.(map[string]any); ok {
				m = mm
			}
		}
	}
	JSON(w, http.StatusOK, m)
}

// pathInt64 解析路径参数为 int64
func pathInt64(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n
}

// formInt64 解析表单整数值
func formInt64(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue(key)), 10, 64)
	return n
}

// CurrentUser 从签名 Cookie 解析当前登录用户
func CurrentUser(r *http.Request) *models.User {
	c, err := r.Cookie("clv_user")
	if err != nil {
		return nil
	}
	uid, ok := util.VerifySession(c.Value)
	if !ok {
		return nil
	}
	u := models.GetUserByID(uid)
	if u == nil || u.Status != 1 {
		return nil
	}
	// 每日自动 +1 硬币；多天未访问时在本次访问补齐。
	if n := models.AwardDailyCoin(u.ID); n > 0 {
		u.CoinTenths += n
	}
	return u
}

// CurrentAdmin 从签名 Cookie 解析当前管理员
func CurrentAdmin(r *http.Request) *models.Admin {
	c, err := r.Cookie("clv_admin")
	if err != nil {
		return nil
	}
	aid, ok := util.VerifySession(c.Value)
	if !ok {
		return nil
	}
	return models.GetAdminByID(aid)
}

// requireAdmin 后台权限门禁：未登录跳登录页，权限不足返回 403
func requireAdmin(w http.ResponseWriter, r *http.Request, perm string) *models.Admin {
	a := CurrentAdmin(r)
	if a == nil {
		http.Redirect(w, r, "/admin/login", http.StatusFound)
		return nil
	}
	if perm != "" && !a.HasPerm(perm) {
		http.Error(w, "没有操作权限", http.StatusForbidden)
		return nil
	}
	return a
}
