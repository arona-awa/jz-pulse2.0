package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clearlove/internal/config"
	"clearlove/internal/database"
)

// setup 准备带数据库的插件测试环境（插件配置保存在 settings 表）
func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config.Cfg = config.Config{
		DataDir:    dataDir,
		UploadDir:  filepath.Join(root, "uploads"),
		DBType:     "sqlite",
		SQLitePath: filepath.Join(dataDir, "clearlove.db"),
		Installed:  true,
		Secret:     "test-secret",
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
		t.Fatalf("数据库建表失败: %v", err)
	}
	return dataDir
}

// writePlugin 写入一个插件目录
func writePlugin(t *testing.T, dataDir, name, manifest, fragment string) string {
	t.Helper()
	d := filepath.Join(dataDir, "plugins", name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if fragment != "" {
		if err := os.WriteFile(filepath.Join(d, "player.html"), []byte(fragment), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TestCallHTMLFileAndConfig 验证 html 钩子：@片段文件加载、{config:} 展开与启用开关
func TestCallHTMLFileAndConfig(t *testing.T) {
	dataDir := setup(t)
	writePlugin(t, dataDir, "demo", `{
		"name":"demo","version":"1.0.0",
		"config":[{"key":"music","default":"/uploads/default.mp3"}],
		"hooks":[{"hook":"footer_html","type":"html","html":"@player.html"}]
	}`, `<audio src="{config:music}"></audio>`)

	// 未启用：不注入任何内容
	if got := CallHTML("footer_html"); got != "" {
		t.Fatalf("未启用的插件不应注入内容, got %q", got)
	}

	SetEnabled("demo", true)
	if got := CallHTML("footer_html"); got != `<audio src="/uploads/default.mp3"></audio>` {
		t.Fatalf("默认配置未展开: %q", got)
	}

	p := Load()["demo"]
	if p == nil {
		t.Fatal("插件未被加载")
	}
	if err := SaveConfig(p, map[string]string{"music": "/uploads/plugins/demo/audio-abc.mp3"}); err != nil {
		t.Fatal(err)
	}
	if got := CallHTML("footer_html"); !strings.Contains(got, "/uploads/plugins/demo/audio-abc.mp3") {
		t.Fatalf("配置值未注入页面: %q", got)
	}

	// 片段文件缺失时应跳过该钩子，而不是把 "@player.html" 原样注入页面
	if err := os.Remove(filepath.Join(dataDir, "plugins", "demo", "player.html")); err != nil {
		t.Fatal(err)
	}
	if got := CallHTML("footer_html"); strings.Contains(got, "@player.html") {
		t.Fatalf("片段缺失时不应注入占位文本: %q", got)
	}
}

// TestHookFragmentTraversal @片段文件不允许路径穿越
func TestHookFragmentTraversal(t *testing.T) {
	dataDir := setup(t)
	if err := os.WriteFile(filepath.Join(dataDir, "secret.txt"), []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, dataDir, "evil", `{
		"name":"evil","version":"1.0.0",
		"hooks":[{"hook":"footer_html","type":"html","html":"@../secret.txt"}]
	}`, "")
	SetEnabled("evil", true)
	if got := CallHTML("footer_html"); strings.Contains(got, "TOP-SECRET") {
		t.Fatalf("存在路径穿越读取: %q", got)
	}
}
