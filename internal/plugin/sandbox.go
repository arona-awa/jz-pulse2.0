// 沙箱与治理：权限校验、审计日志、失败熔断。
package plugin

import (
	"errors"
	"strings"
	"sync"
	"time"

	"clearlove/internal/database"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

var errNoApp = errors.New("插件未加载")

// permError 权限未声明错误
type permError struct{ slug, perm string }

func (e *permError) Error() string {
	return "插件 " + e.slug + " 未声明权限 " + e.perm + "，请在 plugin.json 的 permissions 中声明后重试"
}

// requirePerm 运行时侧的权限校验入口（Host API 统一从这里进入）
func (rt *jsRuntime) requirePerm(perm string) error {
	if rt == nil || rt.app == nil {
		return errNoApp
	}
	return rt.app.requirePerm(perm)
}

// requirePerm 校验插件是否声明了指定权限（能力白名单的唯一入口）
func (a *App) requirePerm(perm string) error {
	if a == nil || a.Plugin == nil {
		return errNoApp
	}
	if !a.Plugin.HasPermission(perm) {
		return &permError{slug: a.Plugin.SlugOf(), perm: perm}
	}
	return nil
}

// logPlugin 写插件运行日志（失败静默，不阻断插件逻辑）
func logPlugin(slug, action, detail string, dur time.Duration) {
	if database.DB == nil {
		return
	}
	detail = strings.TrimSpace(detail)
	if len(detail) > 2000 {
		detail = detail[:2000]
	}
	_, _ = database.DB.Exec(
		"INSERT INTO plugin_logs(plugin_slug,action,detail,duration_ms,created_at) VALUES(?,?,?,?,?)",
		slug, action, detail, dur.Milliseconds(), models.Now())
}

// ---------- 失败熔断 ----------

const failThreshold = 20 // 连续失败阈值，达到后自动禁用插件

var (
	failMu    sync.Mutex
	failCount = map[string]int{}
)

// noteFailure 记录一次插件失败；达到阈值自动禁用，避免拖垮站点
func noteFailure(a *App, err error) {
	if a == nil || a.Plugin == nil {
		return
	}
	slug := a.Plugin.SlugOf()
	failMu.Lock()
	failCount[slug]++
	n := failCount[slug]
	failMu.Unlock()
	util.Log("warn", "插件 %s 执行失败(%d/%d): %v", slug, n, failThreshold, err)
	if n == failThreshold {
		util.Log("error", "插件 %s 连续失败达到 %d 次，已自动禁用", slug, failThreshold)
		logPlugin(slug, "disable", "连续失败自动禁用", 0)
		SetEnabled(a.Plugin.Name, false)
		UnloadApp(a.Dir)
		failMu.Lock()
		delete(failCount, slug)
		failMu.Unlock()
	}
}

// noteSuccess 重置失败计数
func noteSuccess(a *App) {
	if a == nil || a.Plugin == nil {
		return
	}
	failMu.Lock()
	delete(failCount, a.Plugin.SlugOf())
	failMu.Unlock()
}

// FailCount 查询插件当前连续失败次数（后台展示用）
func FailCount(slug string) int {
	failMu.Lock()
	defer failMu.Unlock()
	return failCount[slug]
}
