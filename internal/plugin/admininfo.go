// 面向后台管理页的运行时信息查询与模板覆盖支持。
package plugin

import (
	"errors"
	"os"
	"path/filepath"

	"clearlove/internal/database"
)

// JobInfo 后台展示用的定时任务信息
type JobInfo struct {
	Name  string `json:"name"`
	Every string `json:"every"`
}

// AppInfo 应用型插件在后台展示的运行时概况
type AppInfo struct {
	Running bool      `json:"running"`
	Routes  int       `json:"routes"`
	Pages   int       `json:"pages"`
	Menus   int       `json:"menus"`
	Slots   int       `json:"slots"`
	Events  int       `json:"events"`
	Filters int       `json:"filters"`
	Jobs    []JobInfo `json:"jobs"`
	Perms   []string  `json:"perms"`
	Fails   int       `json:"fails"` // 连续失败次数（熔断进度）
}

// AppInfoOf 查询某插件目录的运行时概况（未加载时 Running=false）
func AppInfoOf(dir string) AppInfo {
	a := GetApp(filepath.Base(dir))
	if a == nil || a.Plugin == nil {
		return AppInfo{}
	}
	info := AppInfo{
		Running: true,
		Perms:   a.Plugin.Permissions,
		Fails:   FailCount(a.Plugin.SlugOf()),
		Routes:  len(a.Routes),
		Pages:   len(a.Pages),
		Menus:   len(a.Menus),
	}
	for _, fns := range a.Slots {
		info.Slots += len(fns)
	}
	for _, fns := range a.Events {
		info.Events += len(fns)
	}
	for _, list := range a.Filters {
		info.Filters += len(list)
	}
	for _, j := range a.Jobs {
		info.Jobs = append(info.Jobs, JobInfo{Name: j.Name, Every: j.Every.String()})
	}
	return info
}

// EnabledTemplateDirs 返回已启用插件中带有 templates/ 目录的绝对路径。
// 内核按顺序重新解析这些目录下的模板，实现"插件覆盖内核同名模板片段"。
func EnabledTemplateDirs() []string {
	enabled := enabledSet()
	var out []string
	for name, p := range cachedPlugins() {
		if !enabled[p.Name] {
			continue
		}
		dir := filepath.Join(dir(), filepath.Base(name), "templates")
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

// PluginLogs 读取运行日志（后台日志页使用）
func PluginLogs(slug string, limit int) []map[string]any {
	if database.DB == nil {
		return nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := "SELECT id, plugin_slug, action, IFNULL(detail,'') AS detail, duration_ms, IFNULL(created_at,'') AS created_at FROM plugin_logs"
	var args []any
	if slug != "" {
		q += " WHERE plugin_slug=?"
		args = append(args, slug)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := queryMaps(database.DB, q, args)
	if err != nil {
		return nil
	}
	return rows
}

// ClearPluginLogs 清空运行日志
func ClearPluginLogs() error {
	if database.DB == nil {
		return errors.New("数据库未连接")
	}
	_, err := database.DB.Exec("DELETE FROM plugin_logs")
	return err
}
