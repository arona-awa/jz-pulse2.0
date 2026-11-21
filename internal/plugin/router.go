// 应用型插件的 HTTP 分发。
//
//	/x/<slug>/...               前台路由（auth: none | user | admin）
//	/x/<slug>/assets/...        插件静态资源（只读）
//	/admin/plugins/<slug>/...   后台页面（强制管理员 + perm 校验）
package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clearlove/internal/util"
)

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

const maxPluginBody = 1 << 20 // 插件请求体读取上限 1MB

// AppHandler 前台插件路由分发器
func AppHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/x/")
		slug, sub := splitSlugPath(rest)
		app := AppBySlug(slug)
		if app == nil {
			http.NotFound(w, r)
			return
		}
		// 静态资源：/x/<slug>/assets/xxx
		if strings.HasPrefix(sub, "/assets/") {
			servePluginAsset(w, r, app, strings.TrimPrefix(sub, "/assets/"))
			return
		}
		route, ok := matchRoute(app, r.Method, sub)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !checkAuth(w, r, route.Auth) {
			return
		}
		serveHandler(w, r, app, route.Fn, route.JSON, nil)
	})
}

// AdminAppHandler 后台插件页面分发器
func AdminAppHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if viewProvider == nil {
			http.Error(w, "服务未就绪", http.StatusServiceUnavailable)
			return
		}
		if viewProvider.AdminID(r) == 0 {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/admin/plugins/")
		slug, sub := splitSlugPath(rest)
		app := AppBySlug(slug)
		if app == nil {
			http.NotFound(w, r)
			return
		}
		fn, ok := AdminPageFn(slug, sub)
		if !ok {
			http.NotFound(w, r)
			return
		}
		// 页面权限：未声明 perm 时按 plugins 权限组处理
		perm := "plugins"
		for _, p := range app.Pages {
			if p.Path == sub {
				if strings.TrimSpace(p.Perm) != "" {
					perm = p.Perm
				}
				break
			}
		}
		if !viewProvider.AdminPerm(r, perm) {
			http.Error(w, "没有操作权限", http.StatusForbidden)
			return
		}
		serveHandler(w, r, app, fn, false, map[string]string{"admin": "1"})
	})
}

// ---------- 内部实现 ----------

// splitSlugPath 拆出 slug 与剩余路径
func splitSlugPath(rest string) (slug, sub string) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", "/"
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		slug, sub = rest[:i], rest[i:]
	} else {
		slug, sub = rest, "/"
	}
	if !strings.HasPrefix(sub, "/") {
		sub = "/" + sub
	}
	if len(sub) > 1 {
		sub = strings.TrimRight(sub, "/")
		if sub == "" {
			sub = "/"
		}
	}
	return slug, sub
}

func matchRoute(a *App, method, sub string) (*RouteReg, bool) {
	for i := range a.Routes {
		rt := &a.Routes[i]
		if !methodEqual(rt.Method, method) {
			continue
		}
		if pathEqual(rt.Path, sub) {
			return rt, true
		}
	}
	return nil, false
}

func checkAuth(w http.ResponseWriter, r *http.Request, auth string) bool {
	if viewProvider == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(auth)) {
	case "user":
		if viewProvider.UserID(r) == 0 {
			next := r.URL.Path
			if r.URL.RawQuery != "" {
				next += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, "/login?next="+next, http.StatusFound)
			return false
		}
	case "admin":
		if viewProvider.AdminID(r) == 0 {
			http.Error(w, "需要管理员身份", http.StatusForbidden)
			return false
		}
	}
	return true
}

// serveHandler 调用插件 handler 并写出响应
func serveHandler(w http.ResponseWriter, r *http.Request, app *App, fn string, isJSON bool, extra map[string]string) {
	if app.Worker == nil || app.Worker.Closed() {
		http.Error(w, "插件未运行", http.StatusServiceUnavailable)
		return
	}
	ctxMap := buildRequestCtx(r, extra)

	ctx, cancel := contextWithTimeout(app.Plugin.Timeout())
	defer cancel()
	v, err := app.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
		rt.cur = ctxToReqContext(r, ctxMap)
		defer func() { rt.cur = nil }()
		return rt.callFn(fn, ctxMap)
	})
	if err != nil {
		noteFailure(app, err)
		logPlugin(app.Plugin.SlugOf(), "route:"+fn, "执行失败: "+err.Error(), 0)
		if isJSON {
			writeJSONError(w, http.StatusInternalServerError, "插件执行失败")
			return
		}
		http.Error(w, "插件执行失败", http.StatusInternalServerError)
		return
	}
	noteSuccess(app)
	writeResult(w, r, app, fn, v, isJSON)
}

func writeResult(w http.ResponseWriter, r *http.Request, app *App, fn string, v any, isJSON bool) {
	m, ok := v.(map[string]any)
	if !ok {
		if isJSON {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	for k, hv := range m {
		if k == "headers" {
			if hdrs, ok := hv.(map[string]any); ok {
				for hk, hvv := range hdrs {
					w.Header().Set(hk, strOf(hvv))
				}
			}
		}
	}
	status := intOf(m["status"])
	if status <= 0 {
		status = http.StatusOK
	}
	if u := strOf(m["redirect"]); u != "" {
		http.Redirect(w, r, u, http.StatusSeeOther)
		return
	}
	if j, ok := m["json"]; ok {
		writeJSON(w, status, j)
		return
	}
	if tpl := strOf(m["template"]); tpl != "" {
		if viewProvider == nil {
			http.Error(w, "模板能力未就绪", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(tpl, "..") || strings.HasPrefix(tpl, "/") {
			http.Error(w, "非法模板名", http.StatusBadRequest)
			return
		}
		html, err := viewProvider.RenderTemplate(os.DirFS(filepath.Join(dir(), app.Dir)), tpl, m["data"])
		if err != nil {
			noteFailure(app, err)
			http.Error(w, "模板渲染失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(html))
		return
	}
	if body := strOf(m["body"]); body != "" {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
		return
	}
	_ = fn
	w.WriteHeader(status)
}

// buildRequestCtx 构造传给脚本的 ctx 对象
func buildRequestCtx(r *http.Request, params map[string]string) map[string]any {
	query := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			query[k] = v[0]
		}
	}
	form := map[string]string{}
	_ = r.ParseForm()
	for k, v := range r.PostForm {
		if len(v) > 0 {
			form[k] = v[0]
		}
	}
	var jsonBody any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if b, err := io.ReadAll(io.LimitReader(r.Body, maxPluginBody)); err == nil && len(b) > 0 {
			_ = json.Unmarshal(b, &jsonBody)
		}
	}
	ctxMap := map[string]any{
		"req": map[string]any{
			"method":      r.Method,
			"path":        r.URL.Path,
			"query":       query,
			"form":        form,
			"json":        jsonBody,
			"ip":          util.ClientIP(r),
			"fingerprint": util.FingerprintOf(r),
		},
		"params": params,
	}
	if viewProvider != nil {
		uid := viewProvider.UserID(r)
		aid := viewProvider.AdminID(r)
		ctxMap["userId"] = uid
		ctxMap["adminId"] = aid
		ctxMap["isAdmin"] = aid > 0
		ctxMap["csrf"] = viewProvider.CSRF(r)
	}
	return ctxMap
}

func ctxToReqContext(r *http.Request, ctxMap map[string]any) *reqContext {
	rc := &reqContext{Method: r.Method, Path: r.URL.Path}
	if v, ok := ctxMap["userId"].(int64); ok {
		rc.UserID = v
	}
	if v, ok := ctxMap["adminId"].(int64); ok {
		rc.AdminID = v
		rc.IsAdmin = v > 0
	}
	if p, ok := ctxMap["params"].(map[string]string); ok {
		rc.Params = p
	}
	if req, ok := ctxMap["req"].(map[string]any); ok {
		if q, ok := req["query"].(map[string]string); ok {
			rc.Query = q
		}
		if f, ok := req["form"].(map[string]string); ok {
			rc.Form = f
		}
		rc.IP = strOf(req["ip"])
		rc.Fingerprint = strOf(req["fingerprint"])
	}
	rc.CSRF = strOf(ctxMap["csrf"])
	return rc
}

// servePluginAsset 插件静态资源（仅 assets 目录，防穿越）
func servePluginAsset(w http.ResponseWriter, r *http.Request, app *App, rel string) {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(dir(), app.Dir, "assets", filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "msg": msg})
}
