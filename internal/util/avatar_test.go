package util

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/webp"

	"clearlove/internal/config"
)

// withUploadDir 把上传目录临时指向 t.TempDir()，测试结束自动还原。
func withUploadDir(t *testing.T) string {
	t.Helper()
	old := config.Cfg.UploadDir
	dir := t.TempDir()
	config.Cfg.UploadDir = dir
	t.Cleanup(func() { config.Cfg.UploadDir = old })
	return dir
}

func avatarPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成测试图片失败: %v", err)
	}
	return buf.Bytes()
}

// TestSaveAvatarBytes 验证非正方形图片会被居中裁成正方形并缩放到 avatarSize。
func TestSaveAvatarBytes(t *testing.T) {
	dir := withUploadDir(t)

	p, err := SaveAvatarBytes(avatarPNG(t, 120, 80))
	if err != nil {
		t.Fatalf("保存头像失败: %v", err)
	}
	if !strings.HasPrefix(p, "/uploads/avatars/") || !strings.HasSuffix(p, ".webp") {
		t.Fatalf("头像路径不符合约定: %q", p)
	}

	full := filepath.Join(dir, "avatars", strings.TrimPrefix(p, "/uploads/avatars/"))
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("读取头像文件失败: %v", err)
	}
	img, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("头像不是有效的 WebP: %v", err)
	}
	if b := img.Bounds(); b.Dx() != avatarSize || b.Dy() != avatarSize {
		t.Fatalf("头像尺寸应为 %d×%d，实际 %d×%d", avatarSize, avatarSize, b.Dx(), b.Dy())
	}
}

// TestSaveAvatarBytesRejectsInvalid 验证非图片数据被拒绝，且不会落盘。
func TestSaveAvatarBytesRejectsInvalid(t *testing.T) {
	dir := withUploadDir(t)
	if _, err := SaveAvatarBytes([]byte("not an image")); err == nil {
		t.Fatal("非图片数据应当报错")
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "avatars")); err == nil && len(entries) > 0 {
		t.Fatalf("失败的保存不应留下文件，实际有 %d 个", len(entries))
	}
}

// TestRemoveAvatar 验证只删除 /uploads/avatars/ 下的文件，拒绝目录穿越。
func TestRemoveAvatar(t *testing.T) {
	dir := withUploadDir(t)
	p, err := SaveAvatarBytes(avatarPNG(t, 60, 60))
	if err != nil {
		t.Fatalf("保存头像失败: %v", err)
	}
	full := filepath.Join(dir, "avatars", strings.TrimPrefix(p, "/uploads/avatars/"))
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("头像文件应存在: %v", err)
	}

	// 非法路径不应删除任何东西
	RemoveAvatar("/uploads/avatars/../clearlove.db")
	RemoveAvatar("/uploads/20260101/xxx.webp")
	RemoveAvatar("")
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("非法路径不应删除头像: %v", err)
	}

	// 合法路径删除成功
	RemoveAvatar(p)
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("头像应已删除，stat err = %v", err)
	}
}
