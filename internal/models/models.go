// Package models 定义核心数据模型与少量通用存取辅助。
// 大部分 SQL 直接写在 handlers 中以保持内聚，此处仅放置跨模块共享的内容。
package models

import (
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"clearlove/internal/database"
)

// User 注册用户
type User struct {
	ID         int64
	Username   string
	Email      string
	Password   string
	Avatar     string
	Status     int // 1 正常 0 禁用
	Exp        int // 等级经验（累计）
	CoinTenths int // 硬币余额，单位 0.1 枚
	CreatedAt  string
}

// Admin 管理员。Role=super 拥有全部权限；custom 按 Perms 权限组列表判定
type Admin struct {
	ID        int64
	Username  string
	Password  string
	Role      string
	Perms     string // JSON 数组，如 ["posts","users"]
	CreatedAt string
}

// HasPerm 判断管理员是否拥有某权限组
func (a *Admin) HasPerm(p string) bool {
	if a == nil || a.Role == "super" {
		return a != nil
	}
	var perms []string
	_ = json.Unmarshal([]byte(a.Perms), &perms)
	for _, v := range perms {
		if v == p {
			return true
		}
	}
	return false
}

// 权限组常量（后台权限分配粒度）
const (
	PermPosts   = "posts"   // 帖子管理
	PermReports = "reports" // 举报管理
	PermUsers   = "users"   // 用户管理
	PermSetting = "settings"
	PermNotice  = "notices" // 公告与话题
	PermPlugin  = "plugins"
	PermAPI     = "api"
	PermAdmins  = "admins"
)

// Post 帖子
type Post struct {
	ID           int64
	UserID       int64
	Nickname     string
	Content      string
	TopicID      int64
	IP           string
	Fingerprint  string
	Status       int // 1 可见 0 隐藏（AI/管理员处理）
	LikeCount    int64
	CommentCount int64
	IsAdmin      int    // 1 = 管理员专享昵称发布（已通过后台账号验证）
	BadgesRaw    string // 插件写入的标识列表（JSON 数组字符串）
	CreatedAt    string
}

// ---------- 帖子标识（内置管理员标识 + 插件 badge 标识） ----------

// BadgesJSON 标识列表转 JSON 字符串（空列表返回空串，便于兼容旧数据）
func BadgesJSON(list []string) string {
	if len(list) == 0 {
		return ""
	}
	b, err := json.Marshal(list)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseBadges 解析 JSON 标识列表
func ParseBadges(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// MergeBadges 合并多个标识列表并去重（保持首次出现的顺序）
func MergeBadges(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range lists {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// DisplayBadges 计算帖子最终展示的标识（管理员标识 + 插件标识）
func DisplayBadges(isAdmin int64, raw string) []string {
	var builtin []string
	if isAdmin == 1 {
		builtin = append(builtin, "管理员")
	}
	return MergeBadges(builtin, ParseBadges(raw))
}

// AdminNicknames 后台配置的「管理员专享昵称」列表。
// 支持中英文逗号、分号与换行分隔，用于防止他人冒充官方身份。
func AdminNicknames() []string {
	raw := GetSetting("admin_nicknames")
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if s := strings.TrimSpace(f); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// IsAdminNickname 昵称是否属于管理员专享（忽略大小写与首尾空白）
func IsAdminNickname(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, n := range AdminNicknames() {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

var (
	setCache   sync.Map // 设置项内存缓存
	setCacheMu sync.Mutex
)

// Now 返回统一格式的 UTC 时间字符串
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// GetSetting 读取设置项（带内存缓存）。安装前数据库未连接时返回空串
func GetSetting(k string) string {
	if database.DB == nil {
		return ""
	}
	if v, ok := setCache.Load(k); ok {
		s, _ := v.(string)
		return s
	}
	setCacheMu.Lock()
	defer setCacheMu.Unlock()
	var v string
	err := database.DB.QueryRow("SELECT v FROM settings WHERE k=?", k).Scan(&v)
	if err != nil {
		return ""
	}
	setCache.Store(k, v)
	return v
}

// SetSetting 写入设置项并刷新缓存（按驱动选择 UPSERT 语法）
func SetSetting(k, v string) error {
	if database.DB == nil {
		return nil
	}
	setCacheMu.Lock()
	defer setCacheMu.Unlock()
	sql := "INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v"
	if database.Driver == "mysql" {
		sql = "INSERT INTO settings(k,v) VALUES(?,?) ON DUPLICATE KEY UPDATE v=VALUES(v)"
	}
	if _, err := database.DB.Exec(sql, k, v); err != nil {
		return err
	}
	setCache.Store(k, v)
	return nil
}

// QueryInt 快捷查询单个整数值
func QueryInt(q string, args ...any) int64 {
	if database.DB == nil {
		return 0
	}
	var n int64
	_ = database.DB.QueryRow(q, args...).Scan(&n)
	return n
}

// QueryStr 快捷查询单个字符串值
func QueryStr(q string, args ...any) string {
	if database.DB == nil {
		return ""
	}
	var s string
	_ = database.DB.QueryRow(q, args...).Scan(&s)
	return s
}

// GetUserByID 按 ID 查询用户
func GetUserByID(id int64) *User {
	if database.DB == nil {
		return nil
	}
	u := &User{}
	err := database.DB.QueryRow(
		"SELECT id,username,email,password,IFNULL(avatar,''),status,IFNULL(exp,0),IFNULL(coin_tenths,0),IFNULL(created_at,'') FROM users WHERE id=?", id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Password, &u.Avatar, &u.Status, &u.Exp, &u.CoinTenths, &u.CreatedAt)
	if err != nil {
		return nil
	}
	return u
}

// GetAdminByID 按 ID 查询管理员
func GetAdminByID(id int64) *Admin {
	if database.DB == nil {
		return nil
	}
	a := &Admin{}
	err := database.DB.QueryRow(
		"SELECT id,username,password,role,IFNULL(perms,'[]'),IFNULL(created_at,'') FROM admins WHERE id=?", id,
	).Scan(&a.ID, &a.Username, &a.Password, &a.Role, &a.Perms, &a.CreatedAt)
	if err != nil {
		return nil
	}
	return a
}

// NilCoalesce 兼容空值扫描的辅助
func NilStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// Prefs 用户个性化设置（服务端持久化）
type Prefs struct {
	Theme     string `json:"theme"`      // light | dark | auto
	Columns   int    `json:"columns"`    // 1 | 2 | 3
	FontScale int    `json:"font_scale"` // 90 | 100 | 115
}

// DefaultPrefs 未登录/未设置时的默认值
func DefaultPrefs() Prefs { return Prefs{Theme: "dark", Columns: 1, FontScale: 100} }

// Normalize 把非法值收敛到受支持范围
func (p Prefs) Normalize() Prefs {
	switch p.Theme {
	case "light", "dark", "auto", "sakura", "mint", "ocean", "violet", "sunset":
	default:
		p.Theme = "auto"
	}
	if p.Columns < 1 || p.Columns > 3 {
		p.Columns = 3
	}
	if p.FontScale < 80 || p.FontScale > 140 {
		p.FontScale = 100
	}
	return p
}

// GetPrefs 读取用户设置，未设置时返回默认值
func GetPrefs(uid int64) Prefs {
	p := DefaultPrefs()
	if database.DB == nil || uid == 0 {
		return p
	}
	err := database.DB.QueryRow(
		"SELECT theme,cols,font_scale FROM user_prefs WHERE user_id=?", uid,
	).Scan(&p.Theme, &p.Columns, &p.FontScale)
	if err != nil {
		return DefaultPrefs()
	}
	return p.Normalize()
}

// SavePrefs 写入/更新用户设置（SQLite / MySQL 双语法）
func SavePrefs(uid int64, p Prefs) error {
	if database.DB == nil || uid == 0 {
		return nil
	}
	p = p.Normalize()
	q := `INSERT INTO user_prefs(user_id,theme,cols,font_scale,updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET theme=excluded.theme, cols=excluded.cols,
font_scale=excluded.font_scale, updated_at=excluded.updated_at`
	if database.Driver == "mysql" {
		q = `INSERT INTO user_prefs(user_id,theme,cols,font_scale,updated_at) VALUES(?,?,?,?,?)
ON DUPLICATE KEY UPDATE theme=VALUES(theme), cols=VALUES(cols), font_scale=VALUES(font_scale), updated_at=VALUES(updated_at)`
	}
	_, err := database.DB.Exec(q, uid, p.Theme, p.Columns, p.FontScale, Now())
	return err
}

// UserCard 用户主页名片（自定义头衔/签名/配色）
type UserCard struct {
	Title string `json:"title"`
	Bio   string `json:"bio"`
	Style string `json:"style"`
	Cover string `json:"cover"`
}

// DefaultCard 默认名片：空头衔/签名 + 默认配色
func DefaultCard() UserCard { return UserCard{Style: "aurora"} }

// Normalize 收敛名片字段
func (c UserCard) Normalize() UserCard {
	switch c.Style {
	case "aurora", "sakura", "ocean", "sunset", "forest", "mono":
	default:
		c.Style = "aurora"
	}
	return c
}

// GetCard 读取用户名片，未设置时返回默认值
func GetCard(uid int64) UserCard {
	c := DefaultCard()
	if database.DB == nil || uid == 0 {
		return c
	}
	err := database.DB.QueryRow(
		"SELECT IFNULL(title,''),IFNULL(bio,''),IFNULL(style,'aurora'),IFNULL(cover,'') FROM user_cards WHERE user_id=?", uid,
	).Scan(&c.Title, &c.Bio, &c.Style, &c.Cover)
	if err != nil {
		return DefaultCard()
	}
	return c.Normalize()
}

// SaveCard 写入/更新用户名片
func SaveCard(uid int64, c UserCard) error {
	if database.DB == nil || uid == 0 {
		return nil
	}
	c = c.Normalize()
	q := `INSERT INTO user_cards(user_id,title,bio,style,cover,updated_at) VALUES(?,?,?,?,'',?)
ON CONFLICT(user_id) DO UPDATE SET title=excluded.title, bio=excluded.bio, style=excluded.style, updated_at=excluded.updated_at`
	if database.Driver == "mysql" {
		q = `INSERT INTO user_cards(user_id,title,bio,style,cover,updated_at) VALUES(?,?,?,?,'',?)
ON DUPLICATE KEY UPDATE title=VALUES(title), bio=VALUES(bio), style=VALUES(style), updated_at=VALUES(updated_at)`
	}
	_, err := database.DB.Exec(q, uid, c.Title, c.Bio, c.Style, Now())
	return err
}

// SetCardCover 只更新封面图，保留头衔/签名/配色
func SetCardCover(uid int64, cover string) error {
	if database.DB == nil || uid == 0 {
		return nil
	}
	q := `INSERT INTO user_cards(user_id,title,bio,style,cover,updated_at) VALUES(?,'','','aurora',?,?)
ON CONFLICT(user_id) DO UPDATE SET cover=excluded.cover, updated_at=excluded.updated_at`
	if database.Driver == "mysql" {
		q = `INSERT INTO user_cards(user_id,title,bio,style,cover,updated_at) VALUES(?,'','','aurora',?,?)
ON DUPLICATE KEY UPDATE cover=VALUES(cover), updated_at=VALUES(updated_at)`
	}
	_, err := database.DB.Exec(q, uid, cover, Now())
	return err
}
