package migrate

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/models"
)

// buildOldDB 构造一个旧版（PHP 1.x）结构与数据的 SQLite 数据库
func buildOldDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("创建旧库失败: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE posts (id INTEGER PRIMARY KEY AUTOINCREMENT, nickname TEXT NOT NULL, content TEXT NOT NULL,
		 topic TEXT, media TEXT, likes INTEGER DEFAULT 0, is_pinned INTEGER DEFAULT 0, ip_address TEXT,
		 fingerprint TEXT, status INTEGER DEFAULT 0, is_admin INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, post_id INTEGER NOT NULL, nickname TEXT NOT NULL,
		 content TEXT NOT NULL, ip_address TEXT, fingerprint TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE topics (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE announcements (id INTEGER PRIMARY KEY AUTOINCREMENT, content TEXT NOT NULL, image TEXT,
		 active INTEGER DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE admins (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, password TEXT NOT NULL,
		 role TEXT NOT NULL DEFAULT 'admin', permissions TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE, password TEXT NOT NULL,
		 email TEXT, email_verified INTEGER DEFAULT 0, verification_token TEXT, email_notify INTEGER DEFAULT 1,
		 created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE banned_devices (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT NOT NULL, value TEXT NOT NULL,
		 reason TEXT DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP, UNIQUE (type, value))`,
		`CREATE TABLE reports (id INTEGER PRIMARY KEY AUTOINCREMENT, post_id INTEGER NOT NULL, reason TEXT NOT NULL,
		 detail TEXT, reporter_ip TEXT, reporter_fingerprint TEXT, status INTEGER DEFAULT 0, handled_by INTEGER DEFAULT 0,
		 handled_at DATETIME, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("旧库建表失败: %v", err)
		}
	}
	seed := []string{
		`INSERT INTO topics(name) VALUES('校园'),('日常')`,
		// 正常帖：1 张图片、5 个赞、status=0（旧版 0 = 正常展示）
		`INSERT INTO posts(nickname,content,topic,media,likes,ip_address,fingerprint,status,is_admin,created_at)
		 VALUES('小明','第一条帖子','校园','[{"file":"a.webp","type":"image"}]',5,'1.1.1.1','fp1',0,0,'2026-01-02 03:04:05')`,
		// 被隐藏的帖子：status=1
		`INSERT INTO posts(nickname,content,topic,media,likes,status,created_at)
		 VALUES('被屏蔽者','这条被隐藏了','日常','[]',0,1,'2026-01-03 03:04:05')`,
		// 管理员帖：is_admin=1
		`INSERT INTO posts(nickname,content,topic,media,likes,status,is_admin,created_at)
		 VALUES('官方','管理员帖','','[]',2,0,1,'2026-01-04 03:04:05')`,
		`INSERT INTO comments(post_id,nickname,content,ip_address,fingerprint,created_at) VALUES(1,'小红','评论一','2.2.2.2','fp2','2026-01-05 03:04:05')`,
		`INSERT INTO comments(post_id,nickname,content,created_at) VALUES(1,'小刚','评论二','2026-01-06 03:04:05')`,
		`INSERT INTO comments(post_id,nickname,content,created_at) VALUES(999,'幽灵','孤儿评论','2026-01-07 03:04:05')`,
		`INSERT INTO announcements(content,active,created_at) VALUES('欢迎来到表白墙','1','2026-01-08 03:04:05')`,
		`INSERT INTO announcements(content,active,created_at) VALUES('已停用公告','0','2026-01-09 03:04:05')`,
		`INSERT INTO users(username,password,email,created_at) VALUES('alice','$2y$10$abcdefghijklmnopqrstuv','a@b.com','2026-01-10 03:04:05')`,
		`INSERT INTO users(username,password,email,created_at) VALUES('bob','$2y$10$abcdefghijklmnopqrstuv','','2026-01-11 03:04:05')`,
		`INSERT INTO admins(username,password,role) VALUES('boss','$2y$10$abcdefghijklmnopqrstuv','super')`,
		`INSERT INTO admins(username,password,role) VALUES('mod','$2y$10$abcdefghijklmnopqrstuv','admin')`,
		`INSERT INTO banned_devices(type,value) VALUES('ip','9.9.9.9'),('fingerprint','badfp')`,
		`INSERT INTO reports(post_id,reason,detail,reporter_ip,status) VALUES(1,'广告','发小广告','3.3.3.3',0)`,
		`INSERT INTO reports(post_id,reason,detail,reporter_ip,status) VALUES(999,'违规','帖子已删','4.4.4.4',0)`,
	}
	for _, s := range seed {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("旧库写入测试数据失败: %v\nSQL: %s", err, s)
		}
	}
}

// setupNew 准备新版数据库环境
func setupNew(t *testing.T, root string) {
	t.Helper()
	newDir := filepath.Join(root, "new")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config.Cfg = config.Config{
		DataDir:    newDir,
		UploadDir:  filepath.Join(root, "newuploads"),
		DBType:     "sqlite",
		SQLitePath: filepath.Join(newDir, "clearlove.db"),
		Installed:  true,
		Secret:     "test-secret",
	}
	if err := database.Connect(); err != nil {
		t.Fatalf("新版数据库连接失败: %v", err)
	}
	// Windows 下临时目录清理前必须关闭连接，否则文件被占用
	t.Cleanup(func() {
		if database.DB != nil {
			_ = database.DB.Close()
			database.DB = nil
		}
	})
	if err := database.Migrate(); err != nil {
		t.Fatalf("新版数据库迁移失败: %v", err)
	}
}

func TestRunMigration(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	oldUploads := filepath.Join(oldDir, "uploads")
	if err := os.MkdirAll(oldUploads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldUploads, "a.webp"), []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldDB := filepath.Join(oldDir, "data.db")
	buildOldDB(t, oldDB)
	setupNew(t, root)

	st, err := Run(oldDB, oldUploads, false)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 统计断言
	if st.Posts != 3 {
		t.Errorf("帖子数 = %d, 期望 3", st.Posts)
	}
	if st.Comments != 2 {
		t.Errorf("评论数 = %d, 期望 2（孤儿评论应被跳过）", st.Comments)
	}
	if st.Media != 1 {
		t.Errorf("媒体数 = %d, 期望 1", st.Media)
	}
	if st.Likes != 7 {
		t.Errorf("点赞补齐 = %d, 期望 7", st.Likes)
	}
	if st.Users != 2 {
		t.Errorf("用户数 = %d, 期望 2", st.Users)
	}
	if st.Admins != 2 {
		t.Errorf("管理员数 = %d, 期望 2", st.Admins)
	}
	if st.Bans != 2 {
		t.Errorf("封禁数 = %d, 期望 2", st.Bans)
	}
	if st.Reports != 1 {
		t.Errorf("举报数 = %d, 期望 1（关联帖子不存在的应跳过）", st.Reports)
	}
	if st.Notices != 1 {
		t.Errorf("公告数 = %d, 期望 1（停用公告不导入）", st.Notices)
	}
	if st.NoPostRef != 2 {
		t.Errorf("无效关联数 = %d, 期望 2（1 条评论 + 1 条举报）", st.NoPostRef)
	}

	db := database.DB
	// ID 保留
	if n := models.QueryInt("SELECT COUNT(1) FROM posts WHERE id IN (1,2,3)"); n != 3 {
		t.Errorf("帖子 ID 未保留，命中 %d 条", n)
	}
	// status 取反：旧 0 -> 新 1，旧 1 -> 新 0
	if n := models.QueryInt("SELECT status FROM posts WHERE id=1"); n != 1 {
		t.Errorf("帖子1 status = %d, 期望 1（旧版 0 为正常）", n)
	}
	if n := models.QueryInt("SELECT status FROM posts WHERE id=2"); n != 0 {
		t.Errorf("帖子2 status = %d, 期望 0（旧版 1 为隐藏）", n)
	}
	// 管理员帖
	if n := models.QueryInt("SELECT is_admin FROM posts WHERE id=3"); n != 1 {
		t.Errorf("帖子3 is_admin = %d, 期望 1", n)
	}
	// 点赞数与 likes 记录一致
	if n := models.QueryInt("SELECT like_count FROM posts WHERE id=1"); n != 5 {
		t.Errorf("帖子1 点赞数 = %d, 期望 5", n)
	}
	if n := models.QueryInt("SELECT COUNT(1) FROM likes WHERE post_id=1"); n != 5 {
		t.Errorf("帖子1 likes 记录 = %d, 期望 5", n)
	}
	// 评论数重算
	if n := models.QueryInt("SELECT comment_count FROM posts WHERE id=1"); n != 2 {
		t.Errorf("帖子1 评论数 = %d, 期望 2", n)
	}
	// 话题关联
	if n := models.QueryInt("SELECT topic_id FROM posts WHERE id=1"); n == 0 {
		t.Error("帖子1 未关联话题")
	}
	if n := models.QueryInt("SELECT COUNT(1) FROM topics WHERE name='校园'"); n != 1 {
		t.Error("话题「校园」未迁移")
	}
	// 媒体：文件复制 + 记录写入
	if _, err := os.Stat(filepath.Join(config.Cfg.UploadDir, "legacy", "a.webp")); err != nil {
		t.Errorf("媒体文件未复制: %v", err)
	}
	if got := models.QueryStr("SELECT path FROM medias WHERE post_id=1"); got != "/uploads/legacy/a.webp" {
		t.Errorf("媒体路径 = %q", got)
	}
	// 时间格式统一为 RFC3339
	created := models.QueryStr("SELECT created_at FROM posts WHERE id=1")
	if _, err := sql.Open("sqlite", oldDB); err == nil {
		if created != "2026-01-02T03:04:05Z" {
			t.Errorf("created_at = %q, 期望 2026-01-02T03:04:05Z", created)
		}
	}
	// 用户：空邮箱补齐占位
	if got := models.QueryStr("SELECT email FROM users WHERE username='bob'"); got == "" {
		t.Error("用户 bob 的邮箱未补齐")
	}
	// 密码哈希原样保留（PHP bcrypt 与 Go bcrypt 兼容）
	if got := models.QueryStr("SELECT password FROM admins WHERE username='boss'"); got != "$2y$10$abcdefghijklmnopqrstuv" {
		t.Errorf("管理员密码未保留: %q", got)
	}
	if got := models.QueryStr("SELECT role FROM admins WHERE username='boss'"); got != "super" {
		t.Errorf("超管角色 = %q", got)
	}
	if got := models.QueryStr("SELECT role FROM admins WHERE username='mod'"); got != "custom" {
		t.Errorf("普通管理员角色 = %q, 期望 custom", got)
	}
	// 举报 reason 合并 detail
	if got := models.QueryStr("SELECT reason FROM reports WHERE id=1"); got != "广告：发小广告" {
		t.Errorf("举报理由 = %q", got)
	}

	// 幂等：再次执行不应重复写入
	st2, err := Run(oldDB, oldUploads, false)
	if err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	if st2.Posts != 0 || st2.Comments != 0 || st2.Users != 0 || st2.Admins != 0 {
		t.Errorf("二次迁移不应新增数据: %+v", st2)
	}
	if n := models.QueryInt("SELECT COUNT(1) FROM posts"); n != 3 {
		t.Errorf("二次迁移后帖子数 = %d, 期望 3", n)
	}
	if n := models.QueryInt("SELECT COUNT(1) FROM likes"); n != 7 {
		t.Errorf("二次迁移后点赞记录 = %d, 期望 7", n)
	}
	_ = db
}

// TestMySQLDumpMigration 验证从 MySQL 导出文件（宝塔 / mysqldump）迁移。
// 反引号在 Go 原始字符串里无法书写，用 « 占位后替换。
func TestMySQLDumpMigration(t *testing.T) {
	root := t.TempDir()
	setupNew(t, root)

	dumpRaw := `-- MySQL dump 10.13  Distrib 5.7.44, for Linux (x86_64)
/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40000 ALTER TABLE «posts» DISABLE KEYS */;

DROP TABLE IF EXISTS «posts»;
CREATE TABLE «posts» (
  «id» int(11) NOT NULL AUTO_INCREMENT,
  «nickname» varchar(50) NOT NULL,
  «content» text NOT NULL,
  «topic» varchar(100) DEFAULT NULL,
  «media» text,
  «likes» int(11) DEFAULT '0',
  «is_pinned» tinyint(1) DEFAULT '0',
  «ip_address» varchar(45) DEFAULT NULL,
  «fingerprint» varchar(64) DEFAULT NULL,
  «status» tinyint(4) DEFAULT '0',
  «is_admin» tinyint(4) DEFAULT '0',
  «created_at» datetime DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY («id»),
  KEY «idx_created» («created_at»)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO «posts» VALUES (1,'小明','第一条帖\n带换行和单引号''测试','校园','[{"file":"a.webp","type":"image"}]',5,0,'1.1.1.1','fp1',0,0,'2026-01-02 03:04:05'),(2,'被屏蔽者','隐藏帖','日常',NULL,0,0,NULL,NULL,1,0,'2026-01-03 03:04:05');

CREATE TABLE «comments» (
  «id» int(11) NOT NULL AUTO_INCREMENT,
  «post_id» int(11) NOT NULL,
  «nickname» varchar(50) NOT NULL,
  «content» text NOT NULL,
  «created_at» datetime DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY («id»)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO «comments» («id», «post_id», «nickname», «content», «created_at») VALUES (1,1,'小红','评论一','2026-01-05 03:04:05'),(2,999,'幽灵','孤儿评论','2026-01-07 03:04:05');

CREATE TABLE «announcements» (
  «id» int(11) NOT NULL AUTO_INCREMENT,
  «content» text NOT NULL,
  «active» tinyint(1) DEFAULT '1',
  «created_at» datetime DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY («id»)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO «announcements» VALUES (1,'欢迎来到表白墙',1,'2026-01-08 03:04:05'),(2,'已停用公告',0,'2026-01-09 03:04:05');

CREATE TABLE «users» (
  «id» int(11) NOT NULL AUTO_INCREMENT,
  «username» varchar(50) NOT NULL,
  «password» varchar(255) NOT NULL,
  «email» varchar(100) DEFAULT NULL,
  «created_at» datetime DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY («id»)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO «users» VALUES (1,'alice','$2y$10$abcdefghijklmnopqrstuv','a@b.com','2026-01-10 03:04:05'),(2,'bob','$2y$10$abcdefghijklmnopqrstuv',NULL,'2026-01-11 03:04:05');

CREATE TABLE «admins» (
  «id» int(11) NOT NULL AUTO_INCREMENT,
  «username» varchar(50) NOT NULL,
  «password» varchar(255) NOT NULL,
  «role» varchar(10) NOT NULL DEFAULT 'admin',
  PRIMARY KEY («id»)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO «admins» VALUES (1,'boss','$2y$10$abcdefghijklmnopqrstuv','super'),(2,'mod','$2y$10$abcdefghijklmnopqrstuv','admin');

CREATE TABLE «banned_devices» (
  «id» int(11) NOT NULL AUTO_INCREMENT,
  «type» varchar(15) NOT NULL,
  «value» varchar(64) NOT NULL,
  PRIMARY KEY («id»)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO «banned_devices» VALUES (1,'ip','9.9.9.9'),(2,'fingerprint','badfp');

CREATE TABLE «reports» (
  «id» int(11) NOT NULL AUTO_INCREMENT,
  «post_id» int(11) NOT NULL,
  «reason» varchar(50) NOT NULL,
  «detail» text,
  «reporter_ip» varchar(45) DEFAULT NULL,
  «status» tinyint(4) DEFAULT '0',
  «created_at» datetime DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY («id»)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO «reports» VALUES (1,1,'广告','发小广告','3.3.3.3',0,'2026-01-12 03:04:05'),(2,1,'重复','重复贴','5.5.5.5',2,'2026-01-13 03:04:05'),(3,999,'违规','帖子已删','4.4.4.4',0,'2026-01-14 03:04:05');

/*!40000 ALTER TABLE «posts» ENABLE KEYS */;
-- Dump completed on 2026-09-13  0:00:00
`
	dump := strings.NewReplacer("«", "`", "»", "`").Replace(dumpRaw)

	oldDir := filepath.Join(root, "old")
	oldUploads := filepath.Join(oldDir, "uploads")
	if err := os.MkdirAll(oldUploads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldUploads, "a.webp"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	dumpPath := filepath.Join(oldDir, "clearlove.sql")
	if err := os.WriteFile(dumpPath, []byte(dump), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Run(dumpPath, oldUploads, false)
	if err != nil {
		t.Fatalf("从 MySQL 导出文件迁移失败: %v", err)
	}
	if st.Source != "mysqldump" {
		t.Errorf("数据源识别为 %q, 期望 mysqldump", st.Source)
	}
	if st.Posts != 2 || st.Comments != 1 || st.Media != 1 || st.Likes != 5 {
		t.Errorf("统计异常: 帖子=%d 评论=%d 媒体=%d 点赞=%d", st.Posts, st.Comments, st.Media, st.Likes)
	}
	if st.Users != 2 || st.Admins != 2 || st.Bans != 2 || st.Reports != 2 || st.Notices != 1 {
		t.Errorf("统计异常: 用户=%d 管理员=%d 封禁=%d 举报=%d 公告=%d",
			st.Users, st.Admins, st.Bans, st.Reports, st.Notices)
	}
	if st.NoPostRef != 2 {
		t.Errorf("无效关联数 = %d, 期望 2（1 条孤儿评论 + 1 条失效举报）", st.NoPostRef)
	}
	// 转义与引号还原
	content := models.QueryStr("SELECT content FROM posts WHERE id=1")
	if content != "第一条帖\n带换行和单引号'测试" {
		t.Errorf("转义解析异常: %q", content)
	}
	// 状态取反
	if n := models.QueryInt("SELECT status FROM posts WHERE id=2"); n != 0 {
		t.Errorf("隐藏帖 status = %d, 期望 0", n)
	}
	// 无列名列表的 INSERT 也能按建表顺序解析（posts 表即为此种写法）
	if n := models.QueryInt("SELECT like_count FROM posts WHERE id=1"); n != 5 {
		t.Errorf("点赞数 = %d, 期望 5", n)
	}
	// 邮箱为空时补齐占位
	if got := models.QueryStr("SELECT email FROM users WHERE username='bob'"); got == "" {
		t.Error("bob 的邮箱未补齐")
	}
	// 举报状态归一
	if n := models.QueryInt("SELECT status FROM reports WHERE id=2"); n != 1 {
		t.Errorf("举报2 状态 = %d, 期望 1（旧版 2 表示已处理）", n)
	}
}

// TestRunDryRun 试运行不应写入任何数据
func TestRunDryRun(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	oldUploads := filepath.Join(oldDir, "uploads")
	if err := os.MkdirAll(oldUploads, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDB := filepath.Join(oldDir, "data.db")
	buildOldDB(t, oldDB)
	setupNew(t, root)

	st, err := Run(oldDB, oldUploads, true)
	if err != nil {
		t.Fatalf("试运行失败: %v", err)
	}
	if st.Posts == 0 {
		t.Error("试运行应统计出帖子数量")
	}
	// 试运行不会真正写入帖子，评论/举报的关联判定不能因此失效
	if st.Comments == 0 || st.Reports == 0 {
		t.Errorf("试运行应统计出评论与举报: 评论=%d 举报=%d", st.Comments, st.Reports)
	}
	if n := models.QueryInt("SELECT COUNT(1) FROM posts"); n != 0 {
		t.Errorf("试运行写入了 %d 条帖子", n)
	}
}
