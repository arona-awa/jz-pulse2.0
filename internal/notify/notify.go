// Package notify 站内事件邮件通知。
// 新帖发布后把提醒放入内存队列，由后台协程异步群发，绝不阻塞发帖请求。
package notify

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"clearlove/internal/database"
	"clearlove/internal/mailer"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

const (
	mailBatchSize     = 20 // 每封密送邮件的收件人数
	mailBatchPause    = 300 * time.Millisecond
	mailMaxRecipients = 2000 // 单次通知最多发送人数，防止异常数据拖垮 SMTP
	mailSnippetRunes  = 140
)

type postNotice struct {
	ID       int64
	AuthorID int64
	Nickname string
	Content  string
}

var (
	queue     = make(chan postNotice, 64)
	startOnce sync.Once
)

// NewPost 触发新帖邮件提醒。只入队，立即返回；SMTP 发送在后台串行进行。
func NewPost(postID, authorID int64, nickname, content string) {
	startOnce.Do(func() { go worker() })
	n := postNotice{ID: postID, AuthorID: authorID, Nickname: nickname, Content: content}
	select {
	case queue <- n:
	default:
		util.Log("warn", "新帖邮件通知队列已满，跳过帖子 #%d", postID)
	}
}

func worker() {
	for n := range queue {
		sendNewPost(n)
	}
}

func sendNewPost(n postNotice) {
	if models.GetSetting("new_post_mail") != "1" {
		return
	}
	host := strings.TrimSpace(models.GetSetting("smtp_host"))
	port := strings.TrimSpace(models.GetSetting("smtp_port"))
	user := strings.TrimSpace(models.GetSetting("smtp_user"))
	pass := models.GetSetting("smtp_pass")
	from := strings.TrimSpace(models.GetSetting("smtp_from"))
	if host == "" || port == "" {
		return
	}
	if from == "" {
		from = user
	}
	if from == "" {
		return
	}
	if database.DB == nil {
		return
	}

	// 收件人：所有启用账号且填写了邮箱的用户（包含作者本人）。
	rows, err := database.DB.Query(
		`SELECT email FROM users
		 WHERE status=1 AND email<>''
		 ORDER BY id LIMIT ?`, mailMaxRecipients)
	if err != nil {
		util.Log("warn", "读取新帖邮件收件人失败: %v", err)
		return
	}
	defer rows.Close()
	recipients := make([]string, 0, 256)
	skipped := 0
	for rows.Next() {
		var email string
		if rows.Scan(&email) == nil {
			if usableEmail(email) {
				recipients = append(recipients, strings.TrimSpace(email))
			} else {
				skipped++
			}
		}
	}
	if skipped > 0 {
		util.Log("info", "新帖邮件通知 #%d：跳过 %d 个无效/迁移占位邮箱", n.ID, skipped)
	}
	if len(recipients) == 0 {
		return
	}

	siteName := strings.TrimSpace(models.GetSetting("site_name"))
	if siteName == "" {
		siteName = "校园墙"
	}
	siteURL := strings.TrimRight(strings.TrimSpace(models.GetSetting("site_url")), "/")
	nickname := strings.TrimSpace(n.Nickname)
	if nickname == "" {
		nickname = "匿名"
	}
	subject := "【" + siteName + "】有新帖子发布"
	body := buildBody(siteName, siteURL, nickname, n.Content, n.ID)

	sent, failed := 0, 0
	var firstErr error
	for i := 0; i < len(recipients); i += mailBatchSize {
		end := i + mailBatchSize
		if end > len(recipients) {
			end = len(recipients)
		}
		batch := recipients[i:end]
		if err := mailer.SendMany(host, port, user, pass, from, batch, subject, body); err != nil {
			failed += len(batch)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			sent += len(batch)
		}
		if end < len(recipients) {
			time.Sleep(mailBatchPause)
		}
	}
	if failed > 0 {
		util.Log("warn", "新帖邮件通知 #%d：成功 %d，失败 %d，首个错误: %v", n.ID, sent, failed, firstErr)
	} else {
		util.Log("info", "新帖邮件通知 #%d：已发送 %d 人", n.ID, sent)
	}
}

// usableEmail 过滤迁移占位地址与明显无效的邮箱，避免浪费 SMTP 额度。
func usableEmail(email string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" || strings.ContainsAny(e, " \t\r\n") || !strings.Contains(e, "@") {
		return false
	}
	if strings.HasSuffix(e, ".local") || strings.Contains(e, "@migrated.") {
		return false
	}
	return true
}

func buildBody(siteName, siteURL, nickname, content string, postID int64) string {
	snippet := strings.Join(strings.Fields(content), " ")
	r := []rune(snippet)
	if len(r) > mailSnippetRunes {
		snippet = string(r[:mailSnippetRunes]) + "…"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 有新帖子发布：\n\n", siteName)
	fmt.Fprintf(&b, "%s：\n%s\n\n", nickname, snippet)
	if siteURL != "" {
		fmt.Fprintf(&b, "查看帖子：%s/post/%d\n\n", siteURL, postID)
	} else {
		b.WriteString("请登录站点查看最新帖子。\n\n")
	}
	b.WriteString("—— 此邮件由系统自动发送，请勿直接回复。")
	return b.String()
}
