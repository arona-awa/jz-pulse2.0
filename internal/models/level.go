package models

import (
	"time"

	"clearlove/internal/database"
)

// LevelInfo 账号等级与当前等级内的经验进度。
type LevelInfo struct {
	Num       int  // 1-6
	Exp       int  // 累计经验
	Min       int  // 当前等级起始经验
	Next      int  // 下一等级起始经验（满级为 0）
	Progress  int  // 当前等级进度 0-100
	Remaining int  // 距离下一级还差多少经验
	Max       bool // 是否满级
}

var levelMins = [6]int{0, 101, 250, 480, 990, 1450}

// LevelForExp 按累计经验计算等级。区间：0-100/101-249/250-479/480-989/990-1449/1450+。
func LevelForExp(exp int) LevelInfo {
	if exp < 0 {
		exp = 0
	}
	info := LevelInfo{Exp: exp}
	for i := 5; i >= 0; i-- {
		if exp >= levelMins[i] {
			info.Num = i + 1
			info.Min = levelMins[i]
			break
		}
	}
	if info.Num >= 6 {
		info.Max = true
		info.Progress = 100
		return info
	}
	info.Next = levelMins[info.Num] // 下一等级的起始值
	span := info.Next - info.Min
	if span > 0 {
		info.Progress = (exp - info.Min) * 100 / span
		if info.Progress < 0 {
			info.Progress = 0
		}
		if info.Progress > 100 {
			info.Progress = 100
		}
	}
	info.Remaining = info.Next - exp
	if info.Remaining < 0 {
		info.Remaining = 0
	}
	return info
}

// Today 返回服务器本地日期（每日奖励按本地自然日计算）。
func Today() string { return time.Now().Format("2006-01-02") }

// AwardDailyLogin 每日首次登录 +5，同日重复登录不重复发放。
func AwardDailyLogin(uid int64) bool {
	if database.DB == nil || uid <= 0 {
		return false
	}
	day := Today()
	res, err := database.DB.Exec(
		"UPDATE users SET exp=IFNULL(exp,0)+5, exp_login_day=? WHERE id=? AND IFNULL(exp_login_day,'')<>?",
		day, uid, day)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// AwardDailyPost 每日首次投稿 +5。
func AwardDailyPost(uid int64) bool {
	if database.DB == nil || uid <= 0 {
		return false
	}
	day := Today()
	res, err := database.DB.Exec(
		"UPDATE users SET exp=IFNULL(exp,0)+5, exp_post_day=? WHERE id=? AND IFNULL(exp_post_day,'')<>?",
		day, uid, day)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// AwardDailyLike 点赞帖子 +5，每天最多计入 2 次。
func AwardDailyLike(uid int64) bool {
	if database.DB == nil || uid <= 0 {
		return false
	}
	day := Today()
	res, err := database.DB.Exec(
		`UPDATE users SET exp=IFNULL(exp,0)+5,
		 exp_like_count=CASE WHEN exp_like_day=? THEN IFNULL(exp_like_count,0)+1 ELSE 1 END,
		 exp_like_day=?
		 WHERE id=? AND (IFNULL(exp_like_day,'')<>? OR IFNULL(exp_like_count,0)<2)`,
		day, day, uid, day)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}
