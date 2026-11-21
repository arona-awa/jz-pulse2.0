// Package database 负责数据库连接与表结构自动迁移。
// 同时兼容 SQLite（modernc.org/sqlite，纯 Go 无 CGO）与 MySQL（go-sql-driver/mysql）。
// 为保证两种数据库行为一致，时间字段统一使用 TEXT 存储 RFC3339 格式（UTC）。
package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"clearlove/internal/config"
	"clearlove/internal/util"
)

// DB 全局数据库句柄，所有处理器直接使用
var DB *sql.DB

// Driver 当前驱动名（sqlite / mysql）
var Driver string

// Connect 依据配置打开数据库连接
func Connect() error {
	var dsn string
	Driver = config.Cfg.DBType
	switch Driver {
	case "mysql":
		dsn = config.Cfg.MySQLDsn
	case "sqlite", "":
		Driver = "sqlite"
		dsn = config.Cfg.SQLitePath
		// 默认/历史默认路径跟随数据目录（支持 CLEARLOVE_DATA_DIR 自定义）
		if dsn == "" || dsn == "data/clearlove.db" {
			dsn = filepath.Join(config.Cfg.DataDir, "clearlove.db")
		}
		if dir := filepath.Dir(dsn); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("创建数据库目录失败: %w", err)
			}
		}
	default:
		return fmt.Errorf("不支持的数据库类型: %s", Driver)
	}
	db, err := sql.Open(Driver, dsn)
	if err != nil {
		return err
	}
	// SQLite 写并发能力有限，限制连接数；MySQL 走默认连接池
	if Driver == "sqlite" {
		db.SetMaxOpenConns(1)
	}
	if err := db.Ping(); err != nil {
		return err
	}
	DB = db
	return nil
}

// schemaDDL 表结构模板。{PK}=自增主键 {I}=整型 {LT}=长文本，按驱动替换
const schemaDDL = `
CREATE TABLE IF NOT EXISTS users (
	id {PK}, username VARCHAR(64) NOT NULL UNIQUE, email VARCHAR(128) NOT NULL UNIQUE,
	password VARCHAR(128) NOT NULL, avatar VARCHAR(255) DEFAULT '', status {I} DEFAULT 1,
	exp {I} DEFAULT 0, exp_login_day VARCHAR(10) DEFAULT '', exp_post_day VARCHAR(10) DEFAULT '',
	exp_like_day VARCHAR(10) DEFAULT '', exp_like_count {I} DEFAULT 0,
	coin_tenths {I} DEFAULT 0, coin_day VARCHAR(10) DEFAULT '',
	coin_tip_day VARCHAR(10) DEFAULT '', coin_tip_exp {I} DEFAULT 0,
	created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS posts (
	id {PK}, user_id {I} DEFAULT 0, nickname VARCHAR(64) DEFAULT '匿名', content {LT},
	topic_id {I} DEFAULT 0, ip VARCHAR(64) DEFAULT '', fingerprint VARCHAR(128) DEFAULT '',
	status {I} DEFAULT 1, like_count {I} DEFAULT 0, comment_count {I} DEFAULT 0,
	coin_count {I} DEFAULT 0, is_admin {I} DEFAULT 0, badges VARCHAR(255) DEFAULT '', ai_reviewed {I} DEFAULT 0, created_at VARCHAR(40)
);

CREATE TABLE IF NOT EXISTS medias (
	id {PK}, post_id {I} DEFAULT 0, type VARCHAR(10) DEFAULT 'image', path VARCHAR(255) DEFAULT '', created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS comments (
	id {PK}, post_id {I} DEFAULT 0, user_id {I} DEFAULT 0, nickname VARCHAR(64) DEFAULT '匿名',
	content {LT}, ip VARCHAR(64) DEFAULT '', fingerprint VARCHAR(128) DEFAULT '',
	status {I} DEFAULT 1, ai_reviewed {I} DEFAULT 0, created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS likes (
	id {PK}, post_id {I} DEFAULT 0, user_id {I} DEFAULT 0,
	fingerprint VARCHAR(128) DEFAULT '', created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS topics (
	id {PK}, name VARCHAR(64) NOT NULL UNIQUE, created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS reports (
	id {PK}, post_id {I} DEFAULT 0, reason {LT}, reporter_ip VARCHAR(64) DEFAULT '',
	status {I} DEFAULT 0, ai_result {LT}, created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS admins (
	id {PK}, username VARCHAR(64) NOT NULL UNIQUE, password VARCHAR(128) NOT NULL,
	role VARCHAR(20) DEFAULT 'custom', perms {LT}, created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS settings (k VARCHAR(64) PRIMARY KEY, v {LT});
CREATE TABLE IF NOT EXISTS notices (id {PK}, title VARCHAR(128) DEFAULT '', content {LT}, created_at VARCHAR(40));
CREATE TABLE IF NOT EXISTS apikeys (id {PK}, name VARCHAR(64) DEFAULT '', api_key VARCHAR(64) NOT NULL UNIQUE, status {I} DEFAULT 1, last_used VARCHAR(40) DEFAULT '', created_at VARCHAR(40));
CREATE TABLE IF NOT EXISTS ai_logs (id {PK}, post_id {I} DEFAULT 0, action VARCHAR(16) DEFAULT '', reason {LT}, target VARCHAR(10) DEFAULT 'post', created_at VARCHAR(40));
CREATE TABLE IF NOT EXISTS bans (id {PK}, btype VARCHAR(16) DEFAULT '', bvalue VARCHAR(128) DEFAULT '', created_at VARCHAR(40));
CREATE TABLE IF NOT EXISTS verifications (id {PK}, email VARCHAR(128) NOT NULL, code VARCHAR(10) NOT NULL, expires VARCHAR(40));
CREATE TABLE IF NOT EXISTS plugin_logs (id {PK}, plugin_slug VARCHAR(64) DEFAULT '', action VARCHAR(48) DEFAULT '', detail {LT}, duration_ms {I} DEFAULT 0, created_at VARCHAR(40));
CREATE TABLE IF NOT EXISTS user_prefs (
	user_id {I} PRIMARY KEY, theme VARCHAR(16) DEFAULT 'dark',
	cols {I} DEFAULT 1, font_scale {I} DEFAULT 100, updated_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS user_cards (
	user_id {I} PRIMARY KEY, title VARCHAR(40) DEFAULT '', bio VARCHAR(200) DEFAULT '',
	style VARCHAR(20) DEFAULT 'aurora', cover VARCHAR(255) DEFAULT '', updated_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS food_places (
	id {PK}, user_id {I} DEFAULT 0,
	shop_name VARCHAR(80) DEFAULT '', location VARCHAR(120) DEFAULT '',
	description {LT}, tag VARCHAR(20) DEFAULT '', image VARCHAR(255) DEFAULT '',
	status {I} DEFAULT 1, created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS food_ratings (
	id {PK}, place_id {I} DEFAULT 0, user_id {I} DEFAULT 0,
	score {I} DEFAULT 0, created_at VARCHAR(40), updated_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS food_reviews (
	id {PK}, place_id {I} DEFAULT 0, user_id {I} DEFAULT 0,
	nickname VARCHAR(64) DEFAULT '', content {LT}, status {I} DEFAULT 1, created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS chat_messages (
	id {PK}, user_id {I} DEFAULT 0, content {LT},
	image VARCHAR(255) DEFAULT '', status {I} DEFAULT 1, created_at VARCHAR(40)
);
CREATE TABLE IF NOT EXISTS coin_tips (
	id {PK}, post_id {I} DEFAULT 0, from_user_id {I} DEFAULT 0, to_user_id {I} DEFAULT 0,
	coins {I} DEFAULT 0, xp_sender {I} DEFAULT 0, xp_recipient {I} DEFAULT 0, created_at VARCHAR(40)
);
`

// Migrate 安装时自动建表并创建索引，可重复执行（幂等）
func Migrate() error {
	ddl := schemaDDL
	// MySQL 的 TEXT/MEDIUMTEXT 不允许设置默认值，长文本列统一不使用 DEFAULT
	if Driver == "mysql" {
		ddl = strings.NewReplacer("{PK}", "BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT",
			"{I}", "INT", "{LT}", "MEDIUMTEXT").Replace(ddl)
	} else {
		ddl = strings.NewReplacer("{PK}", "INTEGER PRIMARY KEY AUTOINCREMENT",
			"{I}", "INTEGER", "{LT}", "TEXT").Replace(ddl)
	}
	for _, stmt := range strings.Split(ddl, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := DB.Exec(stmt); err != nil {
			// MySQL 不支持 CREATE INDEX IF NOT EXISTS，重复创建索引时忽略报错
			if !strings.Contains(strings.ToUpper(stmt), "CREATE INDEX") {
				return fmt.Errorf("迁移失败: %v SQL: %.120s", err, stmt)
			}
		}
	}
	// 兼容旧库：posts 增加 is_admin / badges 列（列已存在时报错，忽略即可）
	alter := "ALTER TABLE posts ADD COLUMN is_admin INTEGER DEFAULT 0"
	if Driver == "mysql" {
		alter = "ALTER TABLE posts ADD COLUMN is_admin INT DEFAULT 0"
	}
	_, _ = DB.Exec(alter)
	_, _ = DB.Exec("ALTER TABLE posts ADD COLUMN badges VARCHAR(255) DEFAULT ''")
	// 兼容旧库：apikeys.key 是 MySQL 保留字，统一改名为 api_key（列不存在/已改名时报错，忽略即可）
	rename := `ALTER TABLE apikeys RENAME COLUMN "key" TO "api_key"`
	if Driver == "mysql" {
		rename = "ALTER TABLE apikeys RENAME COLUMN `key` TO `api_key`"
	}
	_, _ = DB.Exec(rename)

	// AI 审核 v2：评论可见性/已审标记、帖子已审标记、日志目标类型（列已存在时报错，忽略即可）
	aiType := "INTEGER"
	if Driver == "mysql" {
		aiType = "INT"
	}
	for _, stmt := range []string{
		"ALTER TABLE comments ADD COLUMN status " + aiType + " DEFAULT 1",
		"ALTER TABLE comments ADD COLUMN ai_reviewed " + aiType + " DEFAULT 0",
		"ALTER TABLE posts ADD COLUMN ai_reviewed " + aiType + " DEFAULT 0",
		"ALTER TABLE posts ADD COLUMN coin_count " + aiType + " DEFAULT 0",
		"ALTER TABLE ai_logs ADD COLUMN target VARCHAR(10) DEFAULT 'post'",
		"ALTER TABLE user_cards ADD COLUMN cover VARCHAR(255) DEFAULT ''",
		"ALTER TABLE food_places ADD COLUMN image VARCHAR(255) DEFAULT ''",
		"ALTER TABLE food_reviews ADD COLUMN nickname VARCHAR(64) DEFAULT ''",
		"ALTER TABLE users ADD COLUMN exp " + aiType + " DEFAULT 0",
		"ALTER TABLE users ADD COLUMN exp_login_day VARCHAR(10) DEFAULT ''",
		"ALTER TABLE users ADD COLUMN exp_post_day VARCHAR(10) DEFAULT ''",
		"ALTER TABLE users ADD COLUMN exp_like_day VARCHAR(10) DEFAULT ''",
		"ALTER TABLE users ADD COLUMN exp_like_count " + aiType + " DEFAULT 0",
		"ALTER TABLE users ADD COLUMN coin_tenths " + aiType + " DEFAULT 0",
		"ALTER TABLE users ADD COLUMN coin_day VARCHAR(10) DEFAULT ''",
		"ALTER TABLE users ADD COLUMN coin_tip_day VARCHAR(10) DEFAULT ''",
		"ALTER TABLE users ADD COLUMN coin_tip_exp " + aiType + " DEFAULT 0",
	} {
		_, _ = DB.Exec(stmt)
	}

	// 兼容旧库：likes 去重后再建唯一索引，防止并发点赞产生重复行
	_, _ = DB.Exec(`DELETE FROM likes WHERE id NOT IN (
		SELECT min_id FROM (SELECT MIN(id) AS min_id FROM likes GROUP BY post_id, fingerprint) t)`)

	// 常用索引（忽略已存在错误）
	indexes := []string{
		"CREATE INDEX idx_posts_created ON posts (created_at)",
		"CREATE INDEX idx_posts_topic ON posts (topic_id)",
		"CREATE INDEX idx_media_post ON medias (post_id)",
		"CREATE INDEX idx_comments_post ON comments (post_id)",
		"CREATE INDEX idx_likes_post ON likes (post_id)",
		"CREATE UNIQUE INDEX idx_likes_uniq ON likes (post_id, fingerprint)",
		"CREATE INDEX idx_food_places_created ON food_places (created_at)",
		"CREATE UNIQUE INDEX idx_food_ratings_uniq ON food_ratings (place_id, user_id)",
		"CREATE INDEX idx_food_reviews_place ON food_reviews (place_id)",
		"CREATE INDEX idx_chat_messages_user ON chat_messages (user_id)",
		"CREATE INDEX idx_coin_tips_post ON coin_tips (post_id)",
		"CREATE INDEX idx_coin_tips_to ON coin_tips (to_user_id)",
		"CREATE INDEX idx_coin_tips_from_to ON coin_tips (from_user_id, to_user_id, created_at)",
	}
	for _, ix := range indexes {
		_, _ = DB.Exec(ix)
	}
	util.Log("info", "数据库迁移完成（驱动: %s）", Driver)
	return nil
}
