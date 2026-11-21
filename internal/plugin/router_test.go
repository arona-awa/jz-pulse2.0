package plugin

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubView 模拟 handlers 注入的视图能力
type stubView struct{ uid, aid int64 }

func (s stubView) RenderTemplate(fsys fs.FS, name string, data any) (string, error) {
	return "<rendered:" + name + ">", nil
}
func (s stubView) CSRF(r *http.Request) string          { return "tok" }
func (s stubView) UserID(r *http.Request) int64         { return s.uid }
func (s stubView) AdminID(r *http.Request) int64        { return s.aid }
func (s stubView) AdminPerm(r *http.Request, p string) bool { return true }

// TestAppRouter 覆盖插件路由分发：鉴权、返回值约定、后台页面
func TestAppRouter(t *testing.T) {
	dataDir := setupAppEnv(t)
	writeAppPlugin(t, dataDir, "demo", demoManifest, demoScript)

	SetEnabled("demo", true)
	if err := LoadApp("demo"); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	defer func() {
		UnloadApp("demo")
		SetEnabled("demo", false)
		SetViewProvider(nil)
	}()

	api := AppHandler()
	admin := AdminAppHandler()

	// 1) 公开路由：未登录也可访问（auth=none）
	SetViewProvider(stubView{})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x/demo/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hi") {
		t.Fatalf("公开路由异常: code=%d body=%q", w.Code, w.Body.String())
	}

	// 2) 需要登录的路由：未登录应跳转登录页
	w2 := httptest.NewRecorder()
	api.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/x/demo/do", nil))
	if w2.Code != http.StatusFound || !strings.Contains(w2.Header().Get("Location"), "/login") {
		t.Fatalf("未登录未拦截: code=%d location=%q", w2.Code, w2.Header().Get("Location"))
	}

	// 3) 已登录：返回 JSON
	SetViewProvider(stubView{uid: 7})
	w3 := httptest.NewRecorder()
	api.ServeHTTP(w3, httptest.NewRequest(http.MethodPost, "/x/demo/do", nil))
	if w3.Code != http.StatusOK || !strings.Contains(w3.Body.String(), `"ok"`) {
		t.Fatalf("JSON 响应异常: code=%d body=%q", w3.Code, w3.Body.String())
	}

	// 4) 未知 slug → 404
	w4 := httptest.NewRecorder()
	api.ServeHTTP(w4, httptest.NewRequest(http.MethodGet, "/x/nope/", nil))
	if w4.Code != http.StatusNotFound {
		t.Fatalf("未知插件应 404，实际 %d", w4.Code)
	}

	// 5) 后台页面：未登录跳登录页
	SetViewProvider(stubView{})
	w5 := httptest.NewRecorder()
	admin.ServeHTTP(w5, httptest.NewRequest(http.MethodGet, "/admin/plugins/demo/", nil))
	if w5.Code != http.StatusFound {
		t.Fatalf("后台页面未拦截: %d", w5.Code)
	}

	// 6) 后台页面：管理员可访问
	SetViewProvider(stubView{aid: 1})
	w6 := httptest.NewRecorder()
	admin.ServeHTTP(w6, httptest.NewRequest(http.MethodGet, "/admin/plugins/demo/", nil))
	if w6.Code != http.StatusOK || !strings.Contains(w6.Body.String(), "admin") {
		t.Fatalf("后台页面异常: code=%d body=%q", w6.Code, w6.Body.String())
	}
}
