// 用户个性化设置：主题 / 每行帖子数 / 字体大小，服务端持久化。
package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"clearlove/internal/models"
)

// APIPrefsGet 读取当前登录用户的设置；未登录返回默认值。
func APIPrefsGet(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	p := models.DefaultPrefs()
	saved := false
	if u != nil {
		p = models.GetPrefs(u.ID)
		saved = true
	}
	okJSON(w, map[string]any{"prefs": p, "saved": saved})
}

// APIPrefsSave 保存设置（需登录 + CSRF）。
func APIPrefsSave(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录后再保存个性化设置")
		return
	}
	var p models.Prefs
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
			fail(w, http.StatusBadRequest, "参数格式错误")
			return
		}
	} else {
		p.Theme = r.FormValue("theme")
		p.Columns, _ = strconv.Atoi(r.FormValue("columns"))
		p.FontScale, _ = strconv.Atoi(r.FormValue("font_scale"))
	}
	p = p.Normalize()
	if err := models.SavePrefs(u.ID, p); err != nil {
		fail(w, http.StatusInternalServerError, "保存失败，请稍后重试")
		return
	}
	okJSON(w, map[string]any{"prefs": p})
}
