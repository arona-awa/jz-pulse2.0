package models

import (
	"path/filepath"
	"testing"

	"clearlove/internal/config"
	"clearlove/internal/database"
)

func TestDailyExpAwards(t *testing.T) {
	root := t.TempDir()
	config.Cfg = config.Config{
		DataDir: root, UploadDir: filepath.Join(root, "uploads"),
		DBType: "sqlite", SQLitePath: filepath.Join(root, "test.db"),
		Installed: true, Secret: "level-test",
	}
	if err := database.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			_ = database.DB.Close()
			database.DB = nil
		}
	})
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	res, err := database.DB.Exec(
		"INSERT INTO users(username,email,password,status,exp,created_at) VALUES(?,?,?,1,0,?)",
		"tester", "tester@example.com", "x", Now())
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()

	if !AwardDailyLogin(uid) || AwardDailyLogin(uid) {
		t.Fatal("每日登录应只奖励一次")
	}
	if !AwardDailyPost(uid) || AwardDailyPost(uid) {
		t.Fatal("每日首次投稿应只奖励一次")
	}
	if !AwardDailyLike(uid) || !AwardDailyLike(uid) || AwardDailyLike(uid) {
		t.Fatal("每日点赞应最多奖励两次")
	}
	exp := QueryInt("SELECT exp FROM users WHERE id=?", uid)
	if exp != 20 {
		t.Fatalf("经验 = %d, want 20", exp)
	}
	// 跨天：把登录日期改成昨天后应能再次领取
	_, _ = database.DB.Exec("UPDATE users SET exp_login_day='2000-01-01' WHERE id=?", uid)
	if !AwardDailyLogin(uid) {
		t.Fatal("跨天后登录奖励应再次发放")
	}
	if got := QueryInt("SELECT exp FROM users WHERE id=?", uid); got != 25 {
		t.Fatalf("跨天经验 = %d, want 25", got)
	}
}
