// 用户认证：登录、注册（可选邮箱验证码）、登出。
package handlers

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"clearlove/internal/database"
	"clearlove/internal/mailer"
	"clearlove/internal/models"
	"clearlove/internal/plugin"
	"clearlove/internal/util"
)

// Go 正则不支持 \u 转义，使用 \x{...} 表示中文区间
var usernameRe = regexp.MustCompile(`^[\w\x{4e00}-\x{9fa5}]{3,20}$`)

// ---------- 通用失败计数（防爆破/轰炸，进程内内存态） ----------

type tryEntry struct {
	Count int
	Until time.Time
}

var (
	codeTryMu     sync.Mutex
	codeTries     = map[string]tryEntry{}
	codeTriesSeen = time.Now()
)

// codeTryBlocked 是否被锁定（连续失败 5 次锁 10 分钟）
func codeTryBlocked(key string) bool {
	codeTryMu.Lock()
	defer codeTryMu.Unlock()
	t, ok := codeTries[key]
	if !ok {
		return false
	}
	if time.Now().After(t.Until) {
		delete(codeTries, key)
		return false
	}
	return t.Count >= 5
}

func codeTryFail(key string) {
	codeTryMu.Lock()
	defer codeTryMu.Unlock()
	// 定期整体回收过期条目，防止伪造 key 导致 map 无界增长
	if time.Since(codeTriesSeen) > time.Hour {
		codeTriesSeen = time.Now()
		now := time.Now()
		for k, v := range codeTries {
			if now.After(v.Until) {
				delete(codeTries, k)
			}
		}
	}
	t := codeTries[key]
	t.Count++
	t.Until = time.Now().Add(10 * time.Minute)
	codeTries[key] = t
}

func codeTryReset(key string) {
	codeTryMu.Lock()
	defer codeTryMu.Unlock()
	delete(codeTries, key)
}

// codeTryCount 当前失败次数（未锁定也返回计数，用于发码限流判断）
func codeTryCount(key string) int {
	codeTryMu.Lock()
	defer codeTryMu.Unlock()
	t, ok := codeTries[key]
	if !ok || time.Now().After(t.Until) {
		return 0
	}
	return t.Count
}

// LoginPage 登录页
func LoginPage(w http.ResponseWriter, r *http.Request) {
	Render(w, r, "layout.html", "pg_login", map[string]any{
		"err":  r.URL.Query().Get("err"),
		"next": r.URL.Query().Get("next"),
	})
}

// LoginSubmit 登录校验，成功写入签名 Cookie（30天）
func LoginSubmit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	var id int64
	var hash string
	var status int
	err := database.DB.QueryRow("SELECT id,password,status FROM users WHERE username=?", username).
		Scan(&id, &hash, &status)
	if err != nil || !util.CheckPassword(hash, password) {
		Render(w, r, "layout.html", "pg_login", map[string]any{"err": "用户名或密码错误"})
		return
	}
	if status != 1 {
		Render(w, r, "layout.html", "pg_login", map[string]any{"err": "账号已被禁用"})
		return
	}
	// 应用型插件可拦截登录（filter.auth.login，如封禁名单、黑名单校验）
	if !plugin.ApplyFilterBool("filter.auth.login", true, map[string]any{"user_id": id, "username": username}) {
		Render(w, r, "layout.html", "pg_login", map[string]any{"err": "登录被站点插件拦截"})
		return
	}
	models.AwardDailyLogin(id)
	http.SetCookie(w, &http.Cookie{
		Name: "clv_user", Value: util.SignSession(id, 30*24*time.Hour), Path: "/",
		MaxAge: 30 * 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	plugin.Emit("user_login", map[string]any{"user_id": id, "username": username})
	next := r.FormValue("next")
	if next == "" || !strings.HasPrefix(next, "/") {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// Logout 登出
func Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "clv_user", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// RegisterPage 注册页
func RegisterPage(w http.ResponseWriter, r *http.Request) {
	Render(w, r, "layout.html", "pg_register", map[string]any{
		"err":      r.URL.Query().Get("err"),
		"allow":    models.GetSetting("allow_register") == "1",
		"needCode": models.GetSetting("smtp_host") != "",
	})
}

// RegisterSubmit 注册。配置了 SMTP 时必须校验邮箱验证码
func RegisterSubmit(w http.ResponseWriter, r *http.Request) {
	if models.GetSetting("allow_register") != "1" {
		http.Error(w, "站点已关闭注册", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	code := strings.TrimSpace(r.FormValue("code"))
	q := func(msg string) {
		http.Redirect(w, r, "/register?err="+urlQueryEscape(msg), http.StatusSeeOther)
	}

	if !usernameRe.MatchString(username) {
		q("用户名需为 3-20 位字母、数字、下划线或中文")
		return
	}
	// 管理员专享昵称不允许被注册，避免冒充官方身份
	if models.IsAdminNickname(username) {
		q("该名称为管理员专享，请更换用户名")
		return
	}
	if _, err := mail.ParseAddress(email); err != nil {
		q("邮箱格式不正确")
		return
	}
	if len(password) < 6 {
		q("密码至少 6 位")
		return
	}
	if models.QueryInt("SELECT COUNT(1) FROM users WHERE username=?", username) > 0 {
		q("用户名已被占用")
		return
	}
	// SMTP 已配置则强制校验验证码
	if models.GetSetting("smtp_host") != "" {
		key := "vcode:" + email
		var vid int64
		var dbCode string
		var exp string
		err := database.DB.QueryRow(
			"SELECT id,code,expires FROM verifications WHERE email=? ORDER BY id DESC LIMIT 1", email).Scan(&vid, &dbCode, &exp)
		if err != nil || !codeValid(exp) {
			q("邮箱验证码错误或已过期")
			return
		}
		if dbCode != code {
			// 防爆破：同一邮箱连续错误 5 次即作废当前验证码，必须重新获取
			if codeTryBlocked(key) {
				_, _ = database.DB.Exec("DELETE FROM verifications WHERE id=?", vid)
				codeTryReset(key)
				q("验证码错误次数过多，请重新获取验证码")
				return
			}
			codeTryFail(key)
			q("邮箱验证码错误或已过期")
			return
		}
		codeTryReset(key)
	}
	// 新用户注册即获得 30 经验；当天登录奖励视为已领取，避免重复 +5。
	res, err := database.DB.Exec(
		"INSERT INTO users(username,email,password,status,exp,exp_login_day,created_at) VALUES(?,?,?,1,30,?,?)",
		username, email, util.HashPassword(password), models.Today(), models.Now())
	if err != nil {
		q("注册失败，请稍后重试")
		return
	}
	uid, _ := res.LastInsertId()
	plugin.Emit("user_registered", map[string]any{"user_id": uid, "username": username})
	_, _ = database.DB.Exec("DELETE FROM verifications WHERE email=?", email)
	http.SetCookie(w, &http.Cookie{
		Name: "clv_user", Value: util.SignSession(uid, 30*24*time.Hour), Path: "/",
		MaxAge: 30 * 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// codeValid 验证码 10 分钟有效期
func codeValid(expRFC string) bool {
	t, err := time.Parse(time.RFC3339, expRFC)
	return err == nil && time.Now().UTC().Before(t)
}

// RegisterCode 发送注册验证码（POST /api/register-code）
func RegisterCode(w http.ResponseWriter, r *http.Request) {
	host := models.GetSetting("smtp_host")
	if host == "" {
		fail(w, 400, "站点未配置邮箱服务，无需验证码")
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if _, err := mail.ParseAddress(email); err != nil {
		fail(w, 400, "邮箱格式不正确")
		return
	}
	// 发码限流：同一邮箱 10 分钟内最多 3 次，同一 IP 10 分钟内最多 10 次，防 SMTP 轰炸
	ip := util.ClientIP(r)
	if codeTryCount("send:"+email) >= 3 {
		fail(w, 429, "发送过于频繁，请稍后再试")
		return
	}
	if codeTryCount("sendip:"+ip) >= 10 {
		fail(w, 429, "发送过于频繁，请稍后再试")
		return
	}
	if models.QueryInt("SELECT COUNT(1) FROM users WHERE email=?", email) > 0 {
		fail(w, 400, "该邮箱已注册")
		return
	}
	code := fmt.Sprintf("%06d", randomInt(1000000))
	// 同邮箱旧验证码作废，避免多条并存增加爆破面
	_, _ = database.DB.Exec("DELETE FROM verifications WHERE email=?", email)
	_, err := database.DB.Exec(
		"INSERT INTO verifications(email,code,expires) VALUES(?,?,?)",
		email, code, time.Now().UTC().Add(10*time.Minute).Format(time.RFC3339))
	if err != nil {
		fail(w, 500, "验证码生成失败")
		return
	}
	codeTryFail("send:" + email)
	codeTryFail("sendip:" + ip)
	err = mailer.SendCode(host, models.GetSetting("smtp_port"),
		models.GetSetting("smtp_user"), models.GetSetting("smtp_pass"),
		models.GetSetting("smtp_from"), email, code)
	if err != nil {
		util.Log("error", "验证码邮件发送失败: %v", err)
		fail(w, 500, "验证码发送失败，请稍后重试")
		return
	}
	okJSON(w, map[string]any{"msg": "验证码已发送，请查收邮箱"})
}

// randomInt [0,n) 随机整数
func randomInt(n int) int {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	v := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if v < 0 {
		v = -v
	}
	return v % n
}

// urlQueryEscape 简单查询串转义
func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "&", "%26"), "+", "%2B")
}
