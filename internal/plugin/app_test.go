package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/models"
)

// setupAppEnv 准备带数据库的应用型插件测试环境
func setupAppEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config.Cfg = config.Config{
		DataDir:    dataDir,
		UploadDir:  filepath.Join(root, "uploads"),
		DBType:     "sqlite",
		SQLitePath: filepath.Join(dataDir, "clearlove.db"),
		Installed:  true,
		Secret:     "test-secret",
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
		t.Fatalf("数据库迁移失败: %v", err)
	}
	return dataDir
}

func writeAppPlugin(t *testing.T, dataDir, dir, manifest, script string) {
	t.Helper()
	d := filepath.Join(dataDir, "plugins", dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "main.js"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
}

const demoManifest = `{
  "kind": "app",
  "name": "demo",
  "slug": "demo",
  "version": "1.0.0",
  "runtime": { "engine": "js", "entry": "main.js", "timeout_ms": 800 },
  "permissions": ["db", "cache", "settings.read"]
}`

const demoScript = `
var T = clv.db.table("log");
function setup() {
  clv.table("log", { user_id: "int", day: "string" }, { unique: [["user_id", "day"]] });
  clv.route("GET", "/", "pageHome", { auth: "none" });
  clv.route("POST", "/do", "doPost", { auth: "user" });
  clv.adminPage("/", "adminHome", { perm: "plugins" });
  clv.adminMenu({ label: "演示管理", path: "/", perm: "plugins", order: 10 });
  clv.slot("footer_html", "footSlot");
  clv.on("post_created", "onPost");
  clv.filter("filter.content", 10, "censor");
  clv.job("tick", "1h", "onTick");
}
function pageHome(ctx) { return { body: "<h1>hi</h1>" }; }
function doPost(ctx) { return { json: { ok: true } }; }
function adminHome(ctx) { return { body: "admin" }; }
function footSlot(data) { return "<span class='demo-slot'>slot</span>"; }
function onPost(e) { clv.cache.set("last_post", e.post_id, 60); }
function censor(s) { return s.replace(/广告/g, "***"); }
function onTick(ctx) { clv.log.info("tick"); }
function __check() { return clv.cache.get("last_post") || 0; }
function __badPerm() {
  try { clv.setting.set("hack", "1"); return "allowed"; } catch (e) { return "denied"; }
}
function __badSQL() {
  try { clv.db.exec("DELETE FROM posts WHERE id=0"); return "allowed"; } catch (e) { return "denied"; }
}
function __loop() { while (true) {} }
`

// TestAppPluginLifecycle 覆盖应用型插件的完整能力：注册、建表、slot、过滤器、事件、任务
func TestAppPluginLifecycle(t *testing.T) {
	dataDir := setupAppEnv(t)
	writeAppPlugin(t, dataDir, "demo", demoManifest, demoScript)

	SetEnabled("demo", true)
	if err := LoadApp("demo"); err != nil {
		t.Fatalf("加载应用型插件失败: %v", err)
	}
	defer func() {
		UnloadApp("demo")
		SetEnabled("demo", false)
	}()

	app := GetApp("demo")
	if app == nil {
		t.Fatal("插件未登记到注册表")
	}
	if app.Plugin.SlugOf() != "demo" {
		t.Fatalf("slug 解析异常: %q", app.Plugin.SlugOf())
	}

	// 1) 各类扩展点均已注册
	if len(app.Routes) != 2 {
		t.Fatalf("路由数 = %d，期望 2", len(app.Routes))
	}
	if len(app.Pages) != 1 || len(AdminMenus()) != 1 {
		t.Fatalf("后台页面/菜单注册异常: pages=%d menus=%d", len(app.Pages), len(AdminMenus()))
	}
	if len(EventHooks("post_created")) != 1 {
		t.Fatal("事件未注册")
	}
	if len(FilterHooks("filter.content")) != 1 {
		t.Fatal("过滤器未注册")
	}
	if len(app.Jobs) != 1 {
		t.Fatal("定时任务未注册")
	}

	// 2) 数据表已创建（pl_<slug>_log）
	if _, err := database.DB.Exec("INSERT INTO pl_demo_log(user_id,day) VALUES(1,'2026-01-01')"); err != nil {
		t.Fatalf("插件数据表不可写: %v", err)
	}

	// 3) Slot 输出包含插件内容
	if got := SlotHTML("footer_html", map[string]any{}); !strings.Contains(got, "demo-slot") {
		t.Fatalf("Slot 未生效: %q", got)
	}

	// 4) 过滤器生效
	if got := ApplyFilterStr("filter.content", "这是一条广告"); got != "这是一条***" {
		t.Fatalf("过滤器未生效: %q", got)
	}

	// 5) 事件回调（异步）确实执行
	Emit("post_created", map[string]any{"post_id": 42})
	deadline := time.Now().Add(2 * time.Second)
	var last any
	for time.Now().Before(deadline) {
		v, err := app.Worker.Do(context.Background(), func(rt *jsRuntime) (any, error) {
			return rt.callFn("__check")
		})
		if err != nil {
			t.Fatalf("脚本调用失败: %v", err)
		}
		last = v
		if fmt.Sprint(v) == "42" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if fmt.Sprint(last) != "42" {
		t.Fatalf("事件回调未执行，cache = %v", last)
	}
}

// TestAppPluginPermission 未声明的权限必须被拒绝
func TestAppPluginPermission(t *testing.T) {
	dataDir := setupAppEnv(t)
	writeAppPlugin(t, dataDir, "demo", demoManifest, demoScript)

	SetEnabled("demo", true)
	if err := LoadApp("demo"); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	defer func() {
		UnloadApp("demo")
		SetEnabled("demo", false)
	}()

	app := GetApp("demo")

	// settings.write 未在 permissions 中声明
	v, err := app.Worker.Do(context.Background(), func(rt *jsRuntime) (any, error) {
		return rt.callFn("__badPerm")
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if fmt.Sprint(v) != "denied" {
		t.Fatalf("未声明的权限未被拒绝: %v", v)
	}

	// 写内核表必须被拒绝（只允许 pl_<slug>_ 前缀）
	v2, err := app.Worker.Do(context.Background(), func(rt *jsRuntime) (any, error) {
		return rt.callFn("__badSQL")
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if fmt.Sprint(v2) != "denied" {
		t.Fatalf("内核表写操作未被拒绝: %v", v2)
	}
}

// TestAppPluginTimeoutInterrupt 死循环必须被超时中断，且中断后插件仍可继续服务
func TestAppPluginTimeoutInterrupt(t *testing.T) {
	dataDir := setupAppEnv(t)
	writeAppPlugin(t, dataDir, "demo", demoManifest, demoScript)

	SetEnabled("demo", true)
	if err := LoadApp("demo"); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	defer func() {
		UnloadApp("demo")
		SetEnabled("demo", false)
	}()

	app := GetApp("demo")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := app.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
		return rt.callFn("__loop")
	})
	if err == nil {
		t.Fatal("死循环未被超时中断")
	}

	// 中断后虚拟机应恢复可用
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if _, err := app.Worker.Do(ctx2, func(rt *jsRuntime) (any, error) {
		return rt.callFn("__check")
	}); err != nil {
		t.Fatalf("超时中断后插件不可用: %v", err)
	}
}

// TestDeclarativeUnaffected 声明式插件仍然按原逻辑工作（回归）
func TestDeclarativeUnaffected(t *testing.T) {
	dataDir := setupAppEnv(t)
	writePlugin(t, dataDir, "lite", `{
		"name":"lite","version":"1.0.0",
		"hooks":[{"hook":"footer_html","type":"html","html":"<b>lite</b>"},
		         {"hook":"content_filter","type":"filter","match":"坏","replace":"好"}]
	}`, "")

	SetEnabled("lite", true)
	defer SetEnabled("lite", false)

	if got := CallHTML("footer_html"); !strings.Contains(got, "<b>lite</b>") {
		t.Fatalf("声明式 HTML 钩子失效: %q", got)
	}
	if got := ApplyFilterStr("filter.content", "坏人"); got != "好人" {
		t.Fatalf("声明式过滤器失效: %q", got)
	}
	if models.QueryInt("SELECT 1") != 1 {
		t.Fatal("数据库不可用")
	}
}
