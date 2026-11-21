// Package migrate 将 ClearLove PHP 旧版（1.x）的数据迁移到 2.0。
//
// 支持两种数据来源，由文件内容自动识别：
//   - SQLite 数据库文件（旧版根目录 data.db）
//   - MySQL 导出文件（mysqldump / 宝塔导出的 .sql）
//
// 迁移内容：话题 / 帖子（含媒体文件与点赞数）/ 评论 / 公告 / 用户 / 管理员 /
// 封禁名单 / 举报。访问量（visits）与置顶（is_pinned）在新版中没有对应结构，跳过。
//
// 特点：
//   - 保留原有自增 ID（评论、举报的关联关系不受影响）
//   - 可重复执行：已存在的记录按 ID / 用户名 / (类型,值) 判重跳过
//   - 旧版 status 语义为 0=正常 1=隐藏，新版为 1=可见 0=隐藏，迁移时自动取反
package migrate

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

// Stats 迁移结果统计
type Stats struct {
	Source    string `json:"source"` // sqlite / mysqldump
	Topics    int    `json:"topics"`
	Posts     int    `json:"posts"`
	Media     int    `json:"media"`
	Comments  int    `json:"comments"`
	Notices   int    `json:"notices"`
	Users     int    `json:"users"`
	Admins    int    `json:"admins"`
	Bans      int    `json:"bans"`
	Reports   int    `json:"reports"`
	Likes     int    `json:"likes"`
	Skipped   int    `json:"skipped"`     // 已存在而跳过的记录
	Missing   int    `json:"missing"`     // 媒体文件缺失数量
	NoPostRef int    `json:"no_post_ref"` // 引用了不存在帖子的评论 / 举报
}

// dry 试运行：只统计不写入
var dry bool

// pendingPosts 本次迁移中已导入（试运行时为「将会导入」）的帖子 ID。
// 试运行不会真正写入帖子，若直接查库判断，评论与举报会被误判为"关联失效"而统计为 0。
var pendingPosts = map[int64]bool{}

// postExists 判断帖子是否存在于新版库或本次迁移范围内
func postExists(id int64) bool {
	if id <= 0 {
		return false
	}
	if pendingPosts[id] {
		return true
	}
	return exists("SELECT COUNT(1) FROM posts WHERE id=?", id)
}

// Run 按文件路径执行迁移，数据源类型由文件内容自动识别
func Run(oldPath, oldUploads string, dryRun bool) (*Stats, error) {
	if _, err := os.Stat(oldPath); err != nil {
		return nil, fmt.Errorf("找不到旧版数据文件: %s", oldPath)
	}
	src, err := OpenSource(oldPath)
	if err != nil {
		return nil, fmt.Errorf("无法识别旧版数据文件: %w", err)
	}
	defer src.Close()
	if oldUploads == "" {
		oldUploads = filepath.Join(filepath.Dir(oldPath), "uploads")
	}
	return RunSource(src, oldUploads, dryRun)
}

// RunSource 从已打开的数据源执行迁移
func RunSource(src Source, oldUploads string, dryRun bool) (*Stats, error) {
	dry = dryRun
	if !src.HasTable("posts") {
		return nil, fmt.Errorf("数据源中找不到 posts 表，请确认这是旧版 ClearLove 的数据库（data.db 或导出的 .sql）")
	}
	st := &Stats{Source: src.Kind()}
	pendingPosts = map[int64]bool{}

	topicMap, err := migrateTopics(src, st)
	if err != nil {
		return st, err
	}
	if err := migratePosts(src, topicMap, oldUploads, st); err != nil {
		return st, err
	}
	if err := migrateComments(src, st); err != nil {
		return st, err
	}
	if err := migrateNotices(src, st); err != nil {
		return st, err
	}
	if err := migrateUsers(src, st); err != nil {
		return st, err
	}
	if err := migrateAdmins(src, st); err != nil {
		return st, err
	}
	if err := migrateBans(src, st); err != nil {
		return st, err
	}
	if err := migrateReports(src, st); err != nil {
		return st, err
	}
	// 评论数以实际记录为准重算
	if !dry {
		_, _ = database.DB.Exec(`UPDATE posts SET comment_count =
			(SELECT COUNT(1) FROM comments WHERE comments.post_id = posts.id)`)
	}
	return st, nil
}

// ---------- 通用辅助 ----------

// exists 新版库中是否已有该记录
func exists(q string, args ...any) bool {
	return models.QueryInt(q, args...) > 0
}

// exec 写操作（试运行时不执行）
func exec(q string, args ...any) (sql.Result, error) {
	if dry {
		return nil, nil
	}
	return database.DB.Exec(q, args...)
}

// parseTime 旧版时间转 RFC3339（SQLite 存 UTC，MySQL DATETIME 视为 UTC）
func parseTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return models.Now()
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return models.Now()
}

// ensureTopic 按名称取得话题 ID（不存在则创建）
func ensureTopic(name string, m map[string]int64) int64 {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	if id, ok := m[name]; ok {
		return id
	}
	if id := models.QueryInt("SELECT id FROM topics WHERE name=?", name); id > 0 {
		m[name] = id
		return id
	}
	res, err := exec("INSERT INTO topics(name,created_at) VALUES(?,?)", name, models.Now())
	if err != nil || res == nil {
		return 0
	}
	id, _ := res.LastInsertId()
	m[name] = id
	return id
}

// ---------- 各类数据 ----------

// migrateTopics 迁移话题表
func migrateTopics(src Source, st *Stats) (map[string]int64, error) {
	m := map[string]int64{}
	rows, err := src.Rows("topics", "name")
	if err != nil {
		return m, nil
	}
	for _, row := range rows {
		name := strings.TrimSpace(field(row, "name"))
		if name == "" {
			continue
		}
		if exists("SELECT COUNT(1) FROM topics WHERE name=?", name) {
			st.Skipped++
		}
		ensureTopic(name, m)
		st.Topics++
	}
	return m, nil
}

// MediaItem 旧版媒体 JSON 单元
type MediaItem struct {
	File string `json:"file"`
	Type string `json:"type"`
}

// migratePosts 迁移帖子与媒体、点赞
func migratePosts(src Source, topicMap map[string]int64, oldUploads string, st *Stats) error {
	rows, err := src.Rows("posts", "id", "nickname", "content", "topic", "media", "likes",
		"ip_address", "ip", "fingerprint", "status", "is_admin", "created_at")
	if err != nil {
		return fmt.Errorf("读取旧版帖子失败: %w", err)
	}
	for _, row := range rows {
		id := fieldInt(row, "id")
		if id <= 0 || exists("SELECT COUNT(1) FROM posts WHERE id=?", id) {
			st.Skipped++
			continue
		}
		pendingPosts[id] = true
		// 旧版：0（或 NULL）= 正常展示，1 = 隐藏；新版：1 = 可见，0 = 隐藏 -> 取反
		newStatus := int64(1)
		if strings.TrimSpace(field(row, "status")) == "1" {
			newStatus = 0
		}
		nickname := strings.TrimSpace(field(row, "nickname"))
		if nickname == "" {
			nickname = "匿名"
		}
		isAdmin := fieldInt(row, "is_admin")
		likes := fieldInt(row, "likes")
		created := parseTime(field(row, "created_at"))
		topicID := ensureTopic(field(row, "topic"), topicMap)

		if _, err := exec(`INSERT INTO posts(id,user_id,nickname,content,topic_id,ip,fingerprint,
			status,like_count,comment_count,is_admin,badges,created_at) VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?)`,
			id, 0, nickname, field(row, "content"), topicID,
			field(row, "ip_address", "ip"), field(row, "fingerprint"),
			newStatus, likes, isAdmin, "", created); err != nil {
			return fmt.Errorf("写入帖子 #%d 失败: %w", id, err)
		}
		st.Posts++

		// 点赞：新版点赞数由 likes 表实时统计，这里按旧版计数补齐占位记录，
		// 避免迁移后用户点一次赞就把原有赞数清零
		for i := int64(0); i < likes; i++ {
			if _, err := exec("INSERT INTO likes(post_id,user_id,fingerprint,created_at) VALUES(?,0,?,?)",
				id, fmt.Sprintf("legacy-%d-%d", id, i), created); err == nil {
				st.Likes++
			}
		}

		// 媒体：复制文件到新版 uploads/legacy/ 并建立 medias 记录
		var items []MediaItem
		if raw := strings.TrimSpace(field(row, "media")); raw != "" {
			_ = json.Unmarshal([]byte(raw), &items)
		}
		for _, it := range items {
			name := filepath.Base(strings.TrimSpace(it.File))
			if name == "" || name == "." {
				continue
			}
			if copyMedia(oldUploads, name, st) != "" {
				typ := it.Type
				if typ != "video" {
					typ = "image"
				}
				if _, err := exec("INSERT INTO medias(post_id,type,path,created_at) VALUES(?,?,?,?)",
					id, typ, "/uploads/legacy/"+name, created); err == nil {
					st.Media++
				}
			}
		}
	}
	util.Log("info", "旧版帖子迁移完成：新增 %d，跳过 %d", st.Posts, st.Skipped)
	return nil
}

// copyMedia 把旧版上传目录中的文件复制到新版 uploads/legacy/，返回目标文件名
func copyMedia(oldUploads, name string, st *Stats) string {
	src := filepath.Join(oldUploads, name)
	if fi, err := os.Stat(src); err != nil || fi.IsDir() {
		st.Missing++
		return ""
	}
	if dry {
		return name
	}
	dstDir := filepath.Join(config.Cfg.UploadDir, "legacy")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		st.Missing++
		return ""
	}
	dst := filepath.Join(dstDir, name)
	if _, err := os.Stat(dst); err == nil {
		return name // 已复制过
	}
	in, err := os.Open(src)
	if err != nil {
		st.Missing++
		return ""
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		st.Missing++
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		st.Missing++
		return ""
	}
	return name
}

// migrateComments 迁移评论
func migrateComments(src Source, st *Stats) error {
	rows, err := src.Rows("comments", "id", "post_id", "nickname", "content",
		"ip_address", "ip", "fingerprint", "created_at")
	if err != nil {
		return nil
	}
	for _, row := range rows {
		id, pid := fieldInt(row, "id"), fieldInt(row, "post_id")
		if !postExists(pid) {
			st.NoPostRef++
			continue
		}
		if id > 0 && exists("SELECT COUNT(1) FROM comments WHERE id=?", id) {
			st.Skipped++
			continue
		}
		nickname := strings.TrimSpace(field(row, "nickname"))
		if nickname == "" {
			nickname = "匿名"
		}
		if _, err := exec(`INSERT INTO comments(id,post_id,user_id,nickname,content,ip,fingerprint,created_at)
			VALUES(?,?,0,?,?,?,?,?)`,
			id, pid, nickname, field(row, "content"),
			field(row, "ip_address", "ip"), field(row, "fingerprint"),
			parseTime(field(row, "created_at"))); err != nil {
			return fmt.Errorf("写入评论 #%d 失败: %w", id, err)
		}
		st.Comments++
	}
	return nil
}

// migrateNotices 迁移公告（旧版 announcements -> 新版 notices）
func migrateNotices(src Source, st *Stats) error {
	rows, err := src.Rows("announcements", "content", "active", "created_at")
	if err != nil {
		return nil
	}
	for _, row := range rows {
		// 旧版 active=0 表示已停用，导入会让它成为首页最新公告，故跳过
		if strings.TrimSpace(field(row, "active")) == "0" {
			continue
		}
		body := strings.TrimSpace(field(row, "content"))
		if body == "" {
			continue
		}
		// 新版公告有标题字段，旧版没有：截取正文前 20 字作为标题
		title := []rune(body)
		if len(title) > 20 {
			title = title[:20]
		}
		if _, err := exec("INSERT INTO notices(title,content,created_at) VALUES(?,?,?)",
			string(title), body, parseTime(field(row, "created_at"))); err != nil {
			return fmt.Errorf("写入公告失败: %w", err)
		}
		st.Notices++
	}
	return nil
}

// migrateUsers 迁移注册用户（密码同为 bcrypt，可直接沿用）
func migrateUsers(src Source, st *Stats) error {
	if !src.HasTable("users") {
		return nil
	}
	rows, err := src.Rows("users", "id", "username", "password", "email", "created_at")
	if err != nil {
		return nil
	}
	for _, row := range rows {
		id := fieldInt(row, "id")
		username := strings.TrimSpace(field(row, "username"))
		password := field(row, "password")
		if username == "" || password == "" {
			continue
		}
		if exists("SELECT COUNT(1) FROM users WHERE username=?", username) {
			st.Skipped++
			continue
		}
		// 新版 email 非空且唯一：旧版可能为空或重复，这里补齐占位邮箱
		email := strings.TrimSpace(field(row, "email"))
		if email == "" || exists("SELECT COUNT(1) FROM users WHERE email=?", email) {
			email = fmt.Sprintf("legacy_%d@migrated.local", id)
		}
		if _, err := exec("INSERT INTO users(username,email,password,avatar,status,created_at) VALUES(?,?,?,'',1,?)",
			username, email, password, parseTime(field(row, "created_at"))); err != nil {
			return fmt.Errorf("写入用户 %s 失败: %w", username, err)
		}
		st.Users++
	}
	return nil
}

// migrateAdmins 迁移管理员（密码同为 bcrypt）
func migrateAdmins(src Source, st *Stats) error {
	if !src.HasTable("admins") {
		return nil
	}
	rows, err := src.Rows("admins", "username", "password", "role")
	if err != nil {
		return nil
	}
	for _, row := range rows {
		username := strings.TrimSpace(field(row, "username"))
		password := field(row, "password")
		if username == "" || password == "" {
			continue
		}
		if exists("SELECT COUNT(1) FROM admins WHERE username=?", username) {
			st.Skipped++
			continue
		}
		// 旧版权限模型与新版权限组不同：非超管先以空权限导入，由管理员在后台重新分配
		newRole, perms := "custom", "[]"
		if field(row, "role") == "super" {
			newRole = "super"
		}
		if _, err := exec("INSERT INTO admins(username,password,role,perms,created_at) VALUES(?,?,?,?,?)",
			username, password, newRole, perms, models.Now()); err != nil {
			return fmt.Errorf("写入管理员 %s 失败: %w", username, err)
		}
		st.Admins++
	}
	return nil
}

// migrateBans 迁移封禁名单（banned_devices -> bans）
func migrateBans(src Source, st *Stats) error {
	rows, err := src.Rows("banned_devices", "type", "value")
	if err != nil {
		return nil
	}
	for _, row := range rows {
		t := strings.TrimSpace(field(row, "type"))
		v := strings.TrimSpace(field(row, "value"))
		if (t != "ip" && t != "fingerprint") || v == "" {
			continue
		}
		if exists("SELECT COUNT(1) FROM bans WHERE btype=? AND bvalue=?", t, v) {
			st.Skipped++
			continue
		}
		if _, err := exec("INSERT INTO bans(btype,bvalue,created_at) VALUES(?,?,?)", t, v, models.Now()); err != nil {
			return fmt.Errorf("写入封禁记录失败: %w", err)
		}
		st.Bans++
	}
	return nil
}

// migrateReports 迁移举报
func migrateReports(src Source, st *Stats) error {
	if !src.HasTable("reports") {
		return nil
	}
	rows, err := src.Rows("reports", "id", "post_id", "reason", "detail",
		"reporter_ip", "ip", "status", "created_at")
	if err != nil {
		return nil
	}
	for _, row := range rows {
		id, pid := fieldInt(row, "id"), fieldInt(row, "post_id")
		// 帖子已被删除（或未迁移）的举报没有意义，跳过
		if !postExists(pid) {
			st.NoPostRef++
			continue
		}
		if id > 0 && exists("SELECT COUNT(1) FROM reports WHERE id=?", id) {
			st.Skipped++
			continue
		}
		text := strings.TrimSpace(field(row, "reason"))
		if d := strings.TrimSpace(field(row, "detail")); d != "" {
			if text == "" {
				text = d
			} else {
				text += "：" + d
			}
		}
		// 旧版 0=待处理，1=已展示，2=已删除；新版 0=待处理，1=已处理
		newStatus := int64(0)
		if s := strings.TrimSpace(field(row, "status")); s != "" && s != "0" {
			newStatus = 1
		}
		if _, err := exec("INSERT INTO reports(id,post_id,reason,reporter_ip,status,ai_result,created_at) VALUES(?,?,?,?,?,'',?)",
			id, pid, text, field(row, "reporter_ip", "ip"), newStatus,
			parseTime(field(row, "created_at"))); err != nil {
			return fmt.Errorf("写入举报 #%d 失败: %w", id, err)
		}
		st.Reports++
	}
	return nil
}
