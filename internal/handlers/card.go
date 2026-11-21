// 主页名片：自定义头衔、签名与配色，服务端持久化。
package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"clearlove/internal/models"
	"clearlove/internal/util"
)

// APICardGet 读取当前登录用户的名片。
func APICardGet(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录")
		return
	}
	okJSON(w, map[string]any{"card": models.GetCard(u.ID)})
}

// APICardSave 保存名片（需登录 + CSRF）。
func APICardSave(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录后再编辑名片")
		return
	}
	var c models.UserCard
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
			fail(w, http.StatusBadRequest, "参数格式错误")
			return
		}
	} else {
		c.Title = r.FormValue("title")
		c.Bio = r.FormValue("bio")
		c.Style = r.FormValue("style")
	}
	c.Title = strings.TrimSpace(c.Title)
	c.Bio = strings.TrimSpace(c.Bio)
	if len([]rune(c.Title)) > 20 {
		fail(w, http.StatusBadRequest, "头衔最多 20 个字")
		return
	}
	if len([]rune(c.Bio)) > 120 {
		fail(w, http.StatusBadRequest, "签名最多 120 个字")
		return
	}
	c = c.Normalize()
	if err := models.SaveCard(u.ID, c); err != nil {
		fail(w, http.StatusInternalServerError, "保存失败，请稍后重试")
		return
	}
	okJSON(w, map[string]any{"card": models.GetCard(u.ID)})
}

// APICardCover 上传/替换主页名片封面。
func APICardCover(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录后再上传封面")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(w, http.StatusBadRequest, "图片过大或表单无效")
		return
	}
	f, fh, err := r.FormFile("cover")
	if err != nil {
		fail(w, http.StatusBadRequest, "请选择封面图片")
		return
	}
	defer f.Close()
	path, err := util.SaveCardCover(fh)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	old := models.GetCard(u.ID).Cover
	if err := models.SetCardCover(u.ID, path); err != nil {
		util.RemoveCardCover(path)
		fail(w, http.StatusInternalServerError, "保存封面失败，请稍后重试")
		return
	}
	util.RemoveCardCover(old)
	okJSON(w, map[string]any{"cover": path})
}

// APICardCoverDelete 移除主页名片封面。
func APICardCoverDelete(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录")
		return
	}
	old := models.GetCard(u.ID).Cover
	if err := models.SetCardCover(u.ID, ""); err != nil {
		fail(w, http.StatusInternalServerError, "移除失败，请稍后重试")
		return
	}
	util.RemoveCardCover(old)
	okJSON(w, map[string]any{"cover": ""})
}
