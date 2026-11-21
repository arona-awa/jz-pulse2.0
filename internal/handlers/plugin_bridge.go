// 把 handlers 的模板与会话能力注入插件运行时。
//
// 依赖方向是 handlers -> plugin，因此由 handlers 实现 plugin.ViewProvider
// 并反向注册，避免 plugin 包 import handlers 造成循环依赖。
package handlers

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"

	"clearlove/internal/middleware"
	"clearlove/internal/plugin"
)

type pluginViewProvider struct{}

// RenderTemplate 渲染插件目录下的模板文件
func (pluginViewProvider) RenderTemplate(fsys fs.FS, name string, data any) (string, error) {
	t, err := template.New(name).Funcs(tmplFuncs).ParseFS(fsys, name)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (pluginViewProvider) CSRF(r *http.Request) string { return middleware.CSRFToken(r) }

func (pluginViewProvider) UserID(r *http.Request) int64 {
	if u := CurrentUser(r); u != nil {
		return u.ID
	}
	return 0
}

func (pluginViewProvider) AdminID(r *http.Request) int64 {
	if a := CurrentAdmin(r); a != nil {
		return a.ID
	}
	return 0
}

func (pluginViewProvider) AdminPerm(r *http.Request, perm string) bool {
	a := CurrentAdmin(r)
	return a != nil && a.HasPerm(perm)
}

// pluginSlotData 构造传给插件 Slot 处理的轻量数据（避免整页数据序列化开销）
func pluginSlotData(r *http.Request, page string, data map[string]any) map[string]any {
	out := map[string]any{"page": page, "path": r.URL.Path}
	for _, k := range []string{"site", "topic", "notice"} {
		if v, ok := data[k]; ok {
			out[k] = v
		}
	}
	if u := CurrentUser(r); u != nil {
		out["user"] = map[string]any{"id": u.ID, "username": u.Username}
	}
	if a := CurrentAdmin(r); a != nil {
		out["admin"] = map[string]any{"id": a.ID, "username": a.Username, "role": a.Role}
	}
	// 详情页/个人主页把当前帖子（或列表）的轻量信息交给插件 Slot
	switch p := data["post"].(type) {
	case Card:
		out["post"] = map[string]any{"id": p.ID, "user_id": p.UserID, "nickname": p.Nickname}
	case *Card:
		if p != nil {
			out["post"] = map[string]any{"id": p.ID, "user_id": p.UserID, "nickname": p.Nickname}
		}
	}
	return out
}

var _ plugin.ViewProvider = pluginViewProvider{}

// ---------- 过滤器接入辅助 ----------

func strOfAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolOfAny(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "1" || t == "true"
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}

func intOfAny(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	default:
		return 0
	}
}

func anyToStrings(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// cardToMap 把帖子卡片转成插件可读写的普通对象
func cardToMap(c *Card) map[string]any {
	return map[string]any{
		"id":            c.ID,
		"user_id":       c.UserID,
		"nickname":      c.Nickname,
		"avatar":        c.Avatar,
		"content":       c.Content,
		"topic":         c.Topic,
		"images":        c.Images,
		"videos":        c.Videos,
		"likes":         c.Likes,
		"comment_count": c.CommentCount,
		"is_admin":      c.IsAdmin,
		"badges":        c.Badges,
		"created_at":    c.CreatedAt,
	}
}

// mapToCard 把插件改写后的字段写回卡片（id/user_id 不允许改）
func mapToCard(m map[string]any, c *Card) {
	if v, ok := m["nickname"].(string); ok && v != "" {
		c.Nickname = v
	}
	if v, ok := m["content"].(string); ok {
		c.Content = v
	}
	if v, ok := m["topic"].(string); ok {
		c.Topic = v
	}
	if v, ok := m["likes"]; ok {
		c.Likes = intOfAny(v)
	}
	if v, ok := m["comment_count"]; ok {
		c.CommentCount = intOfAny(v)
	}
	if v, ok := m["images"]; ok {
		c.Images = anyToStrings(v)
	}
	if v, ok := m["videos"]; ok {
		c.Videos = anyToStrings(v)
	}
	if v, ok := m["badges"]; ok {
		if list := anyToStrings(v); list != nil {
			c.Badges = list
		}
	}
}

// applyCardFilter 让插件改写卡片数据（filter.card）
func applyCardFilter(c *Card) {
	if !plugin.HasApps() || c == nil {
		return
	}
	out := plugin.ApplyFilterValue("filter.card", cardToMap(c), nil)
	if m, ok := out.(map[string]any); ok {
		mapToCard(m, c)
	}
}
