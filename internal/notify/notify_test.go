package notify

import (
	"bufio"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/database"
	"clearlove/internal/models"
)

func TestNewPostNotifiesAllActiveUsers(t *testing.T) {
	root := t.TempDir()
	config.Cfg = config.Config{
		DataDir: root, UploadDir: filepath.Join(root, "uploads"),
		DBType: "sqlite", SQLitePath: filepath.Join(root, "test.db"),
		Installed: true, Secret: "notify-test",
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
	users := []struct {
		name, email string
		status      int
	}{
		{"author", "author@example.com", 1},
		{"user2", "u2@example.com", 1},
		{"user3", "u3@example.com", 1},
		{"disabled", "off@example.com", 0},
		{"placeholder", "legacy_9@migrated.local", 1},
	}
	for _, u := range users {
		if _, err := database.DB.Exec(
			"INSERT INTO users(username,email,password,status,created_at) VALUES(?,?,?,?,?)",
			u.name, u.email, "x", u.status, models.Now()); err != nil {
			t.Fatal(err)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	for k, v := range map[string]string{
		"new_post_mail": "1",
		"smtp_host":     "127.0.0.1",
		"smtp_port":     port,
		"smtp_user":     "bot",
		"smtp_pass":     "secret",
		"smtp_from":     "site@example.com",
		"site_name":     "测试站点",
		"site_url":      "https://example.test",
	} {
		if err := models.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		rcpts int
		data  string
	}
	done := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(conn)
		w := bufio.NewWriter(conn)
		write := func(s string) { _, _ = w.WriteString(s); _ = w.Flush() }
		write("220 localhost ESMTP\r\n")
		res := result{}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				write("250-localhost\r\n250-AUTH PLAIN\r\n250 OK\r\n")
			case strings.HasPrefix(cmd, "AUTH PLAIN"):
				write("235 Authentication successful\r\n")
			case strings.HasPrefix(cmd, "MAIL FROM"):
				write("250 OK\r\n")
			case strings.HasPrefix(cmd, "RCPT TO"):
				res.rcpts++
				write("250 OK\r\n")
			case cmd == "DATA":
				write("354 End data with <CR><LF>.<CR><LF>\r\n")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						break
					}
					if strings.TrimRight(l, "\r\n") == "." {
						break
					}
					b.WriteString(l)
				}
				res.data = b.String()
				write("250 OK queued\r\n")
			case cmd == "QUIT":
				write("221 Bye\r\n")
				done <- res
				return
			default:
				write("250 OK\r\n")
			}
		}
		done <- res
	}()

	NewPost(42, 1, "作者", "这是新帖正文 测试内容")

	select {
	case res := <-done:
		if res.rcpts != 3 {
			t.Fatalf("收件人数 = %d, want 3（作者也应收到，禁用用户排除）", res.rcpts)
		}
		for _, want := range []string{"测试站点", "这是新帖正文", "https://example.test/post/42", "请勿直接回复"} {
			if !strings.Contains(res.data, want) {
				t.Fatalf("邮件缺少 %q: %q", want, res.data)
			}
		}
		if strings.Contains(res.data, "author@example.com") || strings.Contains(res.data, "u2@example.com") || strings.Contains(res.data, "off@example.com") {
			t.Fatalf("邮件头不应暴露任何收件人地址: %q", res.data)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("等待通知邮件超时")
	}
}
