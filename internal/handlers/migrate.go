package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/migrate"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

// 单个上传文件大小上限（MB）
const maxMigrateMB = 200

// migrateJob 迁移任务状态（同一时间只允许一个任务）
type migrateJob struct {
	mu      sync.Mutex
	running bool
	done    bool
	err     string
	stats   *migrate.Stats
	dry     bool
	startAt time.Time
}

var migJob = &migrateJob{}

// snapshot 读取任务状态快照（供页面渲染与轮询接口共用）
func (j *migrateJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[string]any{
		"running": j.running,
		"done":    j.done,
		"err":     j.err,
		"dry_run": j.dry,
	}
	if j.stats != nil {
		out["stats"] = j.stats
	}
	if !j.startAt.IsZero() {
		out["seconds"] = int(time.Since(j.startAt).Seconds())
	}
	return out
}

// AdminMigrate 旧版数据迁移页
func AdminMigrate(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	Render(w, r, "admin_layout.html", "pg_admin_migrate", map[string]any{
		"status": migJob.snapshot(),
		"maxMB":  maxMigrateMB,
	})
}

// AdminMigrateStart 接收旧版数据库文件并异步执行迁移
func AdminMigrateStart(w http.ResponseWriter, r *http.Request) {
	admin := requireAdmin(w, r, models.PermSetting)
	if admin == nil {
		return
	}
	migJob.mu.Lock()
	running := migJob.running
	migJob.mu.Unlock()
	if running {
		fail(w, http.StatusConflict, "已有迁移任务正在执行，请等待完成")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMigrateMB<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		fail(w, 400, fmt.Sprintf("上传失败：文件不能超过 %dMB", maxMigrateMB))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请选择旧版数据库文件")
		return
	}
	defer file.Close()

	tmpDir := filepath.Join(config.Cfg.DataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		fail(w, 500, "无法创建临时目录："+err.Error())
		return
	}
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	switch ext {
	case ".sql", ".sqlite", ".sqlite3":
	default:
		ext = ".db"
	}
	dst := filepath.Join(tmpDir, "migrate-"+util.RandomHex(8)+ext)
	out, err := os.Create(dst)
	if err != nil {
		fail(w, 500, "无法保存上传文件："+err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		_ = os.Remove(dst)
		fail(w, 500, "写入文件失败："+err.Error())
		return
	}
	out.Close()

	uploadsDir := strings.TrimSpace(r.FormValue("uploads_dir"))
	dryRun := r.FormValue("dry") == "1"
	if !startMigrateJob(dst, uploadsDir, dryRun) {
		_ = os.Remove(dst)
		fail(w, http.StatusConflict, "已有迁移任务正在执行，请等待完成")
		return
	}
	util.Log("info", "管理员 %s 启动旧版数据迁移（试运行=%v）：%s", admin.Username, dryRun, hdr.Filename)
	okJSON(w, map[string]any{"msg": "迁移任务已启动"})
}

// AdminMigrateStatus 查询迁移进度（前端轮询）
func AdminMigrateStatus(w http.ResponseWriter, r *http.Request) {
	if a := requireAdmin(w, r, models.PermSetting); a == nil {
		return
	}
	okJSON(w, migJob.snapshot())
}

// startMigrateJob 启动迁移协程，已有任务运行时返回 false
func startMigrateJob(dbFile, uploadsDir string, dryRun bool) bool {
	migJob.mu.Lock()
	if migJob.running {
		migJob.mu.Unlock()
		return false
	}
	migJob.running = true
	migJob.done = false
	migJob.err = ""
	migJob.stats = nil
	migJob.dry = dryRun
	migJob.startAt = time.Now()
	migJob.mu.Unlock()

	go func() {
		st, err := migrate.Run(dbFile, uploadsDir, dryRun)
		migJob.mu.Lock()
		migJob.running = false
		migJob.done = true
		if err != nil {
			migJob.err = err.Error()
		} else {
			migJob.stats = st
		}
		migJob.mu.Unlock()
		// 迁移结束后删除临时上传文件
		_ = os.Remove(dbFile)
	}()
	return true
}
