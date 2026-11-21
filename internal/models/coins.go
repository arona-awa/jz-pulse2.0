package models

import (
	"fmt"
	"strconv"
	"time"

	"clearlove/internal/database"
)

// CoinTipResult 投币成功后的结算结果。
type CoinTipResult struct {
	BalanceTenths int   `json:"balance_tenths"`
	Coins         int   `json:"coins"`
	SenderXP      int   `json:"sender_xp"`
	RecipientXP   int   `json:"recipient_xp"`
	RecipientID   int64 `json:"recipient_id"`
	Likes         int64 `json:"likes"`
	Liked         bool  `json:"liked"`
	PostCoins     int64 `json:"post_coins"`
}

// FormatCoins 将 0.1 枚硬币为单位的余额格式化为 19 / 19.1。
func FormatCoins(tenths int) string {
	if tenths < 0 {
		tenths = 0
	}
	if tenths%10 == 0 {
		return strconv.Itoa(tenths / 10)
	}
	return fmt.Sprintf("%.1f", float64(tenths)/10)
}

// AwardDailyCoin 每天自动 +1 硬币；若多天未访问，返回时补齐漏掉的每日硬币。
// 返回本次实际发放的 0.1 枚数量（例如 1 天 = 10，即 1 枚）。
func AwardDailyCoin(uid int64) int {
	if database.DB == nil || uid <= 0 {
		return 0
	}
	var last string
	if err := database.DB.QueryRow("SELECT IFNULL(coin_day,'') FROM users WHERE id=?", uid).Scan(&last); err != nil {
		return 0
	}
	today := time.Now()
	days := 1
	if last != "" {
		if d, err := time.ParseInLocation("2006-01-02", last, time.Local); err == nil {
			days = int(today.Sub(d).Hours() / 24)
		}
	}
	if days <= 0 {
		return 0
	}
	if days > 3650 {
		days = 3650
	}
	add := days * 10
	res, err := database.DB.Exec(
		"UPDATE users SET coin_tenths=IFNULL(coin_tenths,0)+?, coin_day=? WHERE id=? AND IFNULL(coin_day,'')=?",
		add, today.Format("2006-01-02"), uid, last)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0
	}
	return add
}

// TipPost 给帖子作者投币：投币人每币 +5 经验（每日最多 25），作者每币 +0.1 硬币 +1 经验。
func TipPost(postID, fromID int64, coins int, withLike bool, fingerprint string) (*CoinTipResult, error) {
	if database.DB == nil || postID <= 0 || fromID <= 0 {
		return nil, fmt.Errorf("参数无效")
	}
	if coins < 1 || coins > 2 {
		return nil, fmt.Errorf("每次只能投 1 或 2 枚硬币")
	}
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("投币失败，请稍后再试")
	}
	defer tx.Rollback()

	var toID int64
	var status int
	if err := tx.QueryRow("SELECT user_id,status FROM posts WHERE id=?", postID).Scan(&toID, &status); err != nil {
		return nil, fmt.Errorf("帖子不存在")
	}
	if status != 1 {
		return nil, fmt.Errorf("该帖子当前不可投币")
	}
	if toID <= 0 {
		return nil, fmt.Errorf("该帖没有绑定账号，无法投币")
	}
	if toID == fromID {
		return nil, fmt.Errorf("不能给自己的帖子投币")
	}

	// 同一用户对同一作者只能投币一次（永久，不按天重置）。
	var already int
	if err := tx.QueryRow(
		"SELECT COUNT(1) FROM coin_tips WHERE from_user_id=? AND to_user_id=?",
		fromID, toID).Scan(&already); err != nil {
		already = 0
	}
	if already > 0 {
		return nil, fmt.Errorf("你已经支持过这位作者了，不能重复投币")
	}

	var balance, tipExp int
	var tipDay string
	if err := tx.QueryRow(
		"SELECT IFNULL(coin_tenths,0),IFNULL(coin_tip_day,''),IFNULL(coin_tip_exp,0) FROM users WHERE id=?",
		fromID).Scan(&balance, &tipDay, &tipExp); err != nil {
		return nil, fmt.Errorf("账号不存在")
	}
	cost := coins * 10
	if balance < cost {
		return nil, fmt.Errorf("硬币不足")
	}

	today := Today()
	used := 0
	if tipDay == today {
		used = tipExp
	}
	xpGain := coins * 5
	if used+xpGain > 25 {
		xpGain = 25 - used
	}
	if xpGain < 0 {
		xpGain = 0
	}
	newTipExp := xpGain
	if tipDay == today {
		newTipExp = used + xpGain
	}

	res, err := tx.Exec(
		`UPDATE users SET coin_tenths=IFNULL(coin_tenths,0)-?, exp=IFNULL(exp,0)+?,
		 coin_tip_day=?, coin_tip_exp=?
		 WHERE id=? AND IFNULL(coin_tenths,0)>=?`,
		cost, xpGain, today, newTipExp, fromID, cost)
	if err != nil {
		return nil, fmt.Errorf("投币失败，请稍后再试")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("硬币不足")
	}

	if _, err := tx.Exec(
		"UPDATE users SET coin_tenths=IFNULL(coin_tenths,0)+?, exp=IFNULL(exp,0)+? WHERE id=?",
		coins, coins, toID); err != nil {
		return nil, fmt.Errorf("投币失败，请稍后再试")
	}
	if _, err := tx.Exec("UPDATE posts SET coin_count=IFNULL(coin_count,0)+? WHERE id=?", coins, postID); err != nil {
		return nil, fmt.Errorf("投币失败，请稍后再试")
	}
	if _, err := tx.Exec(
		"INSERT INTO coin_tips(post_id,from_user_id,to_user_id,coins,xp_sender,xp_recipient,created_at) VALUES(?,?,?,?,?,?,?)",
		postID, fromID, toID, coins, xpGain, coins, Now()); err != nil {
		return nil, fmt.Errorf("投币失败，请稍后再试")
	}

	liked := false
	if withLike && fingerprint != "" {
		var exists int
		_ = tx.QueryRow("SELECT COUNT(1) FROM likes WHERE post_id=? AND fingerprint=?", postID, fingerprint).Scan(&exists)
		if exists == 0 {
			_, _ = tx.Exec("INSERT INTO likes(post_id,fingerprint,created_at) VALUES(?,?,?)", postID, fingerprint, Now())
		}
		_, _ = tx.Exec("UPDATE posts SET like_count=(SELECT COUNT(1) FROM likes WHERE post_id=?) WHERE id=?", postID, postID)
		liked = true
	}
	var likes, postCoins int64
	_ = tx.QueryRow("SELECT like_count,coin_count FROM posts WHERE id=?", postID).Scan(&likes, &postCoins)

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("投币失败，请稍后再试")
	}
	return &CoinTipResult{
		BalanceTenths: balance - cost,
		Coins:         coins,
		SenderXP:      xpGain,
		RecipientXP:   coins,
		RecipientID:   toID,
		Likes:         likes,
		Liked:         liked,
		PostCoins:     postCoins,
	}, nil
}
