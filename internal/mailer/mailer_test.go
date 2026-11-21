package mailer

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// TestSendManyBCC 用本地假 SMTP 服务器验证：
// 一封邮件可带多个信封收件人，且邮件头中不暴露收件人地址。
func TestSendManyBCC(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

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
			case cmd == "RSET", cmd == "NOOP":
				write("250 OK\r\n")
			default:
				write("250 OK\r\n")
			}
		}
		done <- res
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	err = SendMany("127.0.0.1", port, "", "", "site@example.com",
		[]string{"a@example.com", "b@example.com"}, "新帖通知", "正文内容")
	if err != nil {
		t.Fatalf("SendMany: %v", err)
	}
	select {
	case res := <-done:
		if res.rcpts != 2 {
			t.Fatalf("收件人数 = %d, want 2", res.rcpts)
		}
		if !strings.Contains(res.data, "正文内容") {
			t.Fatalf("邮件正文缺失: %q", res.data)
		}
		if strings.Contains(res.data, "a@example.com") || strings.Contains(res.data, "b@example.com") {
			t.Fatalf("邮件头暴露了收件人地址: %q", res.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("假 SMTP 服务器未收到邮件")
	}
}
