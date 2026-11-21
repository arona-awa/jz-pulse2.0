package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/handlers"
	"clearlove/internal/middleware"
	"clearlove/internal/models"
	"clearlove/internal/plugin"
)

// TestAppPluginHTTPIntegration 端到端验证应用型插件：
// 完整路由链（中间件 + 插件分发 + 鉴权）与站点模板渲染。
func TestAppPluginHTTPIntegration(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	uploadDir := filepath.Join(root, "uploads")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	config.Cfg = config.Config{
		DataDir:    dataDir,
		UploadDir:  uploadDir,
		DBType:     "sqlite",
		SQLitePath: filepath.Join(dataDir, "clearlove.db"),
		Installed:  true, // 跳过安装门禁
		Secret:     "integration-secret",
		Version:    config.Version,
	}
	if err := database.Connect(); err != nil {
		t.Fatalf("数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			_ = database.DB.Close()
			database.DB = nil
		}
	})
	if err := database.Migrate(); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	_ = models.SetSetting("site_name", "集成测试站")

	// 装载模板（同时完成插件视图能力注入）
	handlers.SetupTemplates(webFS)

	// 准备应用型插件
	pluginDir := filepath.Join(dataDir, "plugins", "demo")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pluginDir, "plugin.json"), `{
	  "kind": "app", "name": "demo", "slug": "demo", "version": "1.0.0",
	  "runtime": { "engine": "js", "entry": "main.js", "timeout_ms": 800 },
	  "permissions": ["db", "site.read"]
	}`)
	writeFile(t, filepath.Join(pluginDir, "main.js"), `
	  function setup() {
	    clv.table("hit", { path: "string", created_at: "time" });
	    clv.route("GET", "/open", "openPage", { auth: "none" });
	    clv.route("GET", "/json", "jsonPage", { auth: "none", json: true });
	    clv.route("GET", "/me", "mePage", { auth: "user" });
	    clv.adminMenu({ label: "演示管理", path: "/", perm: "plugins" });
	    clv.adminPage("/", "adminHome", { perm: "plugins" });
	    clv.slot("footer_html", "foot");
	    clv.middleware("http.before", "guard");
	  }
	  function openPage(ctx) { return { body: "<h1>plugin-open</h1>" }; }
	  function jsonPage(ctx) { return { json: { ok: true, dialect: clv.db.dialect() } }; }
	  function mePage(ctx) { return { json: { uid: ctx.userId } }; }
	  function adminHome(ctx) { return { body: "<h1>plugin-admin</h1>" }; }
	  function foot(data) { return "<span id='pf'>plugin-footer</span>"; }
	  function guard(req) {
	    if (req.path === "/guard-me") return { abort: true, status: 403, body: "blocked" };
	    return null;
	  }
	`)
	plugin.SetEnabled("demo", true)
	if err := plugin.LoadApp("demo"); err != nil {
		t.Fatalf("加载应用型插件失败: %v", err)
	}
	t.Cleanup(func() {
		plugin.UnloadApp("demo")
		plugin.SetEnabled("demo", false)
	})

	srv := middleware.Use(buildMux())
	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}

	// 1) 插件公开路由（HTML）
	if w := do(http.MethodGet, "/x/demo/open"); w.Code != 200 || !strings.Contains(w.Body.String(), "plugin-open") {
		t.Fatalf("公开路由异常: %d %q", w.Code, w.Body.String())
	}

	// 2) 插件 JSON 路由
	w := do(http.MethodGet, "/x/demo/json")
	if w.Code != 200 {
		t.Fatalf("JSON 路由异常: %d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("JSON 解析失败: %v (%q)", err, w.Body.String())
	}
	if payload["dialect"] != "sqlite" {
		t.Fatalf("dialect 异常: %v", payload["dialect"])
	}

	// 3) 需要登录的插件路由：未登录跳转
	if w := do(http.MethodGet, "/x/demo/me"); w.Code != http.StatusFound {
		t.Fatalf("未登录应跳转，实际 %d", w.Code)
	}

	// 4) 插件中间件短路
	if w := do(http.MethodGet, "/guard-me"); w.Code != http.StatusForbidden {
		t.Fatalf("中间件未短路: %d", w.Code)
	}

	// 5) 站点首页正常渲染，并包含插件 footer slot 输出
	w = do(http.MethodGet, "/")
	if w.Code != 200 {
		t.Fatalf("首页异常: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "plugin-footer") {
		t.Fatalf("footer slot 未注入首页")
	}

	// 6) 后台页面未登录跳转登录页
	if w := do(http.MethodGet, "/admin/plugins/demo/"); w.Code != http.StatusFound {
		t.Fatalf("后台页面未拦截: %d", w.Code)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// copyDir 递归复制目录（测试里把仓库示例插件装进临时站点）
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDir(t, s, d)
			continue
		}
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAIPolishPlugin 端到端验证「AI 语句美化」应用型插件：
// 静态 hook 注入、可用性探测、调用本机 AI 服务、长度限制。
func TestAIPolishPlugin(t *testing.T) {
	dataDir := setupSite(t)
	copyDir(t, filepath.Join("plugins", "ai-polish"), filepath.Join(dataDir, "plugins", "ai-polish"))

	plugin.SetEnabled("AI 语句美化", true)
	if err := plugin.LoadApp("ai-polish"); err != nil {
		t.Fatalf("加载 AI 美化插件失败: %v", err)
	}
	defer func() {
		plugin.UnloadApp("ai-polish")
		plugin.SetEnabled("AI 语句美化", false)
	}()

	// 1) 声明式 hook 静态注入按钮脚本（不占用运行时）
	if got := plugin.CallHTML("footer_html"); !strings.Contains(got, "/x/ai-polish") {
		t.Fatalf("未注入美化脚本: %q", got)
	}

	srv := middleware.Use(buildMux())
	do := func(method, path, body, csrf string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
			req.AddCookie(&http.Cookie{Name: "clv_csrf", Value: csrf})
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}

	w0 := do(http.MethodGet, "/", "", "")
	csrf := ""
	for _, c := range w0.Result().Cookies() {
		if c.Name == "clv_csrf" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatal("未取到 CSRF 令牌")
	}

	// 2) 未接入 AI：config 标记不可用，polish 明确拒绝
	if w := do(http.MethodGet, "/x/ai-polish/config", "", ""); !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatalf("未接入 AI 时可用性判断异常: %s", w.Body.String())
	}
	if w := do(http.MethodPost, "/x/ai-polish/polish", `{"text":"你好呀"}`, csrf); w.Code != 400 {
		t.Fatalf("未接入 AI 应返回 400，实际 %d: %s", w.Code, w.Body.String())
	}

	// 3) 配置本机 AI 服务（模拟 OpenAI 兼容接口，验证 net.local 权限放行内网）
	ai := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(rw, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"choices":[{"message":{"content":"\"今天天气很好，适合出门走走。\""}}]}`))
	}))
	defer ai.Close()

	_ = models.SetSetting("ai_enabled", "1")
	_ = models.SetSetting("ai_base", ai.URL)
	_ = models.SetSetting("ai_key", "test-key")
	_ = models.SetSetting("ai_model", "test-model")

	if w := do(http.MethodGet, "/x/ai-polish/config", "", ""); !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatalf("接入 AI 后可用性判断异常: %s", w.Body.String())
	}

	// 4) 美化成功，并清理模型输出的引号包裹
	w := do(http.MethodPost, "/x/ai-polish/polish", `{"text":"今天天气不错"}`, csrf)
	if w.Code != 200 {
		t.Fatalf("美化失败: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "今天天气很好，适合出门走走。") {
		t.Fatalf("返回内容异常: %s", body)
	}
	if strings.Contains(body, `\"今天天气很好`) {
		t.Fatalf("未清理引号包裹: %s", body)
	}

	// 5) 超出单次处理上限被拒绝
	long := strings.Repeat("啊", 700)
	if w := do(http.MethodPost, "/x/ai-polish/polish", `{"text":"`+long+`"}`, csrf); w.Code != 400 {
		t.Fatalf("超长内容应被拒绝，实际 %d", w.Code)
	}
}

// setupSite 初始化一次完整的站点环境（数据库 + 模板）
func setupSite(t *testing.T) (dataDir string) {
	t.Helper()
	root := t.TempDir()
	dataDir = filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config.Cfg = config.Config{
		DataDir:    dataDir,
		UploadDir:  filepath.Join(root, "uploads"),
		DBType:     "sqlite",
		SQLitePath: filepath.Join(dataDir, "clearlove.db"),
		Installed:  true,
		Secret:     "integration-secret",
		Version:    config.Version,
	}
	if err := database.Connect(); err != nil {
		t.Fatalf("数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			_ = database.DB.Close()
			database.DB = nil
		}
	})
	if err := database.Migrate(); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	_ = models.SetSetting("site_name", "集成测试站")
	handlers.SetupTemplates(webFS)
	return dataDir
}

// TestPluginFiltersTemplateAndLogs 覆盖新增能力：
// 过滤器（card / api.response / post.visible）、http.after、模板覆盖与运行日志。
func TestPluginFiltersTemplateAndLogs(t *testing.T) {
	dataDir := setupSite(t)

	pluginDir := filepath.Join(dataDir, "plugins", "fx")
	if err := os.MkdirAll(filepath.Join(pluginDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pluginDir, "plugin.json"), `{
	  "kind": "app", "name": "fx", "slug": "fx", "version": "1.0.0",
	  "runtime": { "engine": "js", "entry": "main.js", "timeout_ms": 800 },
	  "permissions": ["db", "cache"]
	}`)
	writeFile(t, filepath.Join(pluginDir, "main.js"), `
	  function setup() {
	    clv.filter("filter.card", 10, "cardFilter");
	    clv.filter("filter.api.response", 10, "respFilter");
	    clv.filter("filter.post.visible", 10, "visibleFilter");
	    clv.middleware("http.after", "afterLog");
	  }
	  function cardFilter(card) { card.nickname = "改写:" + card.nickname; return card; }
	  function respFilter(resp) { resp.plugin_tag = "fx"; return resp; }
	  function visibleFilter(ok, ctx) { return (ctx && ctx.post_id === 999) ? false : ok; }
	  function afterLog(info) { clv.cache.set("last_status", info.status, 60); }
	  function __status() { return clv.cache.get("last_status") || 0; }
	`)
	// 模板覆盖：插件目录下的同名模板会覆盖内核片段
	writeFile(t, filepath.Join(pluginDir, "templates", "pg_index.html"),
		`{{define "pg_index"}}<div id="plugin-override">OVERRIDE</div>{{end}}`)

	plugin.SetEnabled("fx", true)
	if err := plugin.LoadApp("fx"); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	defer func() {
		plugin.UnloadApp("fx")
		plugin.SetEnabled("fx", false)
		handlers.ReloadTemplates()
	}()
	handlers.ReloadTemplates() // 应用插件模板覆盖

	// 准备一条帖子（含一条 id=999 用于可见性过滤）
	if _, err := database.DB.Exec(
		`INSERT INTO posts(id,nickname,content,topic_id,ip,fingerprint,status,is_admin,badges,created_at)
		 VALUES(1,'原昵称','正文',0,'','',1,0,'',?)`, models.Now()); err != nil {
		t.Fatal(err)
	}

	srv := middleware.Use(buildMux())
	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}

	// 1) 模板覆盖生效
	if w := do(http.MethodGet, "/"); !strings.Contains(w.Body.String(), "plugin-override") {
		t.Fatalf("模板覆盖未生效: %q", w.Body.String())
	}

	// 2) filter.api.response 改写响应体
	if w := do(http.MethodGet, "/api/v1/topics"); !strings.Contains(w.Body.String(), "plugin_tag") {
		t.Fatalf("filter.api.response 未生效: %q", w.Body.String())
	}

	// 3) filter.card 改写卡片数据
	w := do(http.MethodGet, "/api/v1/posts?limit=5")
	if !strings.Contains(w.Body.String(), "改写:原昵称") {
		t.Fatalf("filter.card 未生效: %q", w.Body.String())
	}

	// 4) filter.post.visible 否决可见性
	if _, err := database.DB.Exec(
		`INSERT INTO posts(id,nickname,content,topic_id,ip,fingerprint,status,is_admin,badges,created_at)
		 VALUES(999,'x','y',0,'','',1,0,'',?)`, models.Now()); err != nil {
		t.Fatal(err)
	}
	if w := do(http.MethodGet, "/post/999"); w.Code != http.StatusNotFound {
		t.Fatalf("filter.post.visible 未生效: %d", w.Code)
	}

	// 5) http.after 被调用（状态码写入插件缓存）
	out, err := plugin.CallFn("fx", "__status")
	if err != nil {
		t.Fatalf("脚本调用失败: %v", err)
	}
	if n, ok := out.(int64); !ok || n == 0 {
		t.Fatalf("http.after 未执行: %v", out)
	}

	// 6) 运行日志可读
	logs := plugin.PluginLogs("fx", 50)
	if len(logs) == 0 {
		t.Fatal("插件运行日志为空")
	}
}

// TestAppPluginHTTPIntegration 端到端验证应用型插件：
// 完整路由链（中间件 + 插件分发 + 鉴权）与站点模板渲染。

