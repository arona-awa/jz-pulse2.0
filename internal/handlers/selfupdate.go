// 一键自更新（仅 Linux x86_64）：
// 管理员在后台「检查更新」页点击"立即更新"，程序从 update_url 指向的云端 JSON
// 获取版本信息，下载 linux-amd64 二进制，校验 SHA256 与 ELF 头后原子替换自身，
// 最后通过 syscall.Exec 原地重启（同 PID，外部守护进程无感知）。
//
// 云端 JSON 契约（兼容旧版检查更新的 version/notes/download 字段）：
//
//	{
//	  "version": "2.0.1",
//	  "notes": "更新说明",
//	  "packages": {
//	    "linux-amd64": { "url": "二进制直链", "sha256": "…", "size": 21823488 }
//	  }
//	}
//
// 未提供 packages 时回退使用顶层 download + sha256；缺少 sha256 时拒绝自动更新。
package handlers

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"clearlove/internal/config"
	"clearlove/internal/models"
	"clearlove/internal/util"
)

// selfUpdateMu 防止并发触发两次更新流程
var selfUpdateMu sync.Mutex

// maxSelfUpdateBytes 二进制下载上限
const maxSelfUpdateBytes = 256 << 20

// updatePackage 单平台下载条目
type updatePackage struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// updateInfo 云端更新接口（update_url）返回的 JSON
type updateInfo struct {
	Version  string                   `json:"version"`
	Notes    string                   `json:"notes"`
	Download string                   `json:"download"`
	SHA256   string                   `json:"sha256"`
	Packages map[string]updatePackage `json:"packages"`
}

// AdminUpdateApply 执行一键更新（POST /admin/update/apply）
func AdminUpdateApply(w http.ResponseWriter, r *http.Request) {
	a := requireAdmin(w, r, models.PermSetting)
	if a == nil {
		return
	}
	if !selfUpdateMu.TryLock() {
		fail(w, http.StatusTooManyRequests, "已有一次更新正在进行，请稍后再试")
		return
	}
	defer selfUpdateMu.Unlock()
	applySelfUpdate(w, r, a)
}

func applySelfUpdate(w http.ResponseWriter, r *http.Request, a *models.Admin) {
	// 1) 平台门禁：本特性仅针对 Linux x86_64
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		fail(w, http.StatusBadRequest, fmt.Sprintf("一键更新仅支持 Linux x86_64（当前 %s/%s），请手动替换二进制", runtime.GOOS, runtime.GOARCH))
		return
	}

	// 2) 拉取并校验更新信息
	info, err := fetchUpdateInfo()
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	if info.Version == "" {
		fail(w, http.StatusBadGateway, "更新接口未提供版本号")
		return
	}
	if info.Version <= config.Version {
		fail(w, http.StatusBadRequest, "当前已是最新版本（云端 "+info.Version+"）")
		return
	}
	dlURL, wantSum := linuxAMD64Package(info)
	if dlURL == "" {
		fail(w, http.StatusBadGateway, "更新接口未提供 linux-amd64 下载地址")
		return
	}
	wantSum = strings.ToLower(strings.TrimSpace(wantSum))
	if wantSum == "" {
		fail(w, http.StatusBadGateway, "更新接口未提供 SHA256 校验值，为安全起见拒绝自动更新")
		return
	}

	// 3) 下载到可执行文件同目录的临时文件（保证同分区，rename 原子生效）
	exe, err := os.Executable()
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法定位当前程序："+err.Error())
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".clearlove-update-*.tmp")
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法创建临时文件："+err.Error())
		return
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(dlURL)
	if err != nil {
		cleanup()
		fail(w, http.StatusBadGateway, "下载失败："+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cleanup()
		fail(w, http.StatusBadGateway, "下载失败：云端返回 "+resp.Status)
		return
	}
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxSelfUpdateBytes))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || written <= 0 {
		cleanup()
		fail(w, http.StatusBadGateway, "下载失败：网络中断或内容为空")
		return
	}
	if written >= maxSelfUpdateBytes {
		cleanup()
		fail(w, http.StatusBadGateway, "下载失败：文件超过大小上限")
		return
	}

	// 4) SHA256 校验
	sum, err := fileSHA256(tmpName)
	if err != nil {
		cleanup()
		fail(w, http.StatusInternalServerError, "读取下载文件失败："+err.Error())
		return
	}
	if sum != wantSum {
		cleanup()
		fail(w, http.StatusBadRequest, "SHA256 校验失败，云端文件与校验值不一致（已放弃更新）")
		return
	}

	// 5) ELF 平台校验（防止云端误传其它平台的文件）
	if err := validLinuxAMD64ELF(tmpName); err != nil {
		cleanup()
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	// 6) 补执行权限
	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		fail(w, http.StatusInternalServerError, "设置执行权限失败："+err.Error())
		return
	}

	// 7) 原子替换：旧程序留作 .bak 便于回滚
	bak := exe + ".bak"
	_ = os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		cleanup()
		fail(w, http.StatusInternalServerError, "备份当前程序失败："+err.Error())
		return
	}
	if err := os.Rename(tmpName, exe); err != nil {
		_ = os.Rename(bak, exe)
		fail(w, http.StatusInternalServerError, "替换程序失败（已回滚）："+err.Error())
		return
	}
	util.Log("info", "管理员 %s（#%d）执行一键更新：%s -> %s", a.Username, a.ID, config.Version, info.Version)

	// 8) 先应答，再原地换载新二进制（syscall.Exec，同 PID）
	okJSON(w, map[string]any{
		"msg":     "更新完成（" + config.Version + " → " + info.Version + "），程序正在重启，页面稍后自动刷新",
		"version": info.Version,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	time.Sleep(500 * time.Millisecond)
	if err := restartSelf(exe); err != nil {
		// 新二进制已就位但重启失败：保留新文件，等服务下次重启时生效
		util.Log("error", "自动重启失败，新版本已就位，请手动重启服务：%v", err)
	}
}

// fetchUpdateInfo 请求 update_url 获取更新信息
func fetchUpdateInfo() (*updateInfo, error) {
	url := strings.TrimSpace(models.GetSetting("update_url"))
	if url == "" {
		return nil, errors.New("未配置更新接口地址（网站设置 → 更新接口地址）")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, errors.New("无法连接更新服务器")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("更新服务器返回 %s", resp.Status)
	}
	var info updateInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return nil, errors.New("更新接口返回内容不是有效的 JSON")
	}
	return &info, nil
}

// linuxAMD64Package 取 linux-amd64 的下载地址与校验值；无 packages 时回退顶层字段
func linuxAMD64Package(info *updateInfo) (url, sha string) {
	if info.Packages != nil {
		if p, ok := info.Packages["linux-amd64"]; ok {
			return p.URL, p.SHA256
		}
	}
	return info.Download, info.SHA256
}

// fileSHA256 计算文件 SHA256（十六进制小写）
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validLinuxAMD64ELF 校验文件是 64 位 x86-64 ELF：
// 魔数 \x7fELF、EI_CLASS=2（64 位）、e_machine=62（EM_X86_64）。
func validLinuxAMD64ELF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 20)
	if _, err := io.ReadFull(f, head); err != nil {
		return errors.New("下载文件过小，不是有效的程序文件")
	}
	if head[0] != 0x7f || head[1] != 'E' || head[2] != 'L' || head[3] != 'F' {
		return errors.New("下载文件不是 ELF 程序（云端是否误传了压缩包？）")
	}
	if head[4] != 2 {
		return errors.New("下载文件不是 64 位 ELF")
	}
	if machine := binary.LittleEndian.Uint16(head[18:20]); machine != 62 {
		return errors.New("下载文件不是 x86_64 架构的 ELF")
	}
	return nil
}
