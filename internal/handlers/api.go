// RESTful API（/api/v1）：覆盖帖子列表/详情/发布/上传/点赞/评论/话题/举报。
// 鉴权策略：
//   - 读取类接口（列表/详情/话题）与匿名互动（点赞/评论/举报）无需鉴权，与前台行为一致；
//   - 发布帖子与媒体上传需要 已登录会话 或 X-API-Key（供第三方客户端调用，防刷）。
package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"clearlove/internal/ai"
	"clearlove/internal/database"
	"clearlove/internal/middleware"
	"clearlove/internal/models"
	postnotify "clearlove/internal/notify"
	"clearlove/internal/plugin"
	"clearlove/internal/util"
)

// apiAuth 需鉴权的接口使用：API Key（第三方客户端）或已登录会话
func apiAuth(r *http.Request) (*models.User, bool) {
	if middleware.HasAPIKey(r) {
		return CurrentUser(r), true // Key 合法即放行（用户可为空=匿名身份）
	}
	if u := CurrentUser(r); u != nil {
		return u, true
	}
	return nil, false
}

// CaptchaJSON 获取人机验证码（内联 SVG）
func CaptchaJSON(w http.ResponseWriter, r *http.Request) {
	id, svg := util.NewCaptcha()
	JSON(w, http.StatusOK, map[string]any{"id": id, "svg": svg})
}

// APIListPosts 帖子分页列表（瀑布流无限滚动数据源，公开访问）
func APIListPosts(w http.ResponseWriter, r *http.Request) {
	before := formInt64(r, "before")
	limit := int(formInt64(r, "limit"))
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	okJSON(w, map[string]any{"posts": fetchCards(before, limit, r.URL.Query().Get("topic"))})
}

// APIGetPost 帖子详情（含全部评论，公开访问）
func APIGetPost(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	if !isVisible(id) {
		fail(w, http.StatusNotFound, "帖子不存在或不可见")
		return
	}
	okJSON(w, map[string]any{"post": singleCard(id), "comments": allComments(id)})
}

// APILike 点赞/取消点赞（匿名可用，按浏览器指纹去重）
func APILike(w http.ResponseWriter, r *http.Request) {
	postID := pathInt64(r, "id")
	if models.QueryInt("SELECT COUNT(1) FROM posts WHERE id=? AND status=1", postID) == 0 {
		fail(w, 404, "帖子不存在或不可见")
		return
	}
	// 点赞身份：优先浏览器指纹，登录用户回退到账号 ID
	fp := util.FingerprintOf(r)
	if fp == "" {
		if u := CurrentUser(r); u != nil {
			fp = "u" + int64ToStr(u.ID)
		}
	}
	if fp == "" {
		fail(w, 400, "无法识别点赞身份，请刷新页面后重试")
		return
	}
	n, liked := toggleLike(postID, fp)
	if liked {
		if u := CurrentUser(r); u != nil {
			models.AwardDailyLike(u.ID)
		}
	}
	okJSON(w, map[string]any{"likes": n, "liked": liked})
}

// APICoins 当前用户的硬币余额（会触发每日自动 +1）。
func APICoins(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录")
		return
	}
	okJSON(w, map[string]any{
		"ok":             true,
		"balance_tenths": u.CoinTenths,
		"balance":        models.FormatCoins(u.CoinTenths),
	})
}

// APITip 给帖子作者投币，可同时点赞。
func APITip(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		Coins    int  `json:"coins"`
		WithLike bool `json:"with_like"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "参数格式不正确")
		return
	}
	if body.Coins != 1 && body.Coins != 2 {
		fail(w, http.StatusBadRequest, "每次只能投 1 或 2 枚硬币")
		return
	}
	res, err := models.TipPost(pathInt64(r, "id"), u.ID, body.Coins, body.WithLike, util.FingerprintOf(r))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	okJSON(w, map[string]any{
		"ok":             true,
		"balance_tenths": res.BalanceTenths,
		"balance":        models.FormatCoins(res.BalanceTenths),
		"coins":          res.Coins,
		"sender_xp":      res.SenderXP,
		"recipient_xp":   res.RecipientXP,
		"recipient_id":   res.RecipientID,
		"likes":          res.Likes,
		"liked":          res.Liked,
		"post_coins":     res.PostCoins,
	})
}

// int64ToStr 快速整型转字符串
func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// APIComment 发表评论（匿名可用）
func APIComment(w http.ResponseWriter, r *http.Request) {
	if middleware.IsFingerprintBanned(util.FingerprintOf(r)) {
		fail(w, 403, "您的浏览器指纹已被封禁")
		return
	}
	postID := formInt64(r, "post_id")
	content := plugin.ApplyFilterStr("filter.content", util.StripHTML(r.FormValue("content")))
	cid, errMsg := addComment(postID, util.StripHTML(r.FormValue("nickname")), content, util.FingerprintOf(r), CurrentUser(r))
	if errMsg != "" {
		fail(w, 400, errMsg)
		return
	}
	okJSON(w, map[string]any{"id": cid})
}

// APITopics 话题列表
func APITopics(w http.ResponseWriter, r *http.Request) {
	okJSON(w, map[string]any{"topics": listTopics()})
}

// APIReport 提交举报（匿名可用）
func APIReport(w http.ResponseWriter, r *http.Request) {
	postID := formInt64(r, "post_id")
	reason := util.StripHTML(r.FormValue("reason"))
	if models.QueryInt("SELECT COUNT(1) FROM posts WHERE id=?", postID) == 0 {
		fail(w, 404, "帖子不存在")
		return
	}
	_, err := database.DB.Exec(
		"INSERT INTO reports(post_id,reason,reporter_ip,status,created_at) VALUES(?,?,?,0,?)",
		postID, reason, util.ClientIP(r), models.Now())
	if err != nil {
		fail(w, 500, "举报提交失败")
		return
	}
	plugin.Emit("report_created", map[string]any{"post_id": postID, "reason": reason})
	okJSON(w, map[string]any{"msg": "举报已提交，感谢您的监督"})
}

// APICreatePost API 发帖（JSON 体，供第三方客户端；媒体经 /api/v1/upload 预传）
func APICreatePost(w http.ResponseWriter, r *http.Request) {
	u, ok := apiAuth(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "请先登录或携带 API Key")
		return
	}
	if middleware.IsFingerprintBanned(r.Header.Get("X-Fingerprint")) {
		fail(w, 403, "您的浏览器指纹已被封禁")
		return
	}
	var body struct {
		Content  string   `json:"content"`
		Nickname string   `json:"nickname"`
		Topic    string   `json:"topic"`
		Images   []string `json:"images"`
		Videos   []string `json:"videos"`
		// 使用管理员专享昵称时必填
		AdminUser string `json:"admin_user"`
		AdminPass string `json:"admin_pass"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		fail(w, 400, "JSON 格式错误")
		return
	}
	content := plugin.ApplyFilterStr("filter.content", util.StripHTML(body.Content))
	if content == "" || len([]rune(content)) > 3000 {
		fail(w, 400, "帖子内容需为 1-3000 字")
		return
	}
	if len(body.Images) > 15 || len(body.Videos) > 3 {
		fail(w, 400, "图片最多15张、视频最多3个")
		return
	}
	// AI 预审（含图片，需多模态模型；失败按 ai_precheck_fail 策略：open 放行 / closed 拦截）
	if models.GetSetting("ai_precheck") == "1" {
		bad, reason, err := ai.Moderate(content, body.Images)
		if err != nil {
			util.Log("warn", "AI API 预审失败: %v", err)
			if models.GetSetting("ai_precheck_fail") == "closed" {
				fail(w, http.StatusServiceUnavailable, "自动审核服务暂时不可用，请稍后再试")
				return
			}
		} else if bad {
			fail(w, 422, "内容未通过自动审核："+reason)
			return
		}
	}
	nickname := util.StripHTML(body.Nickname)
	if nickname == "" {
		nickname = "匿名"
	}
	// 发帖守卫：内置「管理员专享昵称」+ 插件 compose_guard
	isAdmin, badgeList, code, gErr := guardCompose(nickname,
		strings.TrimSpace(body.AdminUser), body.AdminPass, util.ClientIP(r))
	if gErr != "" {
		fail(w, code, gErr)
		return
	}
	topicID := int64(0)
	if t := util.StripHTML(body.Topic); t != "" {
		topicID = ensureTopic(t)
	}
	res, err := database.DB.Exec(
		"INSERT INTO posts(user_id,nickname,content,topic_id,ip,fingerprint,status,is_admin,badges,created_at) VALUES(?,?,?,?,?,?,1,?,?,?)",
		userIDOf(u), nickname, content, topicID, util.ClientIP(r), r.Header.Get("X-Fingerprint"),
		isAdmin, models.BadgesJSON(badgeList), models.Now())
	if err != nil {
		fail(w, 500, "发布失败")
		return
	}
	pid, _ := res.LastInsertId()
	models.AwardDailyPost(userIDOf(u))
	for _, p := range body.Images {
		if strings.HasPrefix(p, "/uploads/") {
			_, _ = database.DB.Exec("INSERT INTO medias(post_id,type,path,created_at) VALUES(?,?,?,?)", pid, "image", p, models.Now())
		}
	}
	for _, p := range body.Videos {
		if strings.HasPrefix(p, "/uploads/") {
			_, _ = database.DB.Exec("INSERT INTO medias(post_id,type,path,created_at) VALUES(?,?,?,?)", pid, "video", p, models.Now())
		}
	}
	plugin.Emit("post_created", map[string]any{"post_id": pid, "nickname": nickname, "content": content})
	postnotify.NewPost(pid, userIDOf(u), nickname, content)
	okJSON(w, map[string]any{"id": pid})
}

// APIUpload API 媒体上传（multipart，字段 file），返回可访问路径
func APIUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := apiAuth(r); !ok {
		fail(w, http.StatusUnauthorized, "请携带 API Key")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 75<<20)
	f, fh, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "缺少文件字段 file")
		return
	}
	f.Close()
	var path string
	switch {
	case strings.HasPrefix(fh.Header.Get("Content-Type"), "video/"):
		path, err = util.SaveVideo(fh)
	default:
		path, err = util.SaveImage(fh)
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	okJSON(w, map[string]any{"url": path})
}
