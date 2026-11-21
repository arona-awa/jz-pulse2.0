// 聊天室：所有已登录用户共享的公开聊天，支持文字与图片。
// 昵称与头像始终取自登录账号，不提供昵称输入，因此不存在匿名发言。
package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"clearlove/internal/database"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

const (
	chatPageSize = 40                     // 单次加载/首屏消息条数
	chatMaxRunes = 1000                   // 单条消息最大字数
	chatMaxBody  = 12 << 20               // 请求体上限（图片 10MB）
	chatInterval = 700 * time.Millisecond // 同一用户两次发送的最小间隔
)

// ChatMessage 聊天室消息（JSON 字段同时供首屏模板与前端轮询使用）
type ChatMessage struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Nickname  string `json:"nickname"`
	Avatar    string `json:"avatar"`
	Content   string `json:"content"`
	Image     string `json:"image"`
	Level     int    `json:"level"`
	CreatedAt string `json:"created_at"`
}

// chatSelect 统一的消息查询：INNER JOIN users 保证每条消息都有真实账号，
// 同时隐藏已禁用/已删除用户的历史消息，从数据层杜绝匿名消息。
const chatSelect = `SELECT m.id,m.user_id,u.username,IFNULL(u.avatar,''),IFNULL(m.content,''),IFNULL(m.image,''),IFNULL(u.exp,0),IFNULL(m.created_at,'')
FROM chat_messages m JOIN users u ON u.id=m.user_id
WHERE m.status=1 AND u.status=1`

var (
	chatRateMu   sync.Mutex
	chatLastSent = map[int64]time.Time{}
)

// reserveChatSend 原子地占用发送冷却窗口，防止并发请求绕过限流。
// 返回占用时间，发送失败时凭它调用 releaseChatSend 立即释放。
func reserveChatSend(uid int64) (time.Time, bool) {
	chatRateMu.Lock()
	defer chatRateMu.Unlock()
	now := time.Now()
	if t, ok := chatLastSent[uid]; ok && now.Sub(t) < chatInterval {
		return time.Time{}, false
	}
	// 防止 map 随用户数无限增长：条目较多时清理 10 分钟前的记录
	if len(chatLastSent) > 2048 {
		for k, t := range chatLastSent {
			if now.Sub(t) > 10*time.Minute {
				delete(chatLastSent, k)
			}
		}
	}
	chatLastSent[uid] = now
	return now, true
}

// releaseChatSend 发送失败时归还冷却窗口，避免一次失败让用户等待。
func releaseChatSend(uid int64, reservedAt time.Time) {
	chatRateMu.Lock()
	if t, ok := chatLastSent[uid]; ok && t.Equal(reservedAt) {
		delete(chatLastSent, uid)
	}
	chatRateMu.Unlock()
}

// listChatMessages 按游标读取消息。
//   - after>0：读取该 ID 之后的消息，按时间正序返回；
//   - before>0：读取该 ID 之前的历史消息，先倒序取出再翻转为正序；
//   - 都为 0：读取最新一页。
//
// 返回 hasMore 表示当前方向上是否还有更多消息。
func listChatMessages(after, before int64, limit int) ([]ChatMessage, bool) {
	out := []ChatMessage{}
	if database.DB == nil {
		return out, false
	}
	if limit < 1 {
		limit = chatPageSize
	}
	if limit > 100 {
		limit = 100
	}

	q := chatSelect
	args := []any{}
	if after > 0 {
		q += " AND m.id > ? ORDER BY m.id ASC LIMIT ?"
		args = append(args, after, limit+1)
	} else {
		if before > 0 {
			q += " AND m.id < ?"
			args = append(args, before)
		}
		q += " ORDER BY m.id DESC LIMIT ?"
		args = append(args, limit+1)
	}

	rows, err := database.DB.Query(q, args...)
	if err != nil {
		util.Log("warn", "读取聊天消息失败: %v", err)
		return out, false
	}
	defer rows.Close()
	for rows.Next() {
		var m ChatMessage
		var exp int
		if rows.Scan(&m.ID, &m.UserID, &m.Nickname, &m.Avatar, &m.Content, &m.Image, &exp, &m.CreatedAt) == nil {
			m.Level = models.LevelForExp(exp).Num
			out = append(out, m)
		}
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	if after == 0 {
		// 倒序查询的结果翻转为时间正序，便于直接渲染
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, hasMore
}

// loadChatMessage 读取单条消息（发送成功后回传前端）
func loadChatMessage(id int64) *ChatMessage {
	m := &ChatMessage{}
	var exp int
	err := database.DB.QueryRow(chatSelect+" AND m.id=?", id).
		Scan(&m.ID, &m.UserID, &m.Nickname, &m.Avatar, &m.Content, &m.Image, &exp, &m.CreatedAt)
	if err != nil {
		return nil
	}
	m.Level = models.LevelForExp(exp).Num
	return m
}

// ChatPage 聊天室页面（必须登录）
func ChatPage(w http.ResponseWriter, r *http.Request) {
	if CurrentUser(r) == nil {
		http.Redirect(w, r, "/login?next=/chat", http.StatusFound)
		return
	}
	msgs, hasMore := listChatMessages(0, 0, chatPageSize)
	var oldest int64
	if len(msgs) > 0 {
		oldest = msgs[0].ID
	}
	Render(w, r, "layout.html", "pg_chat", map[string]any{
		"messages": msgs,
		"hasMore":  hasMore,
		"oldest":   oldest,
		"err":      r.URL.Query().Get("err"),
	})
}

// APIChatMessages 拉取消息：?after= 增量轮询，?before= 加载更早历史
func APIChatMessages(w http.ResponseWriter, r *http.Request) {
	if CurrentUser(r) == nil {
		fail(w, http.StatusUnauthorized, "请先登录后再查看聊天室")
		return
	}
	after := formInt64(r, "after")
	before := formInt64(r, "before")
	if after > 0 {
		before = 0 // 增量与历史游标互斥，优先增量
	}
	limit := int(formInt64(r, "limit"))
	msgs, hasMore := listChatMessages(after, before, limit)
	okJSON(w, map[string]any{"messages": msgs, "has_more": hasMore})
}

// ChatSend 发送消息（文字或图片，至少一项）。AJAX 返回 JSON，普通表单回跳聊天室。
func ChatSend(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	if u == nil {
		chatFail(w, r, http.StatusUnauthorized, "请先登录后再聊天")
		return
	}

	isMultipart := strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data")
	if isMultipart {
		r.Body = http.MaxBytesReader(w, r.Body, chatMaxBody)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			chatFail(w, r, http.StatusBadRequest, "上传内容过大或表单无效")
			return
		}
	} else {
		_ = r.ParseForm()
	}

	content := strings.TrimSpace(util.StripHTML(r.FormValue("content")))
	if len([]rune(content)) > chatMaxRunes {
		chatFail(w, r, http.StatusBadRequest, "消息不能超过 1000 字")
		return
	}
	reservedAt, allowed := reserveChatSend(u.ID)
	if !allowed {
		chatFail(w, r, http.StatusTooManyRequests, "发送太快了，请稍后再试")
		return
	}

	image := ""
	if isMultipart {
		f, fh, err := r.FormFile("image")
		switch {
		case err == nil:
			// 浏览器未选择文件时也会提交一个文件名为空的部分，按纯文字处理
			if strings.TrimSpace(fh.Filename) == "" {
				_ = f.Close()
				break
			}
			p, saveErr := util.SaveImage(fh)
			_ = f.Close()
			if saveErr != nil {
				releaseChatSend(u.ID, reservedAt)
				chatFail(w, r, http.StatusBadRequest, "图片："+saveErr.Error())
				return
			}
			image = p
		case errors.Is(err, http.ErrMissingFile):
			// 没有选择图片，按纯文字消息处理
		default:
			releaseChatSend(u.ID, reservedAt)
			chatFail(w, r, http.StatusBadRequest, "图片上传失败，请重试")
			return
		}
	}

	if content == "" && image == "" {
		releaseChatSend(u.ID, reservedAt)
		chatFail(w, r, http.StatusBadRequest, "请输入内容或选择图片")
		return
	}

	res, err := database.DB.Exec(
		"INSERT INTO chat_messages(user_id,content,image,status,created_at) VALUES(?,?,?,1,?)",
		u.ID, content, image, models.Now())
	if err != nil {
		if image != "" {
			util.RemoveUpload(image)
		}
		releaseChatSend(u.ID, reservedAt)
		util.Log("warn", "聊天消息写入失败: %v", err)
		chatFail(w, r, http.StatusInternalServerError, "发送失败，请稍后重试")
		return
	}

	id, _ := res.LastInsertId()
	m := loadChatMessage(id)
	if m == nil {
		// 理论上不会发生；兜底返回刚插入的内容
		m = &ChatMessage{
			ID: id, UserID: u.ID, Nickname: u.Username, Avatar: u.Avatar,
			Content: content, Image: image, Level: models.LevelForExp(u.Exp).Num, CreatedAt: models.Now(),
		}
	}

	if isAJAX(r) || strings.Contains(r.Header.Get("Accept"), "application/json") {
		okJSON(w, map[string]any{"message": m})
		return
	}
	http.Redirect(w, r, "/chat#chat-list", http.StatusSeeOther)
}

// chatFail 统一的失败输出：AJAX 返回 JSON，普通表单回跳聊天室并带上错误提示
func chatFail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if code == http.StatusUnauthorized && !isAJAX(r) && !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Redirect(w, r, "/login?next=/chat", http.StatusSeeOther)
		return
	}
	if isAJAX(r) || strings.Contains(r.Header.Get("Accept"), "application/json") {
		fail(w, code, msg)
		return
	}
	http.Redirect(w, r, "/chat?err="+url.QueryEscape(msg), http.StatusSeeOther)
}
