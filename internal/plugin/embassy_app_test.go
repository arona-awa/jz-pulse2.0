package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clearlove/internal/database"
	"clearlove/internal/models"
)

// TestEmbassyPlugin 加载仓库 plugins/embassy 真实插件，验证「友校大使馆」完整业务流：
// 后台登记大使 → 昵称识别 → 验证账密 → post_created 落认证 → 卡片徽章。
func TestEmbassyPlugin(t *testing.T) {
	dataDir := setupAppEnv(t)

	// 拷贝真实插件文件到临时插件目录
	src, err := filepath.Abs(filepath.Join("..", "..", "plugins", "embassy"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dataDir, "plugins", "embassy")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"plugin.json", "main.js"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatalf("读取插件文件失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	SetEnabled("友校大使馆", true)
	if err := LoadApp("embassy"); err != nil {
		t.Fatalf("加载 embassy 插件失败: %v", err)
	}
	defer func() {
		UnloadApp("embassy")
		SetEnabled("友校大使馆", false)
	}()

	app := GetApp("embassy")
	if app == nil {
		t.Fatal("embassy 未登记到注册表")
	}
	if len(app.Routes) != 5 {
		t.Fatalf("路由数 = %d，期望 5", len(app.Routes))
	}
	if len(app.Pages) != 1 || len(AdminMenus()) != 1 {
		t.Fatalf("后台页/菜单注册异常")
	}
	if len(EventHooks("post_created")) != 1 {
		t.Fatal("post_created 事件未注册")
	}
	if len(FilterHooks("filter.card")) != 1 {
		t.Fatal("filter.card 未注册")
	}

	// 表已创建
	if _, err := database.DB.Exec("SELECT COUNT(1) FROM pl_embassy_ambassador"); err != nil {
		t.Fatalf("插件表未创建: %v", err)
	}

	// 简易 ctx 构造器（对齐 buildRequestCtx 的关键字段）
	mkCtx := func(query, form map[string]string) map[string]any {
		return map[string]any{
			"req":     map[string]any{"query": query, "form": form, "json": nil, "ip": "1.2.3.4", "fingerprint": "fp-test"},
			"params":  map[string]string{},
			"userId":  int64(0), "adminId": int64(1), "isAdmin": true, "csrf": "test-csrf",
		}
	}
	call := func(fn string, arg any) (any, error) {
		return app.Worker.Do(context.Background(), func(rt *jsRuntime) (any, error) {
			return rt.callFn(fn, arg)
		})
	}
	// 路由函数返回 {status?, json: {...}}，直接调用时需解包
	jbody := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		if m == nil {
			return nil
		}
		j, _ := m["json"].(map[string]any)
		return j
	}

	// 1) 未登记昵称 → 不受保护
	v, err := call("apiLookup", mkCtx(map[string]string{"nickname": "小美"}, nil))
	if err != nil {
		t.Fatalf("apiLookup 失败: %v", err)
	}
	if m := jbody(v); m == nil || m["protected"] == true {
		t.Fatalf("未登记昵称不应受保护: %v", v)
	}

	// 2) 后台登记大使（密码 bcrypt 落库）
	v, err = call("adminAdd", mkCtx(nil, map[string]string{
		"school": "邻校一中", "account": "amb01", "password": "1234", "nickname": "邻校大使小美"}))
	if err != nil {
		t.Fatalf("adminAdd 失败: %v", err)
	}
	if m, _ := v.(map[string]any); m == nil || m["redirect"] == "" {
		t.Fatalf("adminAdd 应重定向回管理页: %v", v)
	}
	var ambID int64
	var hash, school string
	if err := database.DB.QueryRow(
		"SELECT id, school, password FROM pl_embassy_ambassador WHERE nickname='邻校大使小美'").
		Scan(&ambID, &school, &hash); err != nil {
		t.Fatalf("大使未入库: %v", err)
	}
	if school != "邻校一中" {
		t.Fatalf("school 异常: %q", school)
	}
	if strings.EqualFold(hash, "1234") || len(hash) < 20 {
		t.Fatalf("密码应为 bcrypt 哈希，实际: %q", hash)
	}

	// 3) 已登记昵称 → 受保护
	v, err = call("apiLookup", mkCtx(map[string]string{"nickname": "邻校大使小美"}, nil))
	if err != nil {
		t.Fatalf("apiLookup 失败: %v", err)
	}
	if m := jbody(v); m == nil || m["protected"] != true || m["school"] != "邻校一中" {
		t.Fatalf("已登记昵称应受保护: %v", v)
	}

	// 4) 错误密码 → 验证失败
	v, err = call("apiVerify", mkCtx(nil, map[string]string{
		"nickname": "邻校大使小美", "account": "amb01", "password": "wrong"}))
	if err != nil {
		t.Fatalf("apiVerify 失败: %v", err)
	}
	if m := jbody(v); m == nil {
		t.Fatalf("apiVerify 返回异常: %v", v)
	} else if ok, _ := m["ok"].(bool); ok {
		t.Fatalf("错误密码不应通过验证: %v", v)
	}

	// 5) 正确密码 → 验证通过，验证记录落库
	v, err = call("apiVerify", mkCtx(nil, map[string]string{
		"nickname": "邻校大使小美", "account": "amb01", "password": "1234"}))
	if err != nil {
		t.Fatalf("apiVerify 失败: %v", err)
	}
	if m := jbody(v); m == nil {
		t.Fatalf("apiVerify 返回异常: %v", v)
	} else if ok, _ := m["ok"].(bool); !ok {
		t.Fatalf("正确密码应通过验证: %v", v)
	}
	if models.QueryInt("SELECT COUNT(1) FROM pl_embassy_verify WHERE fingerprint='fp-test'") != 1 {
		t.Fatal("验证记录未落库")
	}

	// 6) status：已验证指纹 → verified
	v, err = call("apiStatus", mkCtx(map[string]string{"nickname": "邻校大使小美"}, nil))
	if err != nil {
		t.Fatalf("apiStatus 失败: %v", err)
	}
	if m := jbody(v); m == nil {
		t.Fatalf("apiStatus 返回异常: %v", v)
	} else if verified, _ := m["verified"].(bool); !verified {
		t.Fatalf("status 应为已验证: %v", v)
	}

	// 7) post_created 处理（同步调用 handler）→ 认证落库
	if _, err := call("onPostCreated", map[string]any{
		"post_id": 101, "nickname": "邻校大使小美", "content": "你好"}); err != nil {
		t.Fatalf("onPostCreated 失败: %v", err)
	}
	if models.QueryInt("SELECT COUNT(1) FROM pl_embassy_cert WHERE post_id=101") != 1 {
		t.Fatal("认证记录未落库")
	}

	// 8) filter.card → 徽章追加
	card := map[string]any{"id": int64(101), "badges": []any{}}
	out := ApplyFilterValue("filter.card", card, nil)
	om, _ := out.(map[string]any)
	if om == nil {
		t.Fatalf("filter.card 无返回: %v", out)
	}
	badges, _ := om["badges"].([]any)
	found := false
	for _, b := range badges {
		if s, _ := b.(string); s == "邻校一中大使官方认证" {
			found = true
		}
	}
	if !found {
		t.Fatalf("徽章未追加: %v", om["badges"])
	}

	// 9) 无验证时同昵称发帖 → 不落认证
	if _, err := database.DB.Exec("DELETE FROM pl_embassy_verify"); err != nil {
		t.Fatal(err)
	}
	if _, err := call("onPostCreated", map[string]any{
		"post_id": 102, "nickname": "邻校大使小美", "content": "仿冒"}); err != nil {
		t.Fatalf("onPostCreated 失败: %v", err)
	}
	if models.QueryInt("SELECT COUNT(1) FROM pl_embassy_cert WHERE post_id=102") != 0 {
		t.Fatal("无有效验证不应落认证")
	}

	// 10) 删除大使 → 连带清理验证记录
	if _, err := call("adminDelete", mkCtx(nil, map[string]string{"id": "1"})); err != nil {
		t.Fatalf("adminDelete 失败: %v", err)
	}
	if models.QueryInt("SELECT COUNT(1) FROM pl_embassy_ambassador") != 0 {
		t.Fatal("大使未删除")
	}
	if models.QueryInt("SELECT COUNT(1) FROM pl_embassy_verify") != 0 {
		t.Fatal("验证记录未连带清理")
	}
}
