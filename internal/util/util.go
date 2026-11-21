// Package util 通用工具：日志、随机串、bcrypt 密码、HMAC 会话、图形验证码、
// 图片转 WebP、上传保存、客户端 IP、HTML 清理等。
package util

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	webpenc "github.com/HugoSmits86/nativewebp"
	"golang.org/x/crypto/bcrypt"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // 注册 WebP 解码器（帖子图/头像可上传 .webp）

	"clearlove/internal/config"
)

// ============ 日志 ============

// Log 统一日志输出（级别 + 时间前缀）
func Log(level, format string, args ...any) {
	log.SetFlags(log.Ldate | log.Ltime)
	log.Printf("[%s] %s", strings.ToUpper(level), fmt.Sprintf(format, args...))
}

// ============ 随机与密码 ============

// RandomHex 生成 n 字节的十六进制随机串
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HashPassword bcrypt 加密密码
func HashPassword(p string) string {
	b, _ := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(b)
}

// CheckPassword 校验密码
func CheckPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

// ============ HMAC 签名会话（无状态，不依赖服务端存储） ============

type sessionClaims struct {
	UID int64 `json:"uid"`
	Exp int64 `json:"exp"`
}

// SignSession 生成签名会话串 base64(payload).hex(hmac)
func SignSession(uid int64, dur time.Duration) string {
	c := sessionClaims{UID: uid, Exp: time.Now().Add(dur).Unix()}
	payload, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, []byte(config.Cfg.Secret))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
}

// VerifySession 校验并解析会话，返回用户 ID
func VerifySession(val string) (int64, bool) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, false
	}
	mac := hmac.New(sha256.New, []byte(config.Cfg.Secret))
	mac.Write(payload)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[1])) {
		return 0, false
	}
	var c sessionClaims
	if err := json.Unmarshal(payload, &c); err != nil || c.Exp < time.Now().Unix() {
		return 0, false
	}
	return c.UID, true
}

// ============ 图形验证码（纯数字4位，SVG 输出，内存过期存储） ============

type captchaEntry struct {
	Code string
	Exp  int64
}

var captchas sync.Map

func init() {
	// 定期回收过期验证码，防止匿名刷 /api/captcha 导致内存无限增长
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			now := time.Now().Unix()
			captchas.Range(func(k, v any) bool {
				if e, ok := v.(captchaEntry); ok && e.Exp <= now {
					captchas.Delete(k)
				}
				return true
			})
		}
	}()
}

// NewCaptcha 生成验证码，返回 ID 与内联 SVG
func NewCaptcha() (string, string) {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	code := fmt.Sprintf("%04d", (int(b[0])<<8|int(b[1]))%10000)
	id := RandomHex(8)
	captchas.Store(id, captchaEntry{Code: code, Exp: time.Now().Add(5 * time.Minute).Unix()})
	return id, captchaSVG(code)
}

// VerifyCaptcha 校验并消费验证码（一次性；原子取出+删除，防并发重放）
func VerifyCaptcha(id, code string) bool {
	v, ok := captchas.LoadAndDelete(id)
	if !ok {
		return false
	}
	e := v.(captchaEntry)
	return e.Exp > time.Now().Unix() && e.Code == strings.TrimSpace(code)
}

// captchaSVG 渲染4位数字验证码为内联 SVG（含干扰线，无外部字体依赖）
func captchaSVG(code string) string {
	colors := []string{"#e05575", "#5b8def", "#3aa675", "#c77b3f"}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 44" width="120" height="44">`)
	b.WriteString(`<rect width="120" height="44" rx="6" fill="#f6f7fb"/>`)
	seed := time.Now().UnixNano()
	for i := 0; i < 4; i++ {
		y := 28 + rand01(seed+int64(i))*6
		rot := (rand01(seed+int64(i)*7) - 0.5) * 40
		fmt.Fprintf(&b, `<text x="%d" y="%.0f" font-size="26" font-weight="bold" fill="%s" font-family="monospace" transform="rotate(%.0f %d 28)">%c</text>`,
			14+i*26, y, colors[i], rot, 14+i*26, code[i])
	}
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&b, `<line x1="%.0f" y1="%.0f" x2="%.0f" y2="%.0f" stroke="%s" stroke-width="1" opacity="0.5"/>`,
			rand01(seed+int64(i))*110, rand01(seed+int64(i)*3)*40, rand01(seed+int64(i)*11)*110, rand01(seed+int64(i)*13)*40, colors[i%4])
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// rand01 由种子产生稳定的 [0,1) 伪随机数
func rand01(seed int64) float64 {
	h := sha256.Sum256([]byte(fmt.Sprint(seed)))
	n := int64(h[0])<<24 | int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
	return math.Abs(float64(n)) / float64(1<<32)
}

// ============ HTML 清理（防 XSS：剥离全部标签与首尾空白） ============

var tagRe = regexp.MustCompile(`<[^>]*>`)
var ctrlRe = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)

// StripHTML 移除 HTML 标签与控制字符，保留换行
func StripHTML(s string) string {
	s = ctrlRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(tagRe.ReplaceAllString(s, ""))
}

// ============ 图片转 WebP 与上传保存 ============

const maxImageDim = 1600 // 图片最长边超过则等比缩小

// ImageToWebP 解码任意已注册格式图片，缩放后编码为 WebP（有损，质量80）
func ImageToWebP(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// 尝试 GIF 首帧
		if g, e2 := gif.Decode(bytes.NewReader(data)); e2 == nil {
			img = g
		} else {
			return nil, fmt.Errorf("图片解码失败: %w", err)
		}
	}
	b := img.Bounds()
	if b.Dx() > maxImageDim || b.Dy() > maxImageDim {
		scale := math.Min(float64(maxImageDim)/float64(b.Dx()), float64(maxImageDim)/float64(b.Dy()))
		dst := image.NewNRGBA(image.Rect(0, 0, int(float64(b.Dx())*scale), int(float64(b.Dy())*scale)))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img = dst
	}
	// 统一转为 NRGBA 以满足编码器要求
	if n, ok := img.(*image.NRGBA); ok {
		// 直接复用
		var buf bytes.Buffer
		if err := webpenc.Encode(&buf, n, &webpenc.Options{UseExtendedFormat: false}); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(n, n.Bounds(), img, b.Min, draw.Src)
	var buf bytes.Buffer
	if err := webpenc.Encode(&buf, n, &webpenc.Options{UseExtendedFormat: false}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true}
var videoExts = map[string]bool{".mp4": true, ".webm": true, ".mov": true, ".m4v": true}

// 上传限制常量
const (
	MaxImageSize = 10 << 20 // 单张图片 10MB
	MaxVideoSize = 70 << 20 // 单个视频 70MB
	MaxImages    = 15       // 图片数量上限
	MaxVideos    = 3        // 视频数量上限
)

// IsImageExt 是否为受支持的图片扩展名
func IsImageExt(ext string) bool { return imageExts[strings.ToLower(ext)] }

// IsVideoExt 是否为受支持的视频扩展名
func IsVideoExt(ext string) bool { return videoExts[strings.ToLower(ext)] }

// SaveImage 保存上传图片（自动转 WebP），返回可访问 URL 路径
func SaveImage(fh *multipart.FileHeader) (string, error) {
	if fh.Size > MaxImageSize {
		return "", fmt.Errorf("单张图片不能超过 10MB")
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !imageExts[ext] {
		return "", fmt.Errorf("不支持的图片格式: %s", ext)
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io_ReadAll(f)
	if err != nil {
		return "", err
	}
	webp, err := ImageToWebP(data)
	if err != nil {
		return "", err
	}
	return writeToUpload(webp, ".webp")
}

// SaveImageBytes 保存内存中的图片数据（自动转 WebP），返回可访问 URL 路径。
// 供插件运行时（clv.media.saveImage）等无法拿到 multipart.FileHeader 的场景使用。
func SaveImageBytes(data []byte) (string, error) {
	if len(data) > MaxImageSize {
		return "", fmt.Errorf("单张图片不能超过 10MB")
	}
	webp, err := ImageToWebP(data)
	if err != nil {
		return "", err
	}
	return writeToUpload(webp, ".webp")
}

// SaveVideo 保存上传视频（校验大小与扩展名），返回可访问 URL 路径
func SaveVideo(fh *multipart.FileHeader) (string, error) {
	if fh.Size > MaxVideoSize {
		return "", fmt.Errorf("单个视频不能超过 70MB")
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !videoExts[ext] {
		return "", fmt.Errorf("不支持的视频格式: %s", ext)
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	return saveStream(f, ext)
}

// SaveVideoBytes 保存内存中的视频数据（校验扩展名与大小），返回可访问 URL 路径。
// 供插件运行时（clv.media.saveVideo）使用。
func SaveVideoBytes(data []byte, filename string) (string, error) {
	if len(data) > MaxVideoSize {
		return "", fmt.Errorf("单个视频不能超过 70MB")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if !videoExts[ext] {
		return "", fmt.Errorf("不支持的视频格式: %s", ext)
	}
	return writeToUpload(data, ext)
}

func saveStream(f multipart.File, ext string) (string, error) {
	sub := time.Now().UTC().Format("20060102")
	dir := filepath.Join(config.Cfg.UploadDir, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := RandomHex(8) + ext
	dst, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := dst.ReadFrom(f); err != nil {
		return "", err
	}
	return "/uploads/" + sub + "/" + name, nil
}

func writeToUpload(data []byte, ext string) (string, error) {
	sub := time.Now().UTC().Format("20060102")
	dir := filepath.Join(config.Cfg.UploadDir, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := RandomHex(8) + ext
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	return "/uploads/" + sub + "/" + name, nil
}

// 头像统一输出尺寸（正方形，256px 足够 2x/3x 屏幕展示）
const avatarSize = 256

// maxAvatarPixels 头像源图最大像素数（约 8000×5000），防止超大图解码耗尽内存
const maxAvatarPixels = 40_000_000

// SaveAvatar 保存用户头像：解码 -> 居中裁成正方形 -> 缩放 -> WebP。
// 前端虽然已经做过可视化裁剪，服务端仍再裁一次，防止绕过或比例不符。
func SaveAvatar(fh *multipart.FileHeader) (string, error) {
	if fh.Size > MaxImageSize {
		return "", fmt.Errorf("头像图片不能超过 10MB")
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !imageExts[ext] {
		return "", fmt.Errorf("不支持的图片格式: %s", ext)
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io_ReadAll(f)
	if err != nil {
		return "", err
	}
	return SaveAvatarBytes(data)
}

// SaveAvatarBytes 把内存中的图片保存为正方形头像，返回 /uploads/avatars/xxx.webp。
// 供上传接口与测试复用。
func SaveAvatarBytes(data []byte) (string, error) {
	if len(data) > MaxImageSize {
		return "", fmt.Errorf("头像图片不能超过 10MB")
	}
	// 先只看头部尺寸，避免超大图片解码时把内存打满（解压炸弹）
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("图片解码失败: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxAvatarPixels {
		return "", fmt.Errorf("图片尺寸过大，请上传 4000 万像素以内的图片")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// 兼容 GIF：取首帧
		if g, e2 := gif.Decode(bytes.NewReader(data)); e2 == nil {
			img = g
		} else {
			return "", fmt.Errorf("图片解码失败: %w", err)
		}
	}
	square := squareImage(img, avatarSize)
	var buf bytes.Buffer
	if err := webpenc.Encode(&buf, square, &webpenc.Options{UseExtendedFormat: false}); err != nil {
		return "", fmt.Errorf("头像编码失败: %w", err)
	}
	return writeAvatar(buf.Bytes())
}

// squareImage 居中裁出正方形并等比缩放到 dim×dim。
func squareImage(src image.Image, dim int) *image.NRGBA {
	b := src.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	if side <= 0 {
		side = 1
	}
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	dst := image.NewNRGBA(image.Rect(0, 0, dim, dim))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src,
		image.Rect(x0, y0, x0+side, y0+side), draw.Src, nil)
	return dst
}

// writeAvatar 写入 uploads/avatars/<random>.webp
func writeAvatar(data []byte) (string, error) {
	dir := filepath.Join(config.Cfg.UploadDir, "avatars")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := RandomHex(8) + ".webp"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	return "/uploads/avatars/" + name, nil
}

// RemoveAvatar 删除旧头像文件。只允许 /uploads/avatars/ 下的单层文件名，
// 避免误删其它上传文件或穿越目录。
func RemoveAvatar(urlPath string) {
	const prefix = "/uploads/avatars/"
	if !strings.HasPrefix(urlPath, prefix) {
		return
	}
	name := strings.TrimPrefix(urlPath, prefix)
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return
	}
	_ = os.Remove(filepath.Join(config.Cfg.UploadDir, "avatars", name))
}

// RemoveUpload 删除一个已上传的媒体文件（medias.path 形如 /uploads/<日期>/<名>）。
// 路径合法性校验：必须以 /uploads/ 开头且不含 ..，防目录穿越。文件不存在时静默成功。
func RemoveUpload(urlPath string) {
	p := strings.TrimPrefix(urlPath, "/uploads/")
	if p == urlPath || strings.Contains(p, "..") || strings.Contains(p, "/.") {
		return
	}
	_ = os.Remove(filepath.Join(config.Cfg.UploadDir, filepath.FromSlash(p)))
}

// io_ReadAll 局部封装避免额外命名冲突
func io_ReadAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

// ============ 请求辅助 ============

// ClientIP 提取客户端真实 IP（支持反代 X-Forwarded-For / X-Real-IP）。
// 仅当直连来源为回环/内网地址（即确实经过本机反向代理转发）时才采信代理头，
// 防止服务直接暴露时攻击者伪造 X-Forwarded-For 绕过 IP 封禁与失败锁定。
func ClientIP(r *http.Request) string {
	remote := remoteIP(r)
	if isTrustedProxy(remote) {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
		if v := r.Header.Get("X-Real-IP"); v != "" {
			return strings.TrimSpace(v)
		}
	}
	return remote
}

// remoteIP 取 TCP 连接的直连对端地址（去掉端口）
func remoteIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	// 去掉 IPv6  zone/方括号
	host = strings.Trim(host, "[]")
	return host
}

// isTrustedProxy 判断直连对端是否为可信代理（回环或 RFC 私有/链路本地地址）
func isTrustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast()
}

// FingerprintOf 获取请求携带的浏览器指纹（前端 JS 计算后随请求提交）
func FingerprintOf(r *http.Request) string {
	fp := r.Header.Get("X-Fingerprint")
	if fp == "" {
		fp = r.FormValue("fingerprint")
	}
	if len(fp) > 128 {
		fp = fp[:128]
	}
	return fp
}

// 名片封面：按 3:1 居中裁切后压缩
const (
	cardCoverW = 1200
	cardCoverH = 400
)

// SaveCardCover 解码上传图片，居中裁成 3:1 并保存为 WebP。
func SaveCardCover(fh *multipart.FileHeader) (string, error) {
	if fh.Size > MaxImageSize {
		return "", fmt.Errorf("封面图片不能超过 10MB")
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !imageExts[ext] {
		return "", fmt.Errorf("不支持的图片格式: %s", ext)
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io_ReadAll(f)
	if err != nil {
		return "", err
	}
	return SaveCardCoverBytes(data)
}

// SaveCardCoverBytes 内存图片 -> 1200×400 WebP，返回 /uploads/cards/xxx.webp。
func SaveCardCoverBytes(data []byte) (string, error) {
	if len(data) > MaxImageSize {
		return "", fmt.Errorf("封面图片不能超过 10MB")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("图片解码失败: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxAvatarPixels {
		return "", fmt.Errorf("图片尺寸过大")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		if g, e2 := gif.Decode(bytes.NewReader(data)); e2 == nil {
			img = g
		} else {
			return "", fmt.Errorf("图片解码失败: %w", err)
		}
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var sw, sh int
	if float64(w)/float64(h) > 3.0 {
		sh = h
		sw = int(float64(h) * 3.0)
	} else {
		sw = w
		sh = int(float64(w) / 3.0)
	}
	if sw < 1 {
		sw = 1
	}
	if sh < 1 {
		sh = 1
	}
	x0 := b.Min.X + (w-sw)/2
	y0 := b.Min.Y + (h-sh)/2
	dst := image.NewNRGBA(image.Rect(0, 0, cardCoverW, cardCoverH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img,
		image.Rect(x0, y0, x0+sw, y0+sh), draw.Src, nil)
	var buf bytes.Buffer
	if err := webpenc.Encode(&buf, dst, &webpenc.Options{UseExtendedFormat: false}); err != nil {
		return "", fmt.Errorf("封面编码失败: %w", err)
	}
	dir := filepath.Join(config.Cfg.UploadDir, "cards")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := RandomHex(8) + ".webp"
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return "/uploads/cards/" + name, nil
}

// RemoveCardCover 删除旧封面，仅允许 /uploads/cards/ 下的单层文件名。
func RemoveCardCover(urlPath string) {
	const prefix = "/uploads/cards/"
	if !strings.HasPrefix(urlPath, prefix) {
		return
	}
	name := strings.TrimPrefix(urlPath, prefix)
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return
	}
	_ = os.Remove(filepath.Join(config.Cfg.UploadDir, "cards", name))
}
