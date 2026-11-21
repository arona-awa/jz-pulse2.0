// Host API：站点数据层（clv.setting / clv.post / clv.comment / clv.user / clv.stats）。
//
// 提供高层语义，避免插件手写内核 SQL；写操作需 site.write 权限。
package plugin

import (
	"errors"
	"strings"

	"github.com/dop251/goja"

	"clearlove/internal/database"
	"clearlove/internal/models"
	postnotify "clearlove/internal/notify"
	"clearlove/internal/util"
)

func (rt *jsRuntime) installSite(clv *goja.Object) error {
	// ---------- clv.setting ----------
	st := rt.vm.NewObject()
	_ = st.Set("get", rt.jsFn(rt.settingGet))
	_ = st.Set("set", rt.jsFn(rt.settingSet))
	_ = st.Set("all", rt.jsFn(rt.settingAll))
	_ = clv.Set("setting", st)

	// ---------- clv.post ----------
	post := rt.vm.NewObject()
	_ = post.Set("list", rt.jsFn(rt.postList))
	_ = post.Set("get", rt.jsFn(rt.postGet))
	_ = post.Set("create", rt.jsFn(rt.postCreate))
	_ = post.Set("update", rt.jsFn(rt.postUpdate))
	_ = post.Set("remove", rt.jsFn(rt.postRemove))
	_ = clv.Set("post", post)

	// ---------- clv.comment ----------
	cm := rt.vm.NewObject()
	_ = cm.Set("list", rt.jsFn(rt.commentList))
	_ = cm.Set("create", rt.jsFn(rt.commentCreate))
	_ = cm.Set("remove", rt.jsFn(rt.commentRemove))
	_ = clv.Set("comment", cm)

	// ---------- clv.user ----------
	u := rt.vm.NewObject()
	_ = u.Set("get", rt.jsFn(rt.userGet))
	_ = u.Set("byName", rt.jsFn(rt.userByName))
	_ = u.Set("current", rt.jsFn(rt.userCurrent))
	_ = clv.Set("user", u)

	// ---------- clv.stats ----------
	stat := rt.vm.NewObject()
	_ = stat.Set("overview", rt.jsFn(rt.statsOverview))
	_ = stat.Set("today", rt.jsFn(rt.statsToday))
	_ = clv.Set("stats", stat)

	return nil
}

// requireAnyPerm 任一权限满足即可（用于读类 API 的宽松校验）
func (rt *jsRuntime) requireAnyPerm(perms ...string) error {
	if rt.app == nil {
		return errNoApp
	}
	for _, p := range perms {
		if rt.app.Plugin.HasPermission(p) {
			return nil
		}
	}
	return &permError{slug: rt.app.Plugin.SlugOf(), perm: strings.Join(perms, " / ")}
}

func (rt *jsRuntime) needDB() error {
	if database.DB == nil {
		return errors.New("数据库未连接")
	}
	return nil
}

// ---------- setting ----------

func (rt *jsRuntime) settingGet(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("settings.read"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.setting.get(key) 需要一个参数")
	}
	return models.GetSetting(strOf(args[0])), nil
}

func (rt *jsRuntime) settingSet(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("settings.write"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) < 2 {
		return nil, errors.New("clv.setting.set(key, value) 需要两个参数")
	}
	return nil, models.SetSetting(strOf(args[0]), strOf(args[1]))
}

func (rt *jsRuntime) settingAll(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("settings.read"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	rows, err := queryMaps(database.DB, "SELECT k, v FROM settings", nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strOf(r["k"])] = strOf(r["v"])
	}
	return out, nil
}

// ---------- post ----------

func (rt *jsRuntime) postList(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	arg := mapOfAny(call.Argument(0))
	limit := intOf(arg["limit"])
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	before := int64(intOf(arg["before"]))
	topic := strOf(arg["topic"])
	withHidden := boolOf(arg["include_hidden"])
	q := `SELECT p.id, p.user_id, IFNULL(p.nickname,'匿名') AS nickname, IFNULL(p.content,'') AS content,
	             IFNULL(t.name,'') AS topic, p.status, p.like_count, p.comment_count, IFNULL(p.created_at,'') AS created_at
	      FROM posts p LEFT JOIN topics t ON t.id=p.topic_id
	      WHERE (? <= 0 OR p.id < ?)`
	args := []any{before, before}
	if !withHidden {
		q += " AND p.status=1"
	}
	if topic != "" {
		q += " AND t.name=?"
		args = append(args, topic)
	}
	q += " ORDER BY p.id DESC LIMIT ?"
	args = append(args, limit)
	return queryMaps(database.DB, q, args)
}

func (rt *jsRuntime) postGet(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.post.get(id) 需要一个参数")
	}
	rows, err := queryMaps(database.DB,
		`SELECT p.id, p.user_id, IFNULL(p.nickname,'匿名') AS nickname, IFNULL(p.content,'') AS content,
		        p.topic_id, IFNULL(t.name,'') AS topic, p.status, p.like_count, p.comment_count,
		        IFNULL(p.is_admin,0) AS is_admin, IFNULL(p.created_at,'') AS created_at
		 FROM posts p LEFT JOIN topics t ON t.id=p.topic_id WHERE p.id=?`, []any{int64(intOf(args[0]))})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (rt *jsRuntime) postCreate(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.write"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	arg := mapOfAny(call.Argument(0))
	if arg == nil {
		return nil, errors.New("clv.post.create({content, nickname, topic}) 需要一个对象参数")
	}
	content := util.StripHTML(strOf(arg["content"]))
	if content == "" || len([]rune(content)) > 3000 {
		return nil, errors.New("帖子内容需为 1-3000 字")
	}
	nickname := util.StripHTML(strOf(arg["nickname"]))
	if nickname == "" {
		nickname = "匿名"
	}
	topicID := rt.ensureTopic(util.StripHTML(strOf(arg["topic"])))
	res, err := database.DB.Exec(
		"INSERT INTO posts(user_id,nickname,content,topic_id,ip,fingerprint,status,is_admin,badges,created_at) VALUES(?,?,?,?,?,?,1,0,'',?)",
		0, nickname, content, topicID, "", "", models.Now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	Notify("post_created", map[string]any{"post_id": id, "nickname": nickname, "content": content})
	postnotify.NewPost(id, 0, nickname, content)
	return map[string]any{"id": id}, nil
}

func (rt *jsRuntime) postUpdate(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.write"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) < 2 {
		return nil, errors.New("clv.post.update(id, {content}) 需要两个参数")
	}
	id := int64(intOf(args[0]))
	arg, _ := args[1].(map[string]any)
	content := util.StripHTML(strOf(arg["content"]))
	if content == "" || len([]rune(content)) > 3000 {
		return nil, errors.New("帖子内容需为 1-3000 字")
	}
	_, err := database.DB.Exec("UPDATE posts SET content=? WHERE id=?", content, id)
	return nil, err
}

func (rt *jsRuntime) postRemove(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.write"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.post.remove(id) 需要一个参数")
	}
	id := int64(intOf(args[0]))
	for _, t := range []string{"comments", "medias", "likes", "reports"} {
		_, _ = database.DB.Exec("DELETE FROM "+t+" WHERE post_id=?", id)
	}
	_, err := database.DB.Exec("DELETE FROM posts WHERE id=?", id)
	return nil, err
}

func (rt *jsRuntime) ensureTopic(name string) int64 {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 30 {
		return 0
	}
	if id := models.QueryInt("SELECT id FROM topics WHERE name=?", name); id > 0 {
		return id
	}
	res, err := database.DB.Exec("INSERT INTO topics(name,created_at) VALUES(?,?)", name, models.Now())
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// ---------- comment ----------

func (rt *jsRuntime) commentList(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.comment.list(postId) 需要一个参数")
	}
	return queryMaps(database.DB,
		`SELECT id, post_id, IFNULL(nickname,'匿名') AS nickname, IFNULL(content,'') AS content, IFNULL(created_at,'') AS created_at
		 FROM comments WHERE post_id=? ORDER BY id`, []any{int64(intOf(args[0]))})
}

func (rt *jsRuntime) commentCreate(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.write"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	arg := mapOfAny(call.Argument(0))
	if arg == nil {
		return nil, errors.New("clv.comment.create({post_id, content, nickname}) 需要一个对象参数")
	}
	postID := int64(intOf(arg["post_id"]))
	if models.QueryInt("SELECT COUNT(1) FROM posts WHERE id=? AND status=1", postID) == 0 {
		return nil, errors.New("帖子不存在或不可见")
	}
	content := util.StripHTML(strOf(arg["content"]))
	if content == "" || len([]rune(content)) > 500 {
		return nil, errors.New("评论需为 1-500 字")
	}
	nickname := util.StripHTML(strOf(arg["nickname"]))
	if nickname == "" {
		nickname = "匿名"
	}
	res, err := database.DB.Exec(
		"INSERT INTO comments(post_id,user_id,nickname,content,ip,fingerprint,created_at) VALUES(?,0,?,?,'','',?)",
		postID, nickname, content, models.Now())
	if err != nil {
		return nil, err
	}
	_, _ = database.DB.Exec("UPDATE posts SET comment_count=comment_count+1 WHERE id=?", postID)
	id, _ := res.LastInsertId()
	Notify("comment_created", map[string]any{"post_id": postID, "content": content})
	return map[string]any{"id": id}, nil
}

func (rt *jsRuntime) commentRemove(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.write"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.comment.remove(id) 需要一个参数")
	}
	_, err := database.DB.Exec("DELETE FROM comments WHERE id=?", int64(intOf(args[0])))
	return nil, err
}

// ---------- user ----------

func userPublicMap(u *models.User) map[string]any {
	if u == nil {
		return nil
	}
	return map[string]any{
		"id": u.ID, "username": u.Username, "avatar": u.Avatar,
		"status": u.Status, "created_at": u.CreatedAt,
	}
}

func (rt *jsRuntime) userGet(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.user.get(id) 需要一个参数")
	}
	return userPublicMap(models.GetUserByID(int64(intOf(args[0])))), nil
}

func (rt *jsRuntime) userByName(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.user.byName(name) 需要一个参数")
	}
	var id int64
	if err := database.DB.QueryRow("SELECT id FROM users WHERE username=?", strOf(args[0])).Scan(&id); err != nil {
		return nil, nil
	}
	return userPublicMap(models.GetUserByID(id)), nil
}

// userCurrent 返回当前请求的登录用户（由路由层在调用前注入）
func (rt *jsRuntime) userCurrent(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	if rt.cur == nil || rt.cur.UserID == 0 {
		return nil, nil
	}
	return userPublicMap(models.GetUserByID(rt.cur.UserID)), nil
}

// ---------- stats ----------

func (rt *jsRuntime) statsOverview(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	return map[string]any{
		"users":           models.QueryInt("SELECT COUNT(1) FROM users"),
		"posts":           models.QueryInt("SELECT COUNT(1) FROM posts"),
		"comments":        models.QueryInt("SELECT COUNT(1) FROM comments"),
		"reports_pending": models.QueryInt("SELECT COUNT(1) FROM reports WHERE status=0"),
	}, nil
}

func (rt *jsRuntime) statsToday(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("site.read"); err != nil {
		return nil, err
	}
	if err := rt.needDB(); err != nil {
		return nil, err
	}
	today := models.Now()[:10] + "T00:00:00Z"
	return map[string]any{
		"posts":    models.QueryInt("SELECT COUNT(1) FROM posts WHERE created_at >= ?", today),
		"comments": models.QueryInt("SELECT COUNT(1) FROM comments WHERE created_at >= ?", today),
		"users":    models.QueryInt("SELECT COUNT(1) FROM users WHERE created_at >= ?", today),
	}, nil
}
