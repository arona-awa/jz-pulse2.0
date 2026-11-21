// 视图能力反向注入。
//
// 依赖方向是 handlers -> plugin，因此 plugin 不能 import handlers。
// 插件页面/模板渲染需要模板与会话能力，这里用接口由 handlers 在启动时注入。
package plugin

import (
	"io/fs"
	"net/http"
)

// ViewProvider 由 handlers 实现的视图能力
type ViewProvider interface {
	// RenderTemplate 渲染插件目录（fsys）下的模板片段
	RenderTemplate(fsys fs.FS, name string, data any) (string, error)
	// CSRF 当前请求的 CSRF 令牌（插件表单需要带上）
	CSRF(r *http.Request) string
	// UserID 当前登录用户 ID（未登录返回 0）
	UserID(r *http.Request) int64
	// AdminID 当前登录管理员 ID（未登录返回 0）
	AdminID(r *http.Request) int64
	// AdminPerm 当前管理员是否拥有指定权限
	AdminPerm(r *http.Request, perm string) bool
}

var viewProvider ViewProvider

// SetViewProvider 由 handlers 在模板初始化完成后调用
func SetViewProvider(v ViewProvider) { viewProvider = v }

// HasViewProvider 是否已完成装配（测试环境可能未装配）
func HasViewProvider() bool { return viewProvider != nil }
