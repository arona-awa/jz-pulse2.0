// 前台：首页瀑布流、帖子详情、发帖（含 WebP 转换与人机验证）、
// 点赞、评论、举报、个人主页（含头像裁剪上传）、帖子编辑删除。
package handlers

import (
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"clearlove/internal/ai"
	"clearlove/internal/database"
	"clearlove/internal/middleware"
	"clearlove/internal/models"
	postnotify "clearlove/internal/notify"
	"clearlove/internal/plugin"
	"clearlove/internal/util"
)

// ---------- 管理员专享昵称：凭证校验与限流 ----------

// adminTry 单个 IP 的失败计数（防止在线爆破后台密码）
type adminTry struct {
	Count int
	Until time.Time
}

var (
	adminTryMu sync.Mutex
	adminTries = map[string]adminTry{}
)

// verifyAdminCreds 校验后台管理员账号密码（仅校验，不建立会话）
func verifyAdminCreds(username, password string) bool {
	if username == "" || password == "" {
		return false
	}
	var hash string
	if err := database.DB.QueryRow("SELECT password FROM admins WHERE username=?", username).Scan(&hash); err != nil {
		return false
	}
	return util.CheckPassword(hash, password)
}

// adminTryBlocked 是否因失败次数过多被暂时锁定（5 次 / 10 分钟）
func adminTryBlocked(ip string) bool {
	adminTryMu.Lock()
	defer adminTryMu.Unlock()
	t, ok := adminTries[ip]
	if !ok {
		return false
	}
	if time.Now().After(t.Until) {
		delete(adminTries, ip)
		return false
	}
	return t.Count >= 5
}

func adminTryFail(ip string) {
	adminTryMu.Lock()
	defer adminTryMu.Unlock()
	t := adminTries[ip]
	t.Count++
	t.Until = time.Now().Add(10 * time.Minute)
	adminTries[ip] = t
}

func adminTryReset(ip string) {
	adminTryMu.Lock()
	defer adminTryMu.Unlock()
	delete(adminTries, ip)
}

// guardCompose 执行发帖守卫：内置「管理员专享昵称」+ 插件 compose_guard 钩子。
// 返回 (是否管理员帖, 帖子标识列表, 错误码, 错误提示)
func guardCompose(nickname, adminUser, adminPass, ip string) (int, []string, int, string) {
	labels := plugin.NicknameBadges(nickname)
	needAdmin := false
	msg := ""

	if models.IsAdminNickname(nickname) {
		needAdmin = true
		labels = append(labels, "管理员")
		msg = "「" + nickname + "」为管理员专享昵称，请填写正确的后台账号与密码"
	}
	if g := plugin.CheckComposeGuard(nickname); g != nil {
		labels = append(labels, g.Labels...)
		if g.Require {
			needAdmin = true
			if msg == "" {
				msg = g.Message
			}
		}
	}
	// 应用型插件可追加/放宽守卫要求（filter.compose.guard）
	if plugin.HasApps() {
		out := plugin.ApplyFilterValue("filter.compose.guard", map[string]any{
			"require":  needAdmin,
			"nickname": nickname,
			"labels":   labels,
			"message":  msg,
		}, nil)
		if m, ok := out.(map[string]any); ok {
			needAdmin = boolOfAny(m["require"])
			if v := strOfAny(m["message"]); v != "" {
				msg = v
			}
			if list := anyToStrings(m["labels"]); list != nil {
				labels = list
			}
		}
	}
	if !needAdmin {
		return 0, models.MergeBadges(labels), 0, ""
	}
	if msg == "" {
		msg = "该昵称为管理员专享，请填写正确的后台账号与密码"
	}
	if adminTryBlocked(ip) {
		return 0, nil, 429, "验证失败次数过多，请 10 分钟后再试"
	}
	if !verifyAdminCreds(adminUser, adminPass) {
		adminTryFail(ip)
		return 0, nil, 403, msg
	}
	adminTryReset(ip)
	return 1, models.MergeBadges(labels), 0, ""
}

// CommentItem 评论预览项
type CommentItem struct {
	Nickname  string `json:"nickname"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// Card 帖子卡片（模板与 API 共用）
type Card struct {
	ID           int64         `json:"id"`
	UserID       int64         `json:"user_id"`
	Nickname     string        `json:"nickname"`
	Avatar       string        `json:"avatar"`
	Level        int           `json:"level"`
	Content      string        `json:"content"`
	Topic        string        `json:"topic"`
	Images       []string      `json:"images"`
	Videos       []string      `json:"videos"`
	Likes        int64         `json:"likes"`
	CommentCount int64         `json:"comment_count"`
	Coins        int64         `json:"coins"`
	IsAdmin      int           `json:"is_admin"`
	BadgesRaw    string        `json:"-"`
	Badges       []string      `json:"badges"`
	Comments     []CommentItem `json:"comments"`
	CreatedAt    string        `json:"created_at"`
}

// latestNotice 获取最新一条公告
func latestNotice() (int64, string, string) {
	var id int64
	var title, content string
	err := database.DB.QueryRow(
		"SELECT id,IFNULL(title,''),IFNULL(content,'') FROM notices ORDER BY id DESC LIMIT 1").Scan(&id, &title, &content)
	if err != nil {
		return 0, "", ""
	}
	return id, title, content
}

// fetchCards 分页拉取可见帖子（before=0 表示从最新开始），可按话题过滤，
// 附带媒体与最新4条评论。
func fetchCards(before int64, limit int, topic string) []Card {
	q := `
		SELECT p.id,p.user_id,IFNULL(p.nickname,'匿名'),IFNULL(p.content,''),IFNULL(t.name,''),
		       p.like_count,p.comment_count,p.coin_count,IFNULL(p.is_admin,0),IFNULL(p.badges,''),p.created_at,
		       IFNULL(u.avatar,''),IFNULL(u.exp,0)
		FROM posts p LEFT JOIN topics t ON t.id=p.topic_id
		LEFT JOIN users u ON u.id=p.user_id
		WHERE p.status=1 AND (? <= 0 OR p.id < ?)`
	args := []any{before, before}
	if topic != "" {
		q += " AND t.name=?"
		args = append(args, topic)
	}
	q += " ORDER BY p.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := database.DB.Query(q, args...)
	if err != nil {
		util.Log("error", "查询帖子失败: %v", err)
		return nil
	}
	defer rows.Close()
	var order []int64
	byID := map[int64]*Card{}
	for rows.Next() {
		c := Card{Images: []string{}, Videos: []string{}, Comments: []CommentItem{}}
		var exp int
		if err := rows.Scan(&c.ID, &c.UserID, &c.Nickname, &c.Content, &c.Topic,
			&c.Likes, &c.CommentCount, &c.Coins, &c.IsAdmin, &c.BadgesRaw, &c.CreatedAt, &c.Avatar, &exp); err == nil {
			c.Level = models.LevelForExp(exp).Num
			order = append(order, c.ID)
			byID[c.ID] = &c
		}
	}
	if len(order) == 0 {
		return []Card{}
	}
	// 批量查媒体
	ph, args := buildIN(order)
	mrows, err := database.DB.Query(
		"SELECT post_id,type,path FROM medias WHERE post_id IN ("+ph+") ORDER BY id", args...)
	if err == nil {
		defer mrows.Close()
		for mrows.Next() {
			var pid int64
			var typ, path string
			if mrows.Scan(&pid, &typ, &path) == nil {
				if c := byID[pid]; c != nil {
					if typ == "video" {
						c.Videos = append(c.Videos, path)
					} else {
						c.Images = append(c.Images, path)
					}
				}
			}
		}
	}
	// 批量查每帖最新4条评论（倒序取前4再反转）
	crows, err := database.DB.Query(
		"SELECT post_id,IFNULL(nickname,'匿名'),IFNULL(content,''),IFNULL(created_at,'') FROM comments WHERE status=1 AND post_id IN ("+ph+") ORDER BY post_id, id DESC", args...)
	if err == nil {
		defer crows.Close()
		counts := map[int64]int{}
		for crows.Next() {
			var pid int64
			var it CommentItem
			if crows.Scan(&pid, &it.Nickname, &it.Content, &it.CreatedAt) == nil && counts[pid] < 4 {
				counts[pid]++
				if c := byID[pid]; c != nil {
					c.Comments = append(c.Comments, it)
				}
			}
		}
	}
	out := make([]Card, 0, len(order))
	for _, id := range order {
		c := byID[id]
		c.Badges = models.DisplayBadges(int64(c.IsAdmin), c.BadgesRaw)
		// 评论按时间正序展示
		for i, j := 0, len(c.Comments)-1; i < j; i, j = i+1, j-1 {
			c.Comments[i], c.Comments[j] = c.Comments[j], c.Comments[i]
		}
		applyCardFilter(c)
		out = append(out, *c)
	}
	return out
}

// buildIN 生成 "?,?,?" 占位符
func buildIN(ids []int64) (string, []any) {
	ps := make([]string, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ps[i] = "?"
		args = append(args, id)
	}
	return strings.Join(ps, ","), args
}

// Index 首页（支持 ?topic= 过滤，瀑布流首屏由前端加载）
func Index(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"topics": listTopics(),
		"topic":  r.URL.Query().Get("topic"),
	}
	if id, title, content := latestNotice(); id > 0 {
		data["notice"] = map[string]any{"id": id, "title": title, "content": content}
	}
	Render(w, r, "layout.html", "pg_index", data)
}

// listTopics 全部话题
func listTopics() []map[string]any {
	out := []map[string]any{}
	rows, err := database.DB.Query("SELECT id,name FROM topics ORDER BY id")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			out = append(out, map[string]any{"id": id, "name": name})
		}
	}
	return out
}

// PostDetail 帖子详情页（含全部评论与社区守则）
func PostDetail(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	a := CurrentAdmin(r)
	u := CurrentUser(r)
	// 隐藏/删除的帖子仅管理员可见
	if !isVisible(id) && a == nil {
		http.Error(w, "该帖子不可见", http.StatusNotFound)
		return
	}
	card := singleCard(id)
	if card == nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{
		"post":     *card,
		"comments": allComments(id),
		"canEdit":  (u != nil && u.ID == card.UserID) || a != nil,
		"rules":    models.GetSetting("community_rules"),
	}
	Render(w, r, "layout.html", "pg_detail", data)
}

// isVisible 帖子是否可见（应用型插件可通过 filter.post.visible 追加可见性条件）
func isVisible(id int64) bool {
	ok := models.QueryInt("SELECT COUNT(1) FROM posts WHERE id=? AND status=1", id) > 0
	if !ok || !plugin.HasApps() {
		return ok
	}
	return plugin.ApplyFilterBool("filter.post.visible", ok, map[string]any{"post_id": id})
}

// singleCard 按 ID 构建单个卡片（详情页用，含媒体与评论）
func singleCard(id int64) *Card {
	p := loadPost(id)
	if p == nil {
		return nil
	}
	c := &Card{
		ID: p.ID, UserID: p.UserID, Nickname: p.Nickname, Content: p.Content,
		Likes: p.LikeCount, CommentCount: p.CommentCount, IsAdmin: p.IsAdmin,
		Coins:     models.QueryInt("SELECT IFNULL(coin_count,0) FROM posts WHERE id=?", id),
		Badges:    models.DisplayBadges(int64(p.IsAdmin), p.BadgesRaw),
		CreatedAt: p.CreatedAt,
		Images:    []string{}, Videos: []string{}, Comments: []CommentItem{},
	}
	c.Topic = models.QueryStr("SELECT name FROM topics WHERE id=?", p.TopicID)
	if p.UserID > 0 {
		var exp int
		if err := database.DB.QueryRow("SELECT IFNULL(avatar,''),IFNULL(exp,0) FROM users WHERE id=?", p.UserID).
			Scan(&c.Avatar, &exp); err == nil {
			c.Level = models.LevelForExp(exp).Num
		}
	}
	rows, err := database.DB.Query("SELECT type,path FROM medias WHERE post_id=? ORDER BY id", id)
	if err == nil {
		for rows.Next() {
			var typ, path string
			if rows.Scan(&typ, &path) == nil {
				if typ == "video" {
					c.Videos = append(c.Videos, path)
				} else {
					c.Images = append(c.Images, path)
				}
			}
		}
		rows.Close()
	}
	c.Comments = nil
	for _, cm := range allComments(id) {
		c.Comments = append(c.Comments, CommentItem{
			Nickname: cm["nickname"].(string), Content: cm["content"].(string), CreatedAt: cm["created_at"].(string),
		})
	}
	applyCardFilter(c)
	return c
}

// allComments 帖子全部可见评论（正序）
func allComments(postID int64) []map[string]any {
	out := []map[string]any{}
	rows, err := database.DB.Query(
		"SELECT IFNULL(nickname,'匿名'),IFNULL(content,''),IFNULL(created_at,'') FROM comments WHERE post_id=? AND status=1 ORDER BY id", postID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n, c, t string
		if rows.Scan(&n, &c, &t) == nil {
			out = append(out, map[string]any{"nickname": n, "content": c, "created_at": t})
		}
	}
	return out
}

// ComposePage 发帖页
func ComposePage(w http.ResponseWriter, r *http.Request) {
	if models.GetSetting("require_login_post") == "1" && CurrentUser(r) == nil {
		http.Redirect(w, r, "/login?next=/compose", http.StatusFound)
		return
	}
	Render(w, r, "layout.html", "pg_compose", map[string]any{
		"topics":       listTopics(),
		"requireLogin": models.GetSetting("require_login_post") == "1",
		"maxImages":    15, "maxVideos": 3,
		// AI 发帖预审是否生效（开启且模型已接入）——前端据此展示审核动画
		"aiPrecheck": models.GetSetting("ai_precheck") == "1" && ai.Enabled(),
		// 需要验证后台账号的昵称（内置管理员昵称 + 插件守卫），
		// 前端据此在提交时弹出账号验证弹窗
		"adminNicks": strings.Join(
			append(models.AdminNicknames(), plugin.GuardNicknames()...), ","),
	})
}

// ComposeSubmit 发帖提交（multipart）：验证码校验 -> 内容校验 -> 媒体处理 ->
// 封禁/预审 -> 入库。AJAX 请求返回 JSON，普通表单返回 303 重定向。
func ComposeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 330<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, 400, "上传内容过大或表单无效")
		return
	}
	fp := util.FingerprintOf(r)
	if middleware.IsFingerprintBanned(fp) {
		fail(w, 403, "您的浏览器指纹已被封禁")
		return
	}
	if !util.VerifyCaptcha(r.FormValue("captcha_id"), r.FormValue("captcha_code")) {
		fail(w, 400, "验证码错误或已过期")
		return
	}
	content := plugin.ApplyFilterStr("filter.content", util.StripHTML(r.FormValue("content")))
	if content == "" || len([]rune(content)) > 3000 {
		fail(w, 400, "帖子内容需为 1-3000 字")
		return
	}
	// 昵称：开启登录发帖且已登录时自动匿名
	nickname := util.StripHTML(r.FormValue("nickname"))
	if len([]rune(nickname)) > 20 {
		nickname = string([]rune(nickname)[:20])
	}
	user := CurrentUser(r)
	if user == nil && models.GetSetting("require_login_post") == "1" {
		fail(w, 401, "请先登录后发帖")
		return
	}
	if user != nil && models.GetSetting("require_login_post") == "1" {
		nickname = "匿名"
	}
	if nickname == "" {
		nickname = "匿名"
	}

	// 发帖守卫：内置「管理员专享昵称」+ 插件 compose_guard
	isAdmin, badgeList, code, gErr := guardCompose(nickname,
		strings.TrimSpace(r.FormValue("admin_user")), r.FormValue("admin_pass"), util.ClientIP(r))
	if gErr != "" {
		fail(w, code, gErr)
		return
	}

	// 先全量校验文件（数量/大小/格式），再落盘，避免部分文件写入后失败
	imgs := r.MultipartForm.File["images"]
	vids := r.MultipartForm.File["videos"]
	if len(imgs) > util.MaxImages {
		fail(w, 400, "图片最多上传 15 张")
		return
	}
	if len(vids) > util.MaxVideos {
		fail(w, 400, "视频最多上传 3 个")
		return
	}
	for _, fh := range imgs {
		ext := strings.ToLower(path.Ext(fh.Filename))
		if !util.IsImageExt(ext) {
			fail(w, 400, "不支持的图片格式: "+ext)
			return
		}
		if fh.Size > util.MaxImageSize {
			fail(w, 400, "单张图片不能超过 10MB")
			return
		}
	}
	for _, fh := range vids {
		ext := strings.ToLower(path.Ext(fh.Filename))
		if !util.IsVideoExt(ext) {
			fail(w, 400, "不支持的视频格式: "+ext)
			return
		}
		if fh.Size > util.MaxVideoSize {
			fail(w, 400, "单个视频不能超过 70MB")
			return
		}
	}
	var media []struct {
		typ  string
		path string
	}
	var mediaPaths []string // 供 AI 图片审核
	for _, fh := range imgs {
		p, err := util.SaveImage(fh)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		mediaPaths = append(mediaPaths, p)
		media = append(media, struct {
			typ  string
			path string
		}{"image", p})
	}
	for _, fh := range vids {
		p, err := util.SaveVideo(fh)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		media = append(media, struct {
			typ  string
			path string
		}{"video", p})
	}

	// 话题：选择已有或新建
	topicID := formInt64(r, "topic_id")
	if newName := util.StripHTML(r.FormValue("topic_new")); newName != "" && len([]rune(newName)) <= 30 {
		topicID = ensureTopic(newName)
	}

	// AI 发帖预审（含图片，需多模态模型；失败按 ai_precheck_fail 策略：open 放行 / closed 拦截）
	if models.GetSetting("ai_precheck") == "1" {
		bad, reason, err := ai.Moderate(content, mediaPaths)
		if err != nil {
			util.Log("warn", "AI 发帖预审失败: %v", err)
			if models.GetSetting("ai_precheck_fail") == "closed" {
				fail(w, 503, "自动审核服务暂时不可用，请稍后再试")
				return
			}
		} else if bad {
			fail(w, 422, "内容未通过自动审核："+reason+"，请修改后重新发布")
			return
		}
	}

	// 入库
	res, err := database.DB.Exec(
		"INSERT INTO posts(user_id,nickname,content,topic_id,ip,fingerprint,status,is_admin,badges,created_at) VALUES(?,?,?,?,?,?,1,?,?,?)",
		userIDOf(user), nickname, content, topicID, util.ClientIP(r), fp, isAdmin,
		models.BadgesJSON(badgeList), models.Now())
	if err != nil {
		fail(w, 500, "发布失败，请稍后重试")
		return
	}
	pid, _ := res.LastInsertId()
	models.AwardDailyPost(userIDOf(user))
	for _, m := range media {
		_, _ = database.DB.Exec(
			"INSERT INTO medias(post_id,type,path,created_at) VALUES(?,?,?,?)", pid, m.typ, m.path, models.Now())
	}
	plugin.Emit("post_created", map[string]any{"post_id": pid, "nickname": nickname, "content": content})
	postnotify.NewPost(pid, userIDOf(user), nickname, content)
	if isAJAX(r) {
		okJSON(w, map[string]any{"redirect": "/", "id": pid})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// userIDOf 空用户返回 0
func userIDOf(u *models.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

// ensureTopic 话题不存在则创建，返回话题 ID
func ensureTopic(name string) int64 {
	id := models.QueryInt("SELECT id FROM topics WHERE name=?", name)
	if id > 0 {
		return id
	}
	res, err := database.DB.Exec("INSERT INTO topics(name,created_at) VALUES(?,?)", name, models.Now())
	if err != nil {
		return 0
	}
	id, _ = res.LastInsertId()
	return id
}

// toggleLike 点赞/取消（按指纹去重，支持匿名）
func toggleLike(postID int64, fp string) (int64, bool) {
	if fp == "" {
		return 0, false
	}
	exists := models.QueryInt("SELECT COUNT(1) FROM likes WHERE post_id=? AND fingerprint=?", postID, fp)
	liked := exists == 0
	if exists > 0 {
		_, _ = database.DB.Exec("DELETE FROM likes WHERE post_id=? AND fingerprint=?", postID, fp)
	} else {
		// 唯一索引 idx_likes_uniq 拦截并发重复点赞，冲突报错直接忽略（等价于已被并发请求点亮）
		_, _ = database.DB.Exec(
			"INSERT INTO likes(post_id,fingerprint,created_at) VALUES(?,?,?)", postID, fp, models.Now())
	}
	// 单语句内重算计数，避免"读-改-写"交错导致 like_count 与实际行数永久漂移
	_, _ = database.DB.Exec(
		"UPDATE posts SET like_count=(SELECT COUNT(1) FROM likes WHERE post_id=?) WHERE id=?", postID, postID)
	n := models.QueryInt("SELECT like_count FROM posts WHERE id=?", postID)
	plugin.Emit("like_toggled", map[string]any{"post_id": postID, "liked": liked, "count": n})
	return n, liked
}

// addComment 发表评论
func addComment(postID int64, nickname, content, fp string, u *models.User) (int64, string) {
	content = util.StripHTML(content)
	if content == "" || len([]rune(content)) > 500 {
		return 0, "评论需为 1-500 字"
	}
	if models.QueryInt("SELECT COUNT(1) FROM posts WHERE id=? AND status=1", postID) == 0 {
		return 0, "帖子不存在或不可见"
	}
	// AI 评论预审（失败按 ai_precheck_fail 策略：open 放行 / closed 拦截）
	if models.GetSetting("ai_precheck") == "1" {
		bad, reason, err := ai.Moderate(content, nil)
		if err != nil {
			util.Log("warn", "AI 评论预审失败: %v", err)
			if models.GetSetting("ai_precheck_fail") == "closed" {
				return 0, "自动审核服务暂时不可用，请稍后再试"
			}
		} else if bad {
			return 0, "评论未通过自动审核：" + reason
		}
	}
	if nickname == "" {
		nickname = "匿名"
	}
	if len([]rune(nickname)) > 20 {
		nickname = string([]rune(nickname)[:20])
	}
	res, err := database.DB.Exec(
		"INSERT INTO comments(post_id,user_id,nickname,content,ip,fingerprint,created_at) VALUES(?,?,?,?,?,?,?)",
		postID, userIDOf(u), nickname, content, "", fp, models.Now())
	if err != nil {
		return 0, "评论失败，请稍后重试"
	}
	cid, _ := res.LastInsertId()
	_, _ = database.DB.Exec("UPDATE posts SET comment_count=comment_count+1 WHERE id=?", postID)
	plugin.Emit("comment_created", map[string]any{"post_id": postID, "content": content})
	return cid, ""
}

// PostEdit 编辑自己的帖子（管理员亦可）
func PostEdit(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	p := loadPost(id)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	u := CurrentUser(r)
	a := CurrentAdmin(r)
	if (u == nil || u.ID != p.UserID) && a == nil {
		http.Error(w, "没有编辑权限", http.StatusForbidden)
		return
	}
	Render(w, r, "layout.html", "pg_edit", map[string]any{"post": p})
}

// PostEditSave 保存编辑（仅文字内容）
func PostEditSave(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	p := loadPost(id)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	u := CurrentUser(r)
	a := CurrentAdmin(r)
	if (u == nil || u.ID != p.UserID) && a == nil {
		http.Error(w, "没有编辑权限", http.StatusForbidden)
		return
	}
	content := util.StripHTML(r.FormValue("content"))
	if content == "" || len([]rune(content)) > 3000 {
		http.Error(w, "内容需为 1-3000 字", http.StatusBadRequest)
		return
	}
	if _, err := database.DB.Exec("UPDATE posts SET content=? WHERE id=?", content, id); err != nil {
		http.Error(w, "保存失败", http.StatusInternalServerError)
		return
	}
	plugin.Emit("post_updated", map[string]any{"post_id": id, "content": content})
	http.Redirect(w, r, "/post/"+r.PathValue("id"), http.StatusSeeOther)
}

// loadPost 加载帖子基础信息
func loadPost(id int64) *models.Post {
	p := &models.Post{}
	err := database.DB.QueryRow(
		"SELECT id,user_id,IFNULL(nickname,'匿名'),IFNULL(content,''),topic_id,status,like_count,comment_count,IFNULL(is_admin,0),IFNULL(badges,''),IFNULL(created_at,'') FROM posts WHERE id=?", id,
	).Scan(&p.ID, &p.UserID, &p.Nickname, &p.Content, &p.TopicID, &p.Status, &p.LikeCount, &p.CommentCount, &p.IsAdmin, &p.BadgesRaw, &p.CreatedAt)
	if err != nil {
		return nil
	}
	return p
}

// PostDelete 删除帖子（本人或管理员），级联清理关联数据
func PostDelete(w http.ResponseWriter, r *http.Request) {
	id := pathInt64(r, "id")
	p := loadPost(id)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	u := CurrentUser(r)
	a := CurrentAdmin(r)
	if (u == nil || u.ID != p.UserID) && a == nil {
		http.Error(w, "没有删除权限", http.StatusForbidden)
		return
	}
	deletePostCascade(id)
	plugin.Emit("post_deleted", map[string]any{"post_id": id})
	if isAJAX(r) {
		okJSON(w, map[string]any{"redirect": "/"})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// deletePostCascade 删除帖子及其评论、媒体、点赞（并清理磁盘媒体文件）
func deletePostCascade(id int64) {
	// 先取媒体路径，删除后清理磁盘文件
	if rows, err := database.DB.Query("SELECT IFNULL(path,'') FROM medias WHERE post_id=?", id); err == nil {
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && p != "" {
				util.RemoveUpload(p)
			}
		}
		rows.Close()
	}
	for _, t := range []string{"comments", "medias", "likes"} {
		_, _ = database.DB.Exec("DELETE FROM "+t+" WHERE post_id=?", id)
	}
	_, _ = database.DB.Exec("DELETE FROM posts WHERE id=?", id)
}

// AvatarUpload 用户头像上传（POST /profile/avatar）。
// 前端已用 Canvas 做可视化裁剪，服务端仍居中裁成正方形并压缩为 WebP，
// 防止绕过前端直接上传长图/非图片。
func AvatarUpload(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "请先登录后再上传头像")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		fail(w, http.StatusBadRequest, "图片过大或表单无效")
		return
	}
	f, fh, err := r.FormFile("avatar")
	if err != nil {
		fail(w, http.StatusBadRequest, "请选择要上传的图片")
		return
	}
	defer f.Close()

	path, err := util.SaveAvatar(fh)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := database.DB.Exec("UPDATE users SET avatar=? WHERE id=?", path, u.ID); err != nil {
		util.RemoveAvatar(path) // 入库失败，清理刚写入的文件
		fail(w, http.StatusInternalServerError, "保存头像失败，请稍后重试")
		return
	}
	util.RemoveAvatar(u.Avatar) // 删除旧头像，避免磁盘堆积
	util.Log("info", "用户 %s(#%d) 更新头像: %s", u.Username, u.ID, path)

	if isAJAX(r) {
		okJSON(w, map[string]any{"avatar": path})
		return
	}
	http.Redirect(w, r, "/u/"+strconv.FormatInt(u.ID, 10), http.StatusSeeOther)
}

// Profile 个人主页：展示该用户发布的帖子
func Profile(w http.ResponseWriter, r *http.Request) {
	uid := pathInt64(r, "id")
	u := models.GetUserByID(uid)
	if u == nil {
		http.NotFound(w, r)
		return
	}
	self := CurrentUser(r)
	a := CurrentAdmin(r)
	where := "user_id=? AND status=1"
	if (self != nil && self.ID == uid) || a != nil {
		where = "user_id=?"
	}
	rows, err := database.DB.Query(
		"SELECT id,IFNULL(nickname,'匿名'),IFNULL(content,''),like_count,comment_count,coin_count,IFNULL(is_admin,0),IFNULL(badges,''),IFNULL(created_at,'') FROM posts WHERE "+where+" ORDER BY id DESC LIMIT 100", uid)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type own struct {
		ID           int64
		Nickname     string
		Avatar       string
		Content      string
		LikeCount    int64
		CommentCount int64
		Coins        int64
		IsAdmin      int
		BadgesRaw    string
		Badges       []string
		CreatedAt    string
	}
	var posts []own
	for rows.Next() {
		var p own
		_ = rows.Scan(&p.ID, &p.Nickname, &p.Content, &p.LikeCount, &p.CommentCount, &p.Coins,
			&p.IsAdmin, &p.BadgesRaw, &p.CreatedAt)
		p.Avatar = u.Avatar
		p.Badges = models.DisplayBadges(int64(p.IsAdmin), p.BadgesRaw)
		posts = append(posts, p)
	}
	Render(w, r, "layout.html", "pg_profile", map[string]any{
		"owner": u, "posts": posts,
		"ownerLevel": models.LevelForExp(u.Exp),
		"card":       models.GetCard(uid),
		"self":       self != nil && self.ID == uid, "admin": a != nil,
	})
}

// isAJAX 是否为前端 AJAX 请求
func isAJAX(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// About 关于页：只声明开发/维护方、官网与 Logo，不介绍站点功能。
func About(w http.ResponseWriter, r *http.Request) {
	Render(w, r, "layout.html", "pg_about", nil)
}

// FaviconICO 根路径 favicon 兼容跳转到静态图标。
func FaviconICO(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/static/favicon.ico", http.StatusFound)
}

// Favicon 站点图标（内联 SVG）
func Favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="8" fill="#ff7ba9"/><path d="M16 24s-8-5.2-8-10.4C8 10 10.2 8 12.8 8c1.6 0 2.7.8 3.2 1.7C16.5 8.8 17.6 8 19.2 8 21.8 8 24 10 24 13.6 24 18.8 16 24 16 24z" fill="#fff"/></svg>`))
}
