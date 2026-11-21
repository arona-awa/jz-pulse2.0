// Package middleware 全局中间件：安全响应头、安装门禁、访问日志、
// CSRF 防护（双提交 Cookie）、发帖封禁检查。
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/models"
	"clearlove/internal/plugin"
	"clearlove/internal/util"
)

type ctxKey string

const csrfKey ctxKey = "csrf"

// CSRFToken 从请求上下文取出 CSRF 令牌（handlers 渲染表单时使用）
func CSRFToken(r *http.Request) string {
	if v, ok := r.Context().Value(csrfKey).(string); ok {
		return v
	}
	return ""
}

// Use 全局中间件入口
func Use(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 安全响应头
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// 安装门禁：未安装只放行安装向导与页面所需静态资源
		// （theme.css 必须放行，否则主题变量缺失会导致按钮等样式失效）
		if !config.Cfg.Installed {
			p := r.URL.Path
			ok := p == "/install" || p == "/theme.css" || p == "/favicon.svg" ||
				strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/uploads/")
			if !ok {
				http.Redirect(w, r, "/install", http.StatusFound)
				return
			}
		} else if r.URL.Path == "/install" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		// 静态资源不走 CSRF/Cookie/插件中间件：避免每个资源都 Set-Cookie，
		// 否则 Cloudflare 会判定为动态响应（cf-cache-status: BYPASS），
		// 浏览器和边缘节点都无法缓存 CSS/JS/图片。
		if isAssetPath(r.URL.Path) {
			setAssetCache(w, r)
			next.ServeHTTP(w, r)
			return
		}

		// 应用型插件中间件（http.before）：在 CSRF 校验之前执行，可短路响应
		if plugin.HasApps() && !strings.HasPrefix(r.URL.Path, "/static/") && !strings.HasPrefix(r.URL.Path, "/uploads/") {
			if res := plugin.RunMiddlewareBefore(map[string]any{
				"method":      r.Method,
				"path":        r.URL.Path,
				"query":       firstValues(r.URL.Query()),
				"ip":          util.ClientIP(r),
				"fingerprint": util.FingerprintOf(r),
			}); res != nil {
				for k, v := range res.Headers {
					w.Header().Set(k, v)
				}
				switch {
				case res.Redirect != "":
					http.Redirect(w, r, res.Redirect, http.StatusFound)
				case res.JSON != nil:
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(res.Status)
					_ = json.NewEncoder(w).Encode(res.JSON)
				case res.Body != "":
					w.WriteHeader(res.Status)
					_, _ = w.Write([]byte(res.Body))
				default:
					w.WriteHeader(res.Status)
				}
				return
			}
		}

		// CSRF 令牌：缺失则生成并写入 Cookie，同时放入上下文
		token := ""
		if c, err := r.Cookie("clv_csrf"); err == nil {
			token = c.Value
		}
		fresh := false
		if token == "" || len(token) != 32 {
			token = util.RandomHex(16)
			fresh = true
		}
		if fresh {
			http.SetCookie(w, &http.Cookie{
				Name: "clv_csrf", Value: token, Path: "/", MaxAge: 86400 * 30,
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
		}
		r = r.WithContext(context.WithValue(r.Context(), csrfKey, token))

		// 写操作校验
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			if config.Cfg.Installed && !exemptCSRF(r) {
				v := r.Header.Get("X-CSRF-Token")
				if v == "" {
					// 兼容 multipart：完整解析后读取表单值
					ct := r.Header.Get("Content-Type")
					if strings.HasPrefix(ct, "multipart/form-data") {
						_ = r.ParseMultipartForm(32 << 20)
					} else {
						_ = r.ParseForm()
					}
					v = r.PostFormValue("_csrf")
				}
				if v != token {
					http.Error(w, "CSRF 校验失败，请刷新页面重试", http.StatusForbidden)
					return
				}
			}
			// 封禁检查（发帖/评论/API发帖）
			p := r.URL.Path
			if p == "/compose" || p == "/api/v1/posts" || p == "/api/v1/comments" {
				if isBanned(util.ClientIP(r)) {
					http.Error(w, "您的网络环境已被封禁，无法发布内容", http.StatusForbidden)
					return
				}
			}
		}

		// 应用型插件的 http.after：需要拿到最终状态码，这里包装一层 ResponseWriter
		var sw *statusWriter
		if plugin.HasApps() {
			sw = &statusWriter{ResponseWriter: w, status: http.StatusOK}
			w = sw
		}
		next.ServeHTTP(w, r)
		if sw != nil {
			plugin.RunMiddlewareAfter(map[string]any{
				"method":      r.Method,
				"path":        r.URL.Path,
				"status":      sw.status,
				"ip":          util.ClientIP(r),
				"fingerprint": util.FingerprintOf(r),
				"duration_ms": time.Since(start).Milliseconds(),
			})
		}
		util.Log("info", "%s %s %s %s", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start).Round(time.Millisecond))
	})
}

// isAssetPath 判断是否为无需会话/CSRF 的只读静态资源。
func isAssetPath(p string) bool {
	return strings.HasPrefix(p, "/static/") ||
		strings.HasPrefix(p, "/uploads/") ||
		strings.HasPrefix(p, "/download/") ||
		p == "/theme.css" || p == "/favicon.svg" || p == "/favicon.ico"
}

// setAssetCache 为静态资源设置浏览器/边缘缓存。
// /static/ 的资源在模板中带 ?v=版本号，可安全长缓存；未带版本时降为 1 天。
func setAssetCache(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/static/"):
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
	case strings.HasPrefix(p, "/uploads/"):
		// 上传文件名随机，内容不会原地变化，可长期缓存。
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasPrefix(p, "/download/"):
		// APK 同 URL 更新时最多 1 小时缓存；下载地址的 ?v= 用于主动刷新。
		w.Header().Set("Cache-Control", "public, max-age=3600")
	case p == "/theme.css":
		// 主题可在后台即时切换，使用短缓存。
		w.Header().Set("Cache-Control", "public, max-age=300")
	case p == "/favicon.svg" || p == "/favicon.ico":
		w.Header().Set("Cache-Control", "public, max-age=604800")
	}
}

// statusWriter 记录响应状态码（http.after 使用）
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// exemptCSRF API Key 请求（第三方客户端）无需 CSRF
func exemptCSRF(r *http.Request) bool {
	k := r.Header.Get("X-API-Key")
	if k == "" {
		return false
	}
	var n int64
	err := database.DB.QueryRow("SELECT COUNT(1) FROM apikeys WHERE api_key=? AND status=1", k).Scan(&n)
	return err == nil && n > 0
}

// firstValues 把 query 转成一维 map（供插件中间件使用）
func firstValues(v url.Values) map[string]string {
	out := make(map[string]string, len(v))
	for k, list := range v {
		if len(list) > 0 {
			out[k] = list[0]
		}
	}
	return out
}

// isBanned 检查 IP 是否在封禁名单
func isBanned(ip string) bool {
	var n int64
	err := database.DB.QueryRow("SELECT COUNT(1) FROM bans WHERE btype='ip' AND bvalue=?", ip).Scan(&n)
	return err == nil && n > 0
}

// IsFingerprintBanned 供 handlers 检查浏览器指纹封禁
func IsFingerprintBanned(fp string) bool {
	if fp == "" {
		return false
	}
	var n int64
	err := database.DB.QueryRow("SELECT COUNT(1) FROM bans WHERE btype='fingerprint' AND bvalue=?", fp).Scan(&n)
	return err == nil && n > 0
}

// HasAPIKey 校验 API Key 并记录最近使用时间
func HasAPIKey(r *http.Request) bool {
	k := r.Header.Get("X-API-Key")
	if k == "" {
		return false
	}
	var n int64
	if err := database.DB.QueryRow("SELECT COUNT(1) FROM apikeys WHERE api_key=? AND status=1", k).Scan(&n); err != nil || n == 0 {
		return false
	}
	_, _ = database.DB.Exec("UPDATE apikeys SET last_used=? WHERE api_key=?", models.Now(), k)
	return true
}
