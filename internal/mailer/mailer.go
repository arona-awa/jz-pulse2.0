// Package mailer SMTP 邮件发送，用于注册验证码。
// 兼容 465 端口（SSL 直连）与 587/25 端口（STARTTLS 或明文）。
package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// Send 发送纯文本邮件。host/port/user/pass/from 来自后台 SMTP 设置
func Send(host string, port string, user, pass, from, to, subject, body string) error {
	addr := net.JoinHostPort(host, port)
	auth := smtp.PlainAuth("", user, pass, host)
	msg := buildMessage(from, to, subject, body)

	if port == "465" {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return fmt.Errorf("SSL 连接失败: %w", err)
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer c.Close()
		if err = c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
		if err = c.Mail(from); err != nil {
			return err
		}
		if err = c.Rcpt(to); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err = w.Write(msg); err != nil {
			return err
		}
		return w.Close()
	}
	return smtp.SendMail(addr, auth, from, []string{to}, msg)
}

// buildMessage 构造含 UTF-8 Base64 主题的邮件正文
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// SendCode 发送注册验证码邮件
func SendCode(host, port, user, pass, from, to, code string) error {
	body := fmt.Sprintf("您正在注册 ClearLove 表白墙，验证码为：%s\n\n验证码 10 分钟内有效，若非本人操作请忽略本邮件。", code)
	return Send(host, port, user, pass, from, to, "ClearLove 表白墙注册验证码", body)
}

// SendMany 将同一封邮件密送给多个收件人（收件人地址不出现在邮件头中）。
// 用于新帖提醒等群发场景，避免逐个建立 SMTP 连接。
func SendMany(host string, port string, user, pass, from string, to []string, subject, body string) error {
	recipients := make([]string, 0, len(to))
	for _, addr := range to {
		if strings.TrimSpace(addr) != "" {
			recipients = append(recipients, strings.TrimSpace(addr))
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	addr := net.JoinHostPort(host, port)
	auth := smtp.PlainAuth("", user, pass, host)
	msg := buildBulkMessage(from, subject, body)

	if port == "465" {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return fmt.Errorf("SSL 连接失败: %w", err)
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer c.Close()
		if err = c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
		if err = c.Mail(from); err != nil {
			return err
		}
		accepted := 0
		var firstErr error
		for _, rcpt := range recipients {
			if err = c.Rcpt(rcpt); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			accepted++
		}
		if accepted == 0 {
			if firstErr != nil {
				return firstErr
			}
			return fmt.Errorf("没有可用的收件人")
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err = w.Write(msg); err != nil {
			return err
		}
		return w.Close()
	}
	return smtp.SendMail(addr, auth, from, recipients, msg)
}

// buildBulkMessage 群发邮件头：To 使用发件地址，收件人全部走 SMTP 信封隐藏。
func buildBulkMessage(from, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + from + "\r\n")
	b.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(body)
	return []byte(b.String())
}
