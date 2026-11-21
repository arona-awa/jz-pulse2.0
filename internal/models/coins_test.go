package models

import (
	"path/filepath"
	"testing"

	"clearlove/internal/config"
	"clearlove/internal/database"
)

func TestCoinTipSettlement(t *testing.T) {
	root := t.TempDir()
	config.Cfg = config.Config{
		DataDir: root, UploadDir: filepath.Join(root, "uploads"),
		DBType: "sqlite", SQLitePath: filepath.Join(root, "coins.db"),
		Installed: true, Secret: "coin-test",
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
	res, _ := database.DB.Exec("INSERT INTO users(username,email,password,status,exp,coin_tenths,created_at) VALUES('a','a@example.com','x',1,0,50,?)", Now())
	from, _ := res.LastInsertId()
	res, _ = database.DB.Exec("INSERT INTO users(username,email,password,status,exp,coin_tenths,created_at) VALUES('b','b@example.com','x',1,0,0,?)", Now())
	to, _ := res.LastInsertId()
	res, _ = database.DB.Exec("INSERT INTO posts(user_id,nickname,content,status,created_at) VALUES(?,'b','tip me',1,?)", to, Now())
	post, _ := res.LastInsertId()
	res, _ = database.DB.Exec("INSERT INTO posts(user_id,nickname,content,status,created_at) VALUES(?,'b','another',1,?)", to, Now())
	post2, _ := res.LastInsertId()

	r, err := TipPost(post, from, 2, true, "fptest")
	if err != nil {
		t.Fatal(err)
	}
	if r.BalanceTenths != 30 || r.SenderXP != 10 || r.RecipientXP != 2 || !r.Liked || r.Likes != 1 || r.PostCoins != 2 {
		t.Fatalf("tip = %+v", r)
	}
	if got := QueryInt("SELECT coin_tenths FROM users WHERE id=?", from); got != 30 {
		t.Fatalf("sender balance=%d", got)
	}
	if got := QueryInt("SELECT exp FROM users WHERE id=?", from); got != 10 {
		t.Fatalf("sender exp=%d", got)
	}
	if got := QueryInt("SELECT coin_tenths FROM users WHERE id=?", to); got != 2 {
		t.Fatalf("recipient balance=%d", got)
	}
	if got := QueryInt("SELECT exp FROM users WHERE id=?", to); got != 2 {
		t.Fatalf("recipient exp=%d", got)
	}
	if got := QueryInt("SELECT coin_count FROM posts WHERE id=?", post); got != 2 {
		t.Fatalf("post coins=%d", got)
	}

	// 对同一作者只能投一次：换一篇同作者帖子也不能再投
	if _, err := TipPost(post2, from, 1, false, "fptest"); err == nil {
		t.Fatal("对同一作者重复投币应失败")
	}
	if _, err := TipPost(post, from, 1, false, "fptest"); err == nil {
		t.Fatal("重复投币应失败")
	}
	if _, err := TipPost(post, to, 1, false, "fptest"); err == nil {
		t.Fatal("自投应失败")
	}
}

func TestDailyCoinCatchUp(t *testing.T) {
	root := t.TempDir()
	config.Cfg = config.Config{
		DataDir: root, UploadDir: filepath.Join(root, "uploads"),
		DBType: "sqlite", SQLitePath: filepath.Join(root, "daily.db"),
		Installed: true, Secret: "daily-test",
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
	res, _ := database.DB.Exec("INSERT INTO users(username,email,password,status,coin_tenths,coin_day,created_at) VALUES('d','d@example.com','x',1,0,?,?)", "2000-01-01", Now())
	uid, _ := res.LastInsertId()
	if n := AwardDailyCoin(uid); n <= 0 {
		t.Fatalf("补发天数应大于0，got %d", n)
	}
	if n := AwardDailyCoin(uid); n != 0 {
		t.Fatalf("同日不应重复发放，got %d", n)
	}
}
