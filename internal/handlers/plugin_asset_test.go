package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"clearlove/internal/config"
)

// TestRemovePluginAsset 素材清理必须定位到 <UploadDir>/plugins/ 下的真实文件。
// 曾经漏掉 plugins 一段，导致重新上传后旧文件一直留在磁盘上。
func TestRemovePluginAsset(t *testing.T) {
	root := t.TempDir()
	config.Cfg.UploadDir = filepath.Join(root, "uploads")
	dir := filepath.Join(config.Cfg.UploadDir, "plugins", "bgm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "audio-abc.mp3")
	if err := os.WriteFile(target, []byte("music"), 0o644); err != nil {
		t.Fatal(err)
	}

	removePluginAsset("/uploads/plugins/bgm/audio-abc.mp3")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("旧素材未被删除: %v", err)
	}

	// 重复删除不应报错，也不应影响其他文件
	removePluginAsset("/uploads/plugins/bgm/audio-abc.mp3")
}

// TestRemovePluginAssetGuards 只清理插件素材目录内的文件，拒绝越权路径
func TestRemovePluginAssetGuards(t *testing.T) {
	root := t.TempDir()
	config.Cfg.UploadDir = filepath.Join(root, "uploads")
	if err := os.MkdirAll(config.Cfg.UploadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(config.Cfg.UploadDir, "keep.webp")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, u := range []string{
		"",
		"/uploads/keep.webp",                   // 不在插件目录内
		"/uploads/plugins/../../keep.webp",     // 路径穿越
		"/uploads/plugins/",                    // 目录本身
		"https://example.com/x.mp3",            // 外部地址
		"/uploads/plugins/../../../etc/passwd", // 绝对越权
	} {
		removePluginAsset(u)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("不应删除插件素材目录之外的文件: %v", err)
	}
}

// TestSanitizePluginAsset 素材地址要能安全地注入页面（不含引号与尖括号）
func TestSanitizePluginAsset(t *testing.T) {
	cases := map[string]string{
		`/uploads/plugins/bgm/a.mp3`:   `/uploads/plugins/bgm/a.mp3`,
		`/uploads/plugins/bgm/a'.mp3`:  `/uploads/plugins/bgm/a.mp3`,
		`"/uploads/plugins/bgm/a.mp3"`: `/uploads/plugins/bgm/a.mp3`,
		`x"><script>alert(1)</script>`: `xalert(1)`, // StripHTML 去掉标签，尖括号与引号也会被清除
	}
	for in, want := range cases {
		if got := sanitizePluginAsset(in); got != want {
			t.Errorf("sanitizePluginAsset(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
