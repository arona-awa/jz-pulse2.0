package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"clearlove/internal/config"
)

// AppDownload 下载 Android APK。
// 文件放在 data/downloads/jz-pulse.apk，由部署脚本/管理端更新，不进入 Web 静态目录。
func AppDownload(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(config.Cfg.DataDir, "downloads", "jz-pulse.apk")
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="jz-pulse.apk"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeContent(w, r, "jz-pulse.apk", info.ModTime(), f)
}
