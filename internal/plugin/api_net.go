// Host API：网络与其它能力（clv.http / clv.cache / clv.crypto / clv.mail / clv.media）。
package plugin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"clearlove/internal/mailer"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

const (
	maxHTTPBody = 2 << 20 // 单次出网响应上限 2MB
	maxCacheTTL = 24 * time.Hour
)

func (rt *jsRuntime) installNet(clv *goja.Object) error {
	// ---------- clv.http ----------
	h := rt.vm.NewObject()
	_ = h.Set("request", rt.jsFn(rt.httpRequest))
	_ = clv.Set("http", h)

	// ---------- clv.cache ----------
	c := rt.vm.NewObject()
	_ = c.Set("get", rt.jsFn(rt.cacheGet))
	_ = c.Set("set", rt.jsFn(rt.cacheSet))
	_ = c.Set("del", rt.jsFn(rt.cacheDel))
	_ = clv.Set("cache", c)

	// ---------- clv.crypto ----------
	cr := rt.vm.NewObject()
	_ = cr.Set("bcrypt", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return nil, errors.New("clv.crypto.bcrypt(pwd) 需要一个参数")
		}
		return util.HashPassword(strOf(args[0])), nil
	}))
	_ = cr.Set("verify", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) < 2 {
			return nil, errors.New("clv.crypto.verify(hash, pwd) 需要两个参数")
		}
		return util.CheckPassword(strOf(args[0]), strOf(args[1])), nil
	}))
	_ = cr.Set("hmacSha256", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) < 2 {
			return nil, errors.New("clv.crypto.hmacSha256(key, msg) 需要两个参数")
		}
		m := hmac.New(sha256.New, []byte(strOf(args[0])))
		m.Write([]byte(strOf(args[1])))
		return hex.EncodeToString(m.Sum(nil)), nil
	}))
	_ = cr.Set("randomHex", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		n := 16
		if len(args) > 0 {
			if v := intOf(args[0]); v > 0 && v <= 64 {
				n = v
			}
		}
		return util.RandomHex(n), nil
	}))
	_ = cr.Set("uuid", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		return randomUUID(), nil
	}))
	_ = cr.Set("base64Encode", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return "", nil
		}
		return base64.StdEncoding.EncodeToString([]byte(strOf(args[0]))), nil
	}))
	_ = cr.Set("base64Decode", rt.jsFn(func(call goja.FunctionCall) (any, error) {
		args := argsOf(call)
		if len(args) == 0 {
			return "", nil
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strOf(args[0])))
		if err != nil {
			return nil, errors.New("base64 解码失败")
		}
		return string(b), nil
	}))
	_ = clv.Set("crypto", cr)

	// ---------- clv.mail ----------
	m := rt.vm.NewObject()
	_ = m.Set("send", rt.jsFn(rt.mailSend))
	_ = clv.Set("mail", m)

	// ---------- clv.media ----------
	md := rt.vm.NewObject()
	_ = md.Set("saveImage", rt.jsFn(rt.mediaSaveImage))
	_ = md.Set("saveVideo", rt.jsFn(rt.mediaSaveVideo))
	_ = clv.Set("media", md)

	return nil
}

// ---------- http ----------

func (rt *jsRuntime) httpRequest(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("net"); err != nil {
		return nil, err
	}
	arg := mapOfAny(call.Argument(0))
	if arg == nil {
		return nil, errors.New("clv.http.request({url, method, headers, body}) 需要一个对象参数")
	}
	rawURL := strings.TrimSpace(strOf(arg["url"]))
	if rawURL == "" {
		return nil, errors.New("缺少 url")
	}
	// 声明 net.local 权限的插件允许访问内网/本机（例如部署在本机的 Ollama 等 AI 服务）
	allowLocal := rt.app != nil && rt.app.Plugin.HasPermission("net.local")
	if err := guardSSRF(rawURL, allowLocal); err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(strOf(arg["method"])))
	if method == "" {
		method = http.MethodGet
	}
	timeoutMS := intOf(arg["timeout_ms"])
	if timeoutMS <= 0 || timeoutMS > 30000 {
		timeoutMS = 10000
	}
	maxBytes := intOf(arg["max_bytes"])
	if maxBytes <= 0 || maxBytes > maxHTTPBody {
		maxBytes = 1 << 20
	}

	var body io.Reader
	if b := strOf(arg["body"]); b != "" {
		body = strings.NewReader(b)
	}
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if hdrs, ok := arg["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			req.Header.Set(k, strOf(v))
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "ClearLove-Plugin/1.0")
	}

	// 拨号期校验实际解析到的 IP（防 DNS rebinding：校验与拨号共用同一次解析结果，
	// 并直接连接已校验的 IP），重定向目标同样复查（防外站 302 跳内网绕过）。
	transport := &http.Transport{DialContext: ssrfGuardedDial(allowLocal)}
	client := &http.Client{
		Timeout:   time.Duration(timeoutMS) * time.Millisecond,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("重定向次数过多")
			}
			return guardSSRF(req.URL.String(), allowLocal)
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("响应体超过 %d 字节上限", maxBytes)
	}
	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	return map[string]any{"status": resp.StatusCode, "headers": headers, "body": string(data)}, nil
}

// guardSSRF 拒绝内网/环回/链路本地地址，防止插件被用作内网探测跳板。
// allowLocal 为 true（插件已声明 net.local 权限）时跳过内网校验，
// 以支持访问部署在本机/内网的 AI 服务（Ollama、LM Studio、内网网关等）。
func guardSSRF(rawURL string, allowLocal bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL 无法解析: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("仅支持 http/https 协议")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("URL 缺少主机名")
	}
	if allowLocal {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("禁止访问内网地址 %s", ip)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("域名解析失败: %w", err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("禁止访问内网地址（%s 解析到 %s）", host, ip)
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// ssrfGuardedDial 返回带内网校验的拨号函数：域名解析后逐 IP 校验，
// 仅连接通过校验的 IP，消除"校验时公网、拨号时内网"的 DNS rebinding 窗口。
func ssrfGuardedDial(allowLocal bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if allowLocal {
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		}
		if ip := net.ParseIP(host); ip != nil {
			if isPrivateIP(ip) {
				return nil, fmt.Errorf("禁止连接内网地址 %s", ip)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ipa := range ips {
			if isPrivateIP(ipa.IP) {
				lastErr = fmt.Errorf("禁止连接内网地址（%s 解析到 %s）", host, ipa.IP)
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("无法解析主机 %s", host)
		}
		return nil, lastErr
	}
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------- cache ----------

type cacheEntry struct {
	val any
	exp time.Time
}

var pluginCache sync.Map

func cacheKey(slug, k string) string { return slug + "\x00" + k }

func (rt *jsRuntime) cacheGet(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("cache"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.cache.get(key) 需要一个参数")
	}
	key := cacheKey(rt.plugin.SlugOf(), strOf(args[0]))
	v, ok := pluginCache.Load(key)
	if !ok {
		return nil, nil
	}
	e := v.(cacheEntry)
	if !e.exp.IsZero() && time.Now().After(e.exp) {
		pluginCache.Delete(key)
		return nil, nil
	}
	return e.val, nil
}

func (rt *jsRuntime) cacheSet(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("cache"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) < 2 {
		return nil, errors.New("clv.cache.set(key, value, ttlSec?) 至少需要两个参数")
	}
	ttl := time.Duration(0)
	if len(args) > 2 {
		if sec := intOf(args[2]); sec > 0 {
			d := time.Duration(sec) * time.Second
			if d > maxCacheTTL {
				d = maxCacheTTL
			}
			ttl = d
		}
	}
	e := cacheEntry{val: args[1]}
	if ttl > 0 {
		e.exp = time.Now().Add(ttl)
	}
	pluginCache.Store(cacheKey(rt.plugin.SlugOf(), strOf(args[0])), e)
	return nil, nil
}

func (rt *jsRuntime) cacheDel(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("cache"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) == 0 {
		return nil, errors.New("clv.cache.del(key) 需要一个参数")
	}
	pluginCache.Delete(cacheKey(rt.plugin.SlugOf(), strOf(args[0])))
	return nil, nil
}

// ---------- mail ----------

func (rt *jsRuntime) mailSend(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("mail"); err != nil {
		return nil, err
	}
	args := argsOf(call)
	if len(args) < 3 {
		return nil, errors.New("clv.mail.send(to, subject, body) 需要三个参数")
	}
	host := models.GetSetting("smtp_host")
	if host == "" {
		return nil, errors.New("站点未配置 SMTP 邮箱服务")
	}
	err := mailer.Send(host, models.GetSetting("smtp_port"),
		models.GetSetting("smtp_user"), models.GetSetting("smtp_pass"),
		models.GetSetting("smtp_from"), strOf(args[0]), strOf(args[1]), strOf(args[2]))
	if err != nil {
		return nil, err
	}
	return true, nil
}

// ---------- media ----------

func (rt *jsRuntime) mediaSaveImage(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("media.write"); err != nil {
		return nil, err
	}
	arg := mapOfAny(call.Argument(0))
	if arg == nil {
		return nil, errors.New("clv.media.saveImage({name, data}) 需要一个对象参数（data 为 base64）")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strOf(arg["data"])))
	if err != nil {
		return nil, errors.New("图片数据不是合法的 base64")
	}
	if len(raw) > 10<<20 {
		return nil, errors.New("图片不能超过 10MB")
	}
	path, err := util.SaveImageBytes(raw)
	if err != nil {
		return nil, err
	}
	return path, nil
}

// mediaSaveVideo 保存视频：clv.media.saveVideo({name, data})，data 为 base64
func (rt *jsRuntime) mediaSaveVideo(call goja.FunctionCall) (any, error) {
	if err := rt.requirePerm("media.write"); err != nil {
		return nil, err
	}
	arg := mapOfAny(call.Argument(0))
	if arg == nil {
		return nil, errors.New("clv.media.saveVideo({name, data}) 需要一个对象参数（data 为 base64）")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strOf(arg["data"])))
	if err != nil {
		return nil, errors.New("视频数据不是合法的 base64")
	}
	path, err := util.SaveVideoBytes(raw, strOf(arg["name"]))
	if err != nil {
		return nil, err
	}
	return path, nil
}
