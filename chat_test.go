package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/handlers"
	"clearlove/internal/middleware"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

// TestChatRoomHTTP 验证聊天室的核心约束：
// 必须登录、昵称/头像来自账号（不可匿名）、支持文字与图片、消息按游标读取。
func TestChatRoomHTTP(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	uploadDir := filepath.Join(root, "uploads")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config.Cfg = config.Config{
		DataDir:    dataDir,
		UploadDir:  uploadDir,
		DBType:     "sqlite",
		SQLitePath: filepath.Join(dataDir, "clearlove.db"),
		Installed:  true,
		Secret:     "chat-test-secret",
		Version:    config.Version,
	}
	if err := database.Connect(); err != nil {
		t.Fatalf("数据库连接失败: %v", err)
	}
	t.Cleanup(func() {
		if database.DB != nil {
			_ = database.DB.Close()
			database.DB = nil
		}
	})
	if err := database.Migrate(); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	_ = models.SetSetting("site_name", "聊天室测试站")
	handlers.SetupTemplates(webFS)

	alice := createChatUser(t, "alice")
	bob := createChatUser(t, "bob")
	carol := createChatUser(t, "carol")
	dave := createChatUser(t, "dave")
	if _, err := database.DB.Exec(
		"INSERT INTO user_prefs(user_id,theme,cols,font_scale,updated_at) VALUES(?,?,?,?,?)",
		alice, "light", 2, 120, models.Now()); err != nil {
		t.Fatalf("写入个性化设置失败: %v", err)
	}

	srv := middleware.Use(buildMux())
	token := strings.Repeat("a", 32)
	do := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	request := func(method, target string, body io.Reader, uid int64, ajax bool) *http.Request {
		req := httptest.NewRequest(method, target, body)
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(&http.Cookie{Name: "clv_csrf", Value: token})
		req.Header.Set("X-CSRF-Token", token)
		if uid > 0 {
			req.AddCookie(&http.Cookie{Name: "clv_user", Value: util.SignSession(uid, time.Hour)})
		}
		if ajax {
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.Header.Set("Accept", "application/json")
		}
		return req
	}

	// 1) 未登录访问聊天室 -> 跳转登录
	w := do(request(http.MethodGet, "/chat", nil, 0, false))
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "/login?next=/chat") {
		t.Fatalf("未登录应跳转登录页，实际 %d %q", w.Code, w.Header().Get("Location"))
	}

	// 2) 未登录调用消息接口 -> 401
	w = do(request(http.MethodGet, "/api/chat/messages", nil, 0, true))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录消息接口应 401，实际 %d", w.Code)
	}

	// 3) 登录后页面包含聊天室与账号昵称
	w = do(request(http.MethodGet, "/chat", nil, alice, false))
	if w.Code != http.StatusOK {
		t.Fatalf("聊天室页面异常: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"聊天室", "alice", `data-uid="` + strconv.FormatInt(alice, 10) + `"`,
		`data-clv-theme="light"`, `--clv-font-scale: 120%`, "/static/chat.js",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("聊天室页面缺少 %q", want)
		}
	}

	// 4) 匿名发送 -> 401（不能绕过登录）
	w = do(request(http.MethodPost, "/chat/send", bytes.NewBufferString("content=hi"), 0, true))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名发送应 401，实际 %d", w.Code)
	}

	// 5) 登录发送文字；即使提交 nickname 字段也必须显示账号昵称
	form := bytes.NewBufferString("_csrf=" + token + "&content=%3Cb%3Ehello%3C%2Fb%3E&nickname=%E5%8C%BF%E5%90%8D")
	w = do(request(http.MethodPost, "/chat/send", form, alice, true))
	if w.Code != http.StatusOK {
		t.Fatalf("发送文字失败: %d %s", w.Code, w.Body.String())
	}
	var sent struct {
		OK      bool                 `json:"ok"`
		Message handlers.ChatMessage `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
		t.Fatalf("响应解析失败: %v (%s)", err, w.Body.String())
	}
	if !sent.OK || sent.Message.ID == 0 {
		t.Fatalf("发送响应异常: %s", w.Body.String())
	}
	if sent.Message.Nickname != "alice" {
		t.Fatalf("消息昵称必须是账号名 alice，实际 %q", sent.Message.Nickname)
	}
	if sent.Message.Content != "hello" {
		t.Fatalf("消息内容应被清理为 hello，实际 %q", sent.Message.Content)
	}

	// 6) 同一用户立刻再发 -> 冷却 429
	w = do(request(http.MethodPost, "/chat/send", bytes.NewBufferString("_csrf="+token+"&content=again"), alice, true))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("连续发送应被限流 429，实际 %d %s", w.Code, w.Body.String())
	}

	// 7) 另一用户发送纯图片消息
	pngBytes := testPNG(t)
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	_ = mw.WriteField("_csrf", token)
	_ = mw.WriteField("content", "")
	fw, err := mw.CreateFormFile("image", "pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(pngBytes); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	req := request(http.MethodPost, "/chat/send", &mp, bob, true)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w = do(req)
	if w.Code != http.StatusOK {
		t.Fatalf("发送图片失败: %d %s", w.Code, w.Body.String())
	}
	var imageSent struct {
		OK      bool                 `json:"ok"`
		Message handlers.ChatMessage `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &imageSent); err != nil {
		t.Fatalf("图片响应解析失败: %v", err)
	}
	if imageSent.Message.Nickname != "bob" || !strings.HasPrefix(imageSent.Message.Image, "/uploads/") {
		t.Fatalf("图片消息字段异常: %+v", imageSent.Message)
	}
	uploadFile := filepath.Join(uploadDir, strings.TrimPrefix(imageSent.Message.Image, "/uploads/"))
	if _, err := os.Stat(uploadFile); err != nil {
		t.Fatalf("图片文件未落盘: %v", err)
	}

	// 7.5) 普通表单带上空的 file input（浏览器未选图时的行为）仍可发送纯文字
	var emptyFile bytes.Buffer
	efw := multipart.NewWriter(&emptyFile)
	_ = efw.WriteField("_csrf", token)
	_ = efw.WriteField("content", "文字来自普通表单")
	_, err = efw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="image"; filename=""`},
		"Content-Type":        []string{"application/octet-stream"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = efw.Close()
	req = request(http.MethodPost, "/chat/send", &emptyFile, dave, true)
	req.Header.Set("Content-Type", efw.FormDataContentType())
	w = do(req)
	if w.Code != http.StatusOK {
		t.Fatalf("空图片文件应能按纯文字发送，实际 %d %s", w.Code, w.Body.String())
	}
	var plainSent struct {
		OK      bool                 `json:"ok"`
		Message handlers.ChatMessage `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plainSent); err != nil {
		t.Fatalf("空图片响应解析失败: %v", err)
	}
	if plainSent.Message.Content != "文字来自普通表单" || plainSent.Message.Image != "" {
		t.Fatalf("空图片消息字段异常: %+v", plainSent.Message)
	}

	// 8) 游标读取：按时间正序、带账号昵称
	w = do(request(http.MethodGet, "/api/chat/messages?after=0&limit=20", nil, alice, true))
	if w.Code != http.StatusOK {
		t.Fatalf("读取消息失败: %d", w.Code)
	}
	var listResp struct {
		OK       bool                   `json:"ok"`
		Messages []handlers.ChatMessage `json:"messages"`
		HasMore  bool                   `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("列表解析失败: %v", err)
	}
	if len(listResp.Messages) != 3 {
		t.Fatalf("应有 3 条消息，实际 %d", len(listResp.Messages))
	}
	if listResp.Messages[0].ID >= listResp.Messages[1].ID {
		t.Fatalf("消息应按时间正序返回")
	}
	for _, m := range listResp.Messages {
		if m.Nickname == "" || m.Nickname == "匿名" {
			t.Fatalf("消息昵称不能为空或匿名: %+v", m)
		}
	}
	if listResp.Messages[1].Nickname != "bob" || listResp.Messages[1].Image == "" {
		t.Fatalf("图片消息读取异常: %+v", listResp.Messages[1])
	}

	// 9) 普通表单发送（无 JS）回跳聊天室
	req = request(http.MethodPost, "/chat/send", bytes.NewBufferString("_csrf="+token+"&content=from-form"), carol, false)
	w = do(req)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/chat") {
		t.Fatalf("普通表单发送应回跳聊天室，实际 %d %q", w.Code, w.Header().Get("Location"))
	}

	// 10) 页面首屏应渲染历史消息（文字与图片）
	w = do(request(http.MethodGet, "/chat", nil, alice, false))
	body = w.Body.String()
	if !strings.Contains(body, "hello") || !strings.Contains(body, imageSent.Message.Image) {
		t.Fatalf("首屏未渲染历史消息")
	}
}

func createChatUser(t *testing.T, name string) int64 {
	t.Helper()
	res, err := database.DB.Exec(
		"INSERT INTO users(username,email,password,avatar,status,created_at) VALUES(?,?,?,?,1,?)",
		name, name+"@example.com", "x", "", models.Now())
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 1, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
